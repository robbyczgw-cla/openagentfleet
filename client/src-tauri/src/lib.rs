#[cfg(target_os = "macos")]
mod native_dictation_macos;
#[cfg(target_os = "macos")]
mod secure_prompt_macos;

#[cfg(not(target_os = "macos"))]
mod native_dictation_macos {
    use serde::Serialize;
    use tauri::{AppHandle, State};

    #[derive(Default)]
    pub struct NativeDictationState;

    #[derive(Clone, Debug, Serialize)]
    pub struct NativeDictationStatus {
        pub available: bool,
        pub running: bool,
        pub detail: String,
    }

    pub fn status(_state: State<'_, NativeDictationState>) -> NativeDictationStatus {
        NativeDictationStatus {
            available: false,
            running: false,
            detail: "Native macOS dictation is unavailable on this platform.".to_string(),
        }
    }

    pub fn start(
        _app: AppHandle,
        _state: State<'_, NativeDictationState>,
        _session_id: String,
    ) -> Result<NativeDictationStatus, String> {
        Err("Native macOS dictation is unavailable on this platform.".to_string())
    }

    pub fn stop(
        _app: AppHandle,
        _state: State<'_, NativeDictationState>,
        _session_id: String,
    ) -> Result<(), String> {
        Ok(())
    }

    pub fn cancel(
        _app: AppHandle,
        _state: State<'_, NativeDictationState>,
        _session_id: String,
    ) -> Result<(), String> {
        Ok(())
    }
}

use std::{
    collections::VecDeque,
    env, fs,
    io::{ErrorKind, Read, Write},
    net::{SocketAddr, TcpStream},
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant},
};

use tauri::{AppHandle, Manager, State};

/// Owns only the daemon this app spawned. A healthy local botd without a
/// matching owned child is reported as an instance or port conflict.
#[derive(Default)]
struct DaemonState {
    lifecycle: Mutex<()>,
    child: Mutex<Option<OwnedDaemon>>,
    local_api_token: Mutex<Option<String>>,
    startup_error: Mutex<Option<String>>,
}

const STDERR_CAPTURE_LIMIT: usize = 16 * 1024;
const STARTUP_DIAGNOSTIC_LIMIT: usize = 512;

struct OwnedDaemon {
    child: Child,
    stderr_capture: StderrCapture,
}

struct StderrCapture {
    bytes: Arc<Mutex<VecDeque<u8>>>,
    reader: Option<thread::JoinHandle<()>>,
}

fn append_bounded_stderr(bytes: &Arc<Mutex<VecDeque<u8>>>, input: &[u8]) {
    let Ok(mut bytes) = bytes.lock() else {
        // Keep draining even if the diagnostic buffer was poisoned. A blocked
        // child must never be able to wedge the lifecycle on logging.
        return;
    };
    for byte in input {
        if bytes.len() == STDERR_CAPTURE_LIMIT {
            bytes.pop_front();
        }
        bytes.push_back(*byte);
    }
}

fn capture_child_stderr(child: &mut Child) -> Result<StderrCapture, String> {
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "capture bundled botd diagnostics".to_string())?;
    let bytes = Arc::new(Mutex::new(VecDeque::with_capacity(STDERR_CAPTURE_LIMIT)));
    let reader_bytes = Arc::clone(&bytes);
    let reader = thread::Builder::new()
        .name("bundled-botd-stderr".to_string())
        .spawn(move || {
            let mut stderr = stderr;
            let mut chunk = [0_u8; 4096];
            loop {
                match stderr.read(&mut chunk) {
                    Ok(0) => break,
                    Ok(length) => append_bounded_stderr(&reader_bytes, &chunk[..length]),
                    Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                    Err(_) => break,
                }
            }
        })
        .map_err(|error| format!("capture bundled botd diagnostics: {error}"))?;
    Ok(StderrCapture {
        bytes,
        reader: Some(reader),
    })
}

impl StderrCapture {
    fn finish(mut self) -> Vec<u8> {
        if let Some(reader) = self.reader.take() {
            let _ = reader.join();
        }
        self.bytes
            .lock()
            .map(|bytes| bytes.iter().copied().collect())
            .unwrap_or_default()
    }
}

fn redact_marked_values(input: &str) -> String {
    const MARKERS: &[&str] = &[
        "bearer",
        "authorization",
        "access_token",
        "access-token",
        "refresh_token",
        "refresh-token",
        "api_key",
        "api-key",
        "api_secret",
        "api-secret",
        "apikey",
        "access_key",
        "password",
        "passwd",
        "credential",
        "cookie",
        "session",
        "secret",
        "token",
    ];
    let mut output = input.to_string();
    for marker in MARKERS {
        let mut search_from = 0;
        loop {
            let lower = output.to_ascii_lowercase();
            let Some(relative) = lower[search_from..].find(marker) else {
                break;
            };
            let marker_start = search_from + relative;
            let marker_end = marker_start + marker.len();
            let before_is_boundary =
                marker_start == 0 || !lower.as_bytes()[marker_start - 1].is_ascii_alphanumeric();
            let after_is_boundary =
                marker_end == lower.len() || !lower.as_bytes()[marker_end].is_ascii_alphanumeric();
            if !before_is_boundary || !after_is_boundary {
                search_from = marker_end;
                continue;
            }

            let mut value_start = marker_end;
            while output.as_bytes().get(value_start) == Some(&b' ') {
                value_start += 1;
            }
            if matches!(output.as_bytes().get(value_start), Some(b'=') | Some(b':')) {
                value_start += 1;
                while matches!(output.as_bytes().get(value_start), Some(b' ')) {
                    value_start += 1;
                }
            }
            if matches!(output.as_bytes().get(value_start), Some(b'"') | Some(b'\'')) {
                value_start += 1;
            }
            let Some(first_value_byte) = output.as_bytes().get(value_start) else {
                break;
            };
            if matches!(first_value_byte, b' ' | b'\n' | b'\r' | b',' | b';') {
                search_from = marker_end;
                continue;
            }
            let mut value_end = value_start;
            while let Some(byte) = output.as_bytes().get(value_end) {
                if matches!(
                    byte,
                    b' ' | b'\n' | b'\r' | b',' | b';' | b'"' | b'\'' | b'}'
                ) {
                    break;
                }
                value_end += 1;
            }
            if value_end == value_start {
                search_from = marker_end;
                continue;
            }
            output.replace_range(value_start..value_end, "[redacted]");
            search_from = value_start + "[redacted]".len();
        }
    }
    output
}

fn redact_long_opaque_runs(input: &str) -> String {
    let mut output = String::with_capacity(input.len());
    let mut run = String::new();
    let flush_run = |output: &mut String, run: &mut String| {
        if run.len() >= 24 && run.bytes().any(|byte| byte.is_ascii_digit()) {
            output.push_str("[redacted]");
        } else {
            output.push_str(run);
        }
        run.clear();
    };
    for character in input.chars() {
        if character.is_ascii_alphanumeric() || matches!(character, '_' | '-' | '=' | '+') {
            run.push(character);
        } else {
            flush_run(&mut output, &mut run);
            output.push(character);
        }
    }
    flush_run(&mut output, &mut run);
    output
}

fn bounded_push(output: &mut String, character: char, limit: usize) -> bool {
    let width = character.len_utf8();
    if output.len() + width > limit {
        return false;
    }
    output.push(character);
    true
}

fn sanitize_startup_diagnostic(stderr: &[u8], local_api_token: &str) -> Option<String> {
    let mut text = String::from_utf8_lossy(stderr).into_owned();
    if !local_api_token.is_empty() {
        text = text.replace(local_api_token, "[redacted]");
    }
    text = redact_marked_values(&text);
    text = redact_long_opaque_runs(&text);

    let mut diagnostic = String::new();
    for line in text.lines().map(str::trim).filter(|line| !line.is_empty()) {
        let mut sanitized_line = String::new();
        for character in line.chars() {
            let character = if character.is_control() {
                ' '
            } else {
                character
            };
            if !bounded_push(&mut sanitized_line, character, STARTUP_DIAGNOSTIC_LIMIT) {
                break;
            }
        }
        let sanitized_line = sanitized_line.trim();
        if sanitized_line.is_empty() {
            continue;
        }
        if !diagnostic.is_empty() {
            if diagnostic.len() + 3 > STARTUP_DIAGNOSTIC_LIMIT {
                break;
            }
            diagnostic.push_str(" | ");
        }
        for character in sanitized_line.chars() {
            if !bounded_push(&mut diagnostic, character, STARTUP_DIAGNOSTIC_LIMIT) {
                break;
            }
        }
        if diagnostic.len() == STARTUP_DIAGNOSTIC_LIMIT {
            break;
        }
    }
    (!diagnostic.is_empty()).then_some(diagnostic)
}

fn startup_error_with_diagnostic(error: String, stderr: &[u8], local_api_token: &str) -> String {
    let safe_error = sanitize_startup_diagnostic(error.as_bytes(), local_api_token)
        .unwrap_or_else(|| "bundled botd startup failed".to_string());
    let diagnostic = sanitize_startup_diagnostic(stderr, local_api_token);
    let suffix = diagnostic
        .as_deref()
        .map(|diagnostic| format!("; diagnostic: {diagnostic}"));
    let mut result = String::new();
    for character in safe_error
        .chars()
        .chain(suffix.iter().flat_map(|value| value.chars()))
    {
        if !bounded_push(&mut result, character, STARTUP_DIAGNOSTIC_LIMIT) {
            break;
        }
    }
    result
}

#[derive(serde::Serialize)]
struct LocalApiAuth {
    token: Option<String>,
    startup_error: Option<String>,
}

#[tauri::command]
fn local_api_auth(app: AppHandle) -> LocalApiAuth {
    let state = app.state::<DaemonState>();
    let token = state
        .local_api_token
        .lock()
        .ok()
        .and_then(|value| value.clone());
    let startup_error = state
        .startup_error
        .lock()
        .ok()
        .and_then(|value| value.clone());
    LocalApiAuth {
        token,
        startup_error,
    }
}

fn set_startup_error(app: &AppHandle, error: Option<String>) {
    let state = app.state::<DaemonState>();
    if let Ok(mut guard) = state.startup_error.lock() {
        *guard = error;
    };
}

fn new_local_api_token() -> Result<String, String> {
    let mut bytes = [0_u8; 32];
    getrandom::fill(&mut bytes)
        .map_err(|error| format!("generate local API credential: {error}"))?;
    const HEX: &[u8; 16] = b"0123456789abcdef";
    let mut token = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        token.push(HEX[usize::from(byte >> 4)] as char);
        token.push(HEX[usize::from(byte & 0x0f)] as char);
    }
    Ok(token)
}

fn botd_health_response(response: &[u8]) -> bool {
    let response = String::from_utf8_lossy(response);
    response.starts_with("HTTP/1.1 200")
        && response.contains("\"ok\":true")
        && response.contains("\"service\":\"botd\"")
}

fn local_botd_is_healthy() -> bool {
    let address: SocketAddr = match "127.0.0.1:4317".parse() {
        Ok(address) => address,
        Err(_) => return false,
    };
    let mut stream = match TcpStream::connect_timeout(&address, Duration::from_millis(250)) {
        Ok(stream) => stream,
        Err(_) => return false,
    };
    let _ = stream.set_read_timeout(Some(Duration::from_millis(500)));
    if stream
        .write_all(b"GET /health HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
        .is_err()
    {
        return false;
    }
    let mut response = [0_u8; 512];
    match stream.read(&mut response) {
        Ok(length) => botd_health_response(&response[..length]),
        Err(_) => false,
    }
}

fn local_botd_port_is_occupied() -> bool {
    let address: SocketAddr = match "127.0.0.1:4317".parse() {
        Ok(address) => address,
        Err(_) => return false,
    };
    TcpStream::connect_timeout(&address, Duration::from_millis(250)).is_ok()
}

fn local_botd_conflict() -> Option<String> {
    if local_botd_is_healthy() {
        return Some(
            "another botd is already serving 127.0.0.1:4317; OpenAgentFleet will not adopt it"
                .to_string(),
        );
    }
    if local_botd_port_is_occupied() {
        return Some(
            "127.0.0.1:4317 is already in use; stop the other service before starting OpenAgentFleet"
                .to_string(),
        );
    }
    None
}

fn bundled_executable_path(name: &str) -> Result<PathBuf, String> {
    let executable = env::current_exe()
        .map_err(|error| format!("resolve OpenAgentFleet executable: {error}"))?;
    let parent = executable
        .parent()
        .ok_or_else(|| "resolve bundled executable directory".to_string())?;
    let sidecar = parent.join(name);
    if sidecar.is_file() {
        Ok(sidecar)
    } else {
        Err(format!("bundled {name} executable is unavailable"))
    }
}

fn bundled_sidecar_path() -> Result<PathBuf, String> {
    bundled_executable_path("botd")
}

fn configure_sidecar_environment(
    command: &mut Command,
    local_api_token: &str,
    data_dir: &std::path::Path,
) -> Result<(), String> {
    // `botd` must find Docker and the user's already-authorized worker CLIs,
    // but it must not inherit arbitrary API keys or controller credentials.
    command.env_clear();
    for variable in [
        "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "SHELL",
    ] {
        if let Some(value) = env::var_os(variable) {
            command.env(variable, value);
        }
    }
    let inherited_path = env::var_os("PATH")
        .unwrap_or_else(|| "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin".into());
    let uv = bundled_executable_path("uv")?;
    let uvx = bundled_executable_path("uvx")?;
    let opencode = bundled_executable_path("opencode")?;
    let browser_mcp = bundled_executable_path("browser-mcp")?;
    let tool_dir = uv
        .parent()
        .ok_or_else(|| "resolve bundled WebSearchPlus launcher directory".to_string())?;
    let mut path_entries = vec![tool_dir.to_path_buf()];
    path_entries.extend(env::split_paths(&inherited_path));
    let sidecar_path = env::join_paths(path_entries)
        .map_err(|error| format!("build bundled botd PATH: {error}"))?;
    command.env("PATH", sidecar_path);
    command.env("OPENAGENTFLEET_REMOTE_TOKEN", local_api_token);
    command.env("OPENAGENTFLEET_UV_BINARY", uv);
    command.env("OPENAGENTFLEET_UVX_BINARY", uvx);
    command.env("OPENAGENTFLEET_OPENCODE_BINARY", opencode);
    command.env("OPENAGENTFLEET_BROWSER_MCP_BINARY", browser_mcp);
    command.env(
        "OPENAGENTFLEET_WEBSEARCH_DATA_DIR",
        data_dir.join("web-search"),
    );
    // The packaged product must be usable without asking users to relaunch a
    // GUI app from Terminal with hidden environment gates. Actual write/tool
    // authority remains controlled by the Agent permission profile and the
    // controller approval broker; starting the app alone runs no harness and
    // creates no container.
    command.env("OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION", "1");
    command.env("OPENAGENTFLEET_ALLOW_HARNESS_WORKSPACE_WRITES", "1");
    command.env("OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION", "1");
    // Advanced remote-computer routing remains opt-in. The token is read only
    // from the launch environment and is never persisted in preferences or
    // exposed to the webview.
    if let Some(remote_token) = env::var_os("OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN") {
        command.env("OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN", remote_token);
    }
    Ok(())
}

fn wait_for_local_botd(child: &mut Child) -> Result<(), String> {
    // First launch inventories local harnesses and seeds a fresh database. On
    // slower Macs that legitimately takes longer than a warm controller start.
    let deadline = Instant::now() + Duration::from_secs(15);
    while Instant::now() < deadline {
        if local_botd_is_healthy() {
            return Ok(());
        }
        if let Some(status) = child
            .try_wait()
            .map_err(|error| format!("inspect bundled botd startup: {error}"))?
        {
            return Err(format!("bundled botd exited during startup ({status})"));
        }
        thread::sleep(Duration::from_millis(80));
    }
    Err("bundled botd did not become ready within 15 seconds".to_string())
}

fn owned_child_is_live(app: &AppHandle) -> Result<bool, String> {
    let state = app.state::<DaemonState>();
    let exited = {
        let mut guard = state
            .child
            .lock()
            .map_err(|_| "inspect bundled botd process".to_string())?;
        let Some(daemon) = guard.as_mut() else {
            return Ok(false);
        };
        match daemon
            .child
            .try_wait()
            .map_err(|error| format!("inspect bundled botd process: {error}"))?
        {
            None => return Ok(true),
            Some(_) => {
                // try_wait reaps an exited child; remove its ownership record.
                guard.take()
            }
        }
    };
    if let Some(daemon) = exited {
        let _ = daemon.stderr_capture.finish();
        if let Ok(mut guard) = state.local_api_token.lock() {
            guard.take();
        }
    }
    Ok(false)
}

#[cfg(target_os = "macos")]
fn request_sigterm(child: &Child) {
    let pid = child.id() as libc::pid_t;
    // The child is retained in DaemonState, so this PID is still owned by the
    // current app while shutdown is in progress.
    unsafe {
        let _ = libc::kill(pid, libc::SIGTERM);
    }
}

#[cfg(not(target_os = "macos"))]
fn request_sigterm(child: &Child) {
    let _ = child;
}

fn terminate_owned_child(child: &mut Child) {
    const GRACE_PERIOD: Duration = Duration::from_secs(2);
    const POLL_INTERVAL: Duration = Duration::from_millis(25);

    match child.try_wait() {
        Ok(Some(_)) => return,
        Ok(None) | Err(_) => {}
    }
    request_sigterm(child);
    let deadline = Instant::now() + GRACE_PERIOD;
    while Instant::now() < deadline {
        match child.try_wait() {
            Ok(Some(_)) => return,
            Ok(None) => thread::sleep(POLL_INTERVAL),
            Err(_) => break,
        }
    }

    let _ = child.kill();
    // Reap the child after the force-kill fallback. kill() is expected to
    // succeed for the owned process; wait() also handles an already-exited
    // child without leaving a zombie behind.
    let _ = child.wait();
}

fn start_bundled_daemon(app: &AppHandle) -> Result<(), String> {
    let state = app.state::<DaemonState>();
    let _lifecycle_guard = state
        .lifecycle
        .lock()
        .map_err(|_| "serialize bundled botd lifecycle".to_string())?;
    if owned_child_is_live(app)? {
        set_startup_error(app, None);
        return Ok(());
    }
    if let Some(error) = local_botd_conflict() {
        return Err(error);
    }

    let local_api_token = new_local_api_token()?;

    #[cfg(debug_assertions)]
    let data_dir = match env::var_os("OPENAGENTFLEET_DEV_DATA_DIR") {
        Some(value) => {
            let path = PathBuf::from(value);
            if !path.is_absolute() {
                return Err("OPENAGENTFLEET_DEV_DATA_DIR must be an absolute path".to_string());
            }
            path
        }
        None => app
            .path()
            .app_data_dir()
            .map_err(|error| format!("resolve OpenAgentFleet data directory: {error}"))?,
    };
    #[cfg(not(debug_assertions))]
    let data_dir = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve OpenAgentFleet data directory: {error}"))?;
    fs::create_dir_all(&data_dir)
        .map_err(|error| format!("create OpenAgentFleet data directory: {error}"))?;
    let resource_dir = app
        .path()
        .resource_dir()
        .map_err(|error| format!("resolve OpenAgentFleet resources: {error}"))?;
    let build_context = resource_dir.join("agent-computer");
    if !build_context.join("Dockerfile").is_file() {
        return Err("bundled Agent Computer image is unavailable".to_string());
    }

    let args = [
        "-addr".to_string(),
        "127.0.0.1:4317".to_string(),
        "-mobile-addr".to_string(),
        "127.0.0.1:4318".to_string(),
        "-data-dir".to_string(),
        data_dir.to_string_lossy().into_owned(),
        "-build-context".to_string(),
        build_context.to_string_lossy().into_owned(),
    ];
    let sidecar = bundled_sidecar_path()?;
    let mut command = Command::new(sidecar);
    command
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    // Every packaged app instance gets a fresh in-memory credential. The
    // Tauri webview retrieves it through `local_api_auth`; no token is
    // written to disk or compiled into the frontend bundle.
    configure_sidecar_environment(&mut command, &local_api_token, &data_dir)?;
    let mut child = command
        .spawn()
        .map_err(|error| format!("start bundled botd: {error}"))?;
    let stderr_capture = match capture_child_stderr(&mut child) {
        Ok(capture) => capture,
        Err(error) => {
            terminate_owned_child(&mut child);
            return Err(error);
        }
    };
    if let Err(error) = wait_for_local_botd(&mut child) {
        terminate_owned_child(&mut child);
        let stderr = stderr_capture.finish();
        if let Some(conflict) = local_botd_conflict() {
            return Err(conflict);
        }
        return Err(startup_error_with_diagnostic(
            error,
            &stderr,
            &local_api_token,
        ));
    }

    let mut guard = state
        .child
        .lock()
        .map_err(|_| "record bundled botd process".to_string())?;
    *guard = Some(OwnedDaemon {
        child,
        stderr_capture,
    });
    let mut token_guard = state
        .local_api_token
        .lock()
        .map_err(|_| "record bundled botd credential".to_string())?;
    *token_guard = Some(local_api_token);
    set_startup_error(app, None);
    Ok(())
}

fn stop_bundled_daemon(app: &AppHandle) {
    let state = app.state::<DaemonState>();
    let _lifecycle_guard = state.lifecycle.lock().ok();
    if let Ok(mut guard) = state.child.lock() {
        if let Some(mut daemon) = guard.take() {
            terminate_owned_child(&mut daemon.child);
            let _ = daemon.stderr_capture.finish();
        }
    };
    if let Ok(mut guard) = state.local_api_token.lock() {
        if let Some(mut token) = guard.take() {
            token.clear();
        }
    };
}

fn validate_prompt_request(handoff_id: &str, purpose: &str) -> Result<(), String> {
    let suffix = handoff_id
        .strip_prefix("handoff_")
        .ok_or_else(|| "invalid secure handoff request".to_string())?;
    if suffix.len() != 32
        || !suffix
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    {
        return Err("invalid secure handoff request".to_string());
    }

    match purpose {
        "password" | "two_factor_code" => Ok(()),
        _ => Err("unsupported secure prompt purpose".to_string()),
    }
}

#[tauri::command(rename_all = "snake_case")]
async fn prompt_secret(
    app: tauri::AppHandle,
    handoff_id: String,
    purpose: String,
) -> Result<(), String> {
    validate_prompt_request(&handoff_id, &purpose)?;

    #[cfg(target_os = "macos")]
    {
        return tauri::async_runtime::spawn_blocking(move || {
            secure_prompt_macos::prompt_and_submit(&app, &handoff_id, &purpose)
        })
        .await
        .map_err(|_| "secure prompt failed".to_string())?;
    }

    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        let _ = handoff_id;
        let _ = purpose;
        Err("secure prompt is supported on macOS only".to_string())
    }
}

#[tauri::command]
fn native_dictation_status(
    state: State<'_, native_dictation_macos::NativeDictationState>,
) -> native_dictation_macos::NativeDictationStatus {
    native_dictation_macos::status(state)
}

#[tauri::command(rename_all = "snake_case")]
fn native_dictation_start(
    app: AppHandle,
    state: State<'_, native_dictation_macos::NativeDictationState>,
    session_id: String,
) -> Result<native_dictation_macos::NativeDictationStatus, String> {
    native_dictation_macos::start(app, state, session_id)
}

#[tauri::command(rename_all = "snake_case")]
fn native_dictation_stop(
    app: AppHandle,
    state: State<'_, native_dictation_macos::NativeDictationState>,
    session_id: String,
) -> Result<(), String> {
    native_dictation_macos::stop(app, state, session_id)
}

#[tauri::command(rename_all = "snake_case")]
fn native_dictation_cancel(
    app: AppHandle,
    state: State<'_, native_dictation_macos::NativeDictationState>,
    session_id: String,
) -> Result<(), String> {
    native_dictation_macos::cancel(app, state, session_id)
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .setup(|app| {
            app.manage(DaemonState::default());
            app.manage(native_dictation_macos::NativeDictationState::default());
            if let Err(error) = start_bundled_daemon(&app.handle()) {
                set_startup_error(&app.handle(), Some(error.clone()));
                eprintln!("OpenAgentFleet could not start bundled botd: {error}");
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            prompt_secret,
            local_api_auth,
            native_dictation_status,
            native_dictation_start,
            native_dictation_stop,
            native_dictation_cancel
        ])
        .build(tauri::generate_context!())
        .expect("error while building OpenAgentFleet")
        .run(|app, event| {
            if matches!(event, tauri::RunEvent::Exit) {
                stop_bundled_daemon(app);
            }
        })
}

#[cfg(test)]
mod tests {
    use super::{
        append_bounded_stderr, botd_health_response, new_local_api_token,
        sanitize_startup_diagnostic, startup_error_with_diagnostic, validate_prompt_request,
        STARTUP_DIAGNOSTIC_LIMIT, STDERR_CAPTURE_LIMIT,
    };
    #[cfg(target_os = "macos")]
    use super::terminate_owned_child;
    #[cfg(target_os = "macos")]
    use std::process::Command;
    use std::{
        collections::VecDeque,
        sync::{Arc, Mutex},
    };

    #[test]
    fn accepts_only_native_supported_handoff_metadata() {
        let id = "handoff_0123456789abcdef0123456789abcdef";
        assert!(validate_prompt_request(id, "password").is_ok());
        assert!(validate_prompt_request(id, "two_factor_code").is_ok());
        assert!(validate_prompt_request(id, "captcha").is_err());
        assert!(validate_prompt_request("handoff_short", "password").is_err());
        assert!(
            validate_prompt_request("wrong_0123456789abcdef0123456789abcdef", "password").is_err()
        );
    }

    #[test]
    fn recognizes_only_a_healthy_botd_response() {
        assert!(botd_health_response(
            b"HTTP/1.1 200 OK\r\n\r\n{\"ok\":true,\"service\":\"botd\"}\n"
        ));
        assert!(!botd_health_response(
            b"HTTP/1.1 200 OK\r\n\r\n{\"ok\":true,\"service\":\"another-service\"}\n"
        ));
        assert!(!botd_health_response(
            b"HTTP/1.1 503 Service Unavailable\r\n\r\n{\"service\":\"botd\"}\n"
        ));
    }

    #[test]
    fn creates_non_empty_ephemeral_local_api_credentials() {
        let first = new_local_api_token().expect("generate first local token");
        let second = new_local_api_token().expect("generate second local token");
        assert_eq!(first.len(), 64);
        assert!(first.bytes().all(|byte| byte.is_ascii_hexdigit()));
        assert_ne!(first, second);
    }

    #[test]
    fn bounds_stderr_capture_to_the_latest_bytes() {
        let bytes = Arc::new(Mutex::new(VecDeque::new()));
        let input = vec![b'x'; STDERR_CAPTURE_LIMIT + 17];

        append_bounded_stderr(&bytes, &input);

        let captured = bytes.lock().expect("read bounded stderr capture");
        assert_eq!(captured.len(), STDERR_CAPTURE_LIMIT);
        assert!(captured.iter().all(|byte| *byte == b'x'));
    }

    #[test]
    fn sanitizes_tokens_secrets_and_opaque_values_with_a_hard_bound() {
        let token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
        let stderr = format!(
            "level=ERROR OPENAGENTFLEET_REMOTE_TOKEN={token} authorization: Bearer bearer-secret secret=top-secret nonce=abcdefghijklmnopqrstuvwxyz0123456789\n{}",
            "diagnostic ".repeat(200)
        );

        let diagnostic = sanitize_startup_diagnostic(stderr.as_bytes(), token)
            .expect("produce sanitized startup diagnostic");

        assert!(diagnostic.len() <= STARTUP_DIAGNOSTIC_LIMIT);
        assert!(!diagnostic.contains(token));
        assert!(!diagnostic.contains("bearer-secret"));
        assert!(!diagnostic.contains("top-secret"));
        assert!(!diagnostic.contains("abcdefghijklmnopqrstuvwxyz0123456789"));
        assert!(!diagnostic.chars().any(char::is_control));
    }

    #[test]
    fn startup_error_keeps_diagnostic_bounded_and_redacted() {
        let token = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd";
        let error = startup_error_with_diagnostic(
            "bundled botd exited during startup (exit status: 1)".to_string(),
            format!("fatal: token={token} secret=do-not-leak").as_bytes(),
            token,
        );

        assert!(error.len() <= STARTUP_DIAGNOSTIC_LIMIT);
        assert!(error.contains("diagnostic:"));
        assert!(!error.contains(token));
        assert!(!error.contains("do-not-leak"));
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn graceful_termination_reaps_owned_child() {
        let mut child = Command::new("/bin/sleep")
            .arg("30")
            .spawn()
            .expect("spawn disposable child");

        terminate_owned_child(&mut child);

        assert!(child
            .try_wait()
            .expect("inspect terminated disposable child")
            .is_some());
    }
}
