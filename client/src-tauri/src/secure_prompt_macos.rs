use std::env;
use std::fs;
use std::io::{Read, Write};
use std::net::{Shutdown, SocketAddr, TcpStream};
use std::os::unix::fs::{FileTypeExt, MetadataExt, PermissionsExt};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::time::Duration;

use objc2::{MainThreadMarker, MainThreadOnly};
use objc2_app_kit::{NSAlert, NSAlertFirstButtonReturn, NSSecureTextField};
use objc2_foundation::{ns_string, NSPoint, NSRect, NSSize, NSUTF8StringEncoding};
use serde::Deserialize;
use tauri::AppHandle;
use zeroize::{Zeroize, Zeroizing};

const BOTD_ADDR: &str = "127.0.0.1:4317";
const TRANSPORT_ENDPOINT: &str = "/api/secret-handoffs/transport";
const IO_TIMEOUT: Duration = Duration::from_secs(3);
const MAX_HTTP_RESPONSE: u64 = 16 * 1024;
const MAX_HTTP_BODY: usize = 8 * 1024;
const MAX_TOKEN_LENGTH: usize = 4 * 1024;
const MAX_HANDOFF_ID: usize = 256;
const MAX_SECRET: usize = 64 * 1024;
const MAX_SOCKET_RESPONSE: usize = 128;
const REQUEST_MAGIC: &[u8; 4] = b"OFBH";
const PROTOCOL_VERSION: u8 = 1;

#[derive(Deserialize)]
struct TransportResponse {
    available: bool,
    socket_path: String,
    protocol: String,
}

enum PromptOutcome {
    Accepted(Zeroizing<Vec<u8>>),
    Cancelled,
    Failed,
}

pub(crate) fn prompt_and_submit(
    app: &AppHandle,
    handoff_id: &str,
    purpose: &str,
) -> Result<(), String> {
    let socket_path = fetch_socket_path().map_err(|_| generic_failure())?;
    let mut secret = show_secure_prompt(app, purpose)?;
    let result = submit_secret(&socket_path, handoff_id, secret.as_slice());
    secret.zeroize();
    result.map_err(|_| generic_failure())
}

fn show_secure_prompt(app: &AppHandle, purpose: &str) -> Result<Zeroizing<Vec<u8>>, String> {
    let (sender, receiver) = mpsc::sync_channel(1);
    let is_two_factor_code = purpose == "two_factor_code";

    app.run_on_main_thread(move || {
        let outcome = objc2::rc::autoreleasepool(|_| {
            let Some(main_thread) = MainThreadMarker::new() else {
                return PromptOutcome::Failed;
            };

            let alert = NSAlert::new(main_thread);
            alert.setMessageText(if is_two_factor_code {
                ns_string!("Enter verification code")
            } else {
                ns_string!("Enter password securely")
            });
            alert.setInformativeText(ns_string!(
                "OpenAgentFleet sends this value once to the approved focused target. It is not returned to the web interface."
            ));
            alert.addButtonWithTitle(ns_string!("Enter"));
            alert.addButtonWithTitle(ns_string!("Cancel"));

            let field = NSSecureTextField::initWithFrame(
                NSSecureTextField::alloc(main_thread),
                NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(360.0, 24.0)),
            );
            alert.setAccessoryView(Some(&field));

            let response = alert.runModal();
            if response != NSAlertFirstButtonReturn {
                field.setStringValue(ns_string!(""));
                return PromptOutcome::Cancelled;
            }

            let value = field.stringValue();
            // NSString::len is measured in UTF-16 code units. The native
            // protocol transports UTF-8 bytes, so use the matching byte
            // length before copying from UTF8String. Otherwise a non-ASCII
            // password could be truncated before it reaches the target.
            let byte_length = value.lengthOfBytesUsingEncoding(NSUTF8StringEncoding);
            if byte_length == 0 || byte_length > MAX_SECRET {
                field.setStringValue(ns_string!(""));
                return PromptOutcome::Failed;
            }

            let pointer = value.UTF8String();
            if pointer.is_null() {
                field.setStringValue(ns_string!(""));
                return PromptOutcome::Failed;
            }

            // SAFETY: UTF8String points to at least `byte_length` UTF-8 bytes
            // for the lifetime of `value`. The bytes are copied immediately
            // into a zeroizing allocation before the AppKit field is cleared.
            let bytes = unsafe {
                std::slice::from_raw_parts(pointer.cast::<u8>(), byte_length)
            };
            let secret = Zeroizing::new(bytes.to_vec());
            field.setStringValue(ns_string!(""));
            PromptOutcome::Accepted(secret)
        });
        let _ = sender.send(outcome);
    })
    .map_err(|_| generic_failure())?;

    match receiver.recv().map_err(|_| generic_failure())? {
        PromptOutcome::Accepted(secret) => Ok(secret),
        PromptOutcome::Cancelled => Err("secure prompt cancelled".to_string()),
        PromptOutcome::Failed => Err(generic_failure()),
    }
}

fn fetch_socket_path() -> Result<PathBuf, ()> {
    let address: SocketAddr = BOTD_ADDR.parse().map_err(|_| ())?;
    let mut stream = TcpStream::connect_timeout(&address, IO_TIMEOUT).map_err(|_| ())?;
    stream.set_read_timeout(Some(IO_TIMEOUT)).map_err(|_| ())?;
    stream.set_write_timeout(Some(IO_TIMEOUT)).map_err(|_| ())?;

    let token = env::var("OPENAGENTFLEET_BOTD_TOKEN")
        .ok()
        .map(Zeroizing::new);
    if let Some(value) = token.as_deref() {
        if value.is_empty()
            || value.len() > MAX_TOKEN_LENGTH
            || value.bytes().any(|byte| byte == b'\r' || byte == b'\n')
        {
            return Err(());
        }
    }

    let mut request = Zeroizing::new(format!(
        "GET {TRANSPORT_ENDPOINT} HTTP/1.1\r\nHost: {BOTD_ADDR}\r\nAccept: application/json\r\nConnection: close\r\n"
    ));
    if let Some(value) = token.as_deref() {
        request.push_str("Authorization: Bearer ");
        request.push_str(value);
        request.push_str("\r\n");
    }
    request.push_str("\r\n");
    stream.write_all(request.as_bytes()).map_err(|_| ())?;
    stream.shutdown(Shutdown::Write).map_err(|_| ())?;

    let mut response = Vec::new();
    stream
        .take(MAX_HTTP_RESPONSE + 1)
        .read_to_end(&mut response)
        .map_err(|_| ())?;
    if response.len() as u64 > MAX_HTTP_RESPONSE {
        return Err(());
    }

    let body = parse_http_response(&response)?;
    let transport: TransportResponse = serde_json::from_slice(&body).map_err(|_| ())?;
    if !transport.available || !transport.protocol.eq_ignore_ascii_case("ofbh/1") {
        return Err(());
    }
    if transport.socket_path.is_empty()
        || transport.socket_path.len() > 1024
        || transport.socket_path.as_bytes().contains(&0)
    {
        return Err(());
    }

    let path = PathBuf::from(transport.socket_path);
    if !path.is_absolute() {
        return Err(());
    }
    let expected_uid = unsafe { libc::geteuid() };
    let parent = path.parent().ok_or(())?;
    let parent_metadata = fs::symlink_metadata(parent).map_err(|_| ())?;
    if !parent_metadata.file_type().is_dir()
        || parent_metadata.uid() != expected_uid
        || parent_metadata.permissions().mode() & 0o077 != 0
    {
        return Err(());
    }
    let metadata = fs::symlink_metadata(&path).map_err(|_| ())?;
    if !metadata.file_type().is_socket()
        || metadata.uid() != expected_uid
        || metadata.permissions().mode() & 0o777 != 0o600
    {
        return Err(());
    }
    Ok(path)
}

fn parse_http_response(response: &[u8]) -> Result<Vec<u8>, ()> {
    let separator = response
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .ok_or(())?;
    let header = &response[..separator];
    let body = &response[separator + 4..];
    let header_text = std::str::from_utf8(header).map_err(|_| ())?;
    let mut lines = header_text.split("\r\n");
    let status = lines.next().ok_or(())?;
    if status != "HTTP/1.1 200 OK" && status != "HTTP/1.0 200 OK" {
        return Err(());
    }

    let mut chunked = false;
    let mut content_length = None;
    for line in lines {
        let (name, value) = line.split_once(':').ok_or(())?;
        if name.eq_ignore_ascii_case("transfer-encoding") {
            chunked = value
                .split(',')
                .any(|item| item.trim().eq_ignore_ascii_case("chunked"));
        } else if name.eq_ignore_ascii_case("content-length") {
            let length = value.trim().parse::<usize>().map_err(|_| ())?;
            if content_length.replace(length).is_some() {
                return Err(());
            }
        }
    }

    let decoded = if chunked {
        decode_chunked(body)?
    } else {
        if let Some(length) = content_length {
            if length != body.len() {
                return Err(());
            }
        }
        body.to_vec()
    };
    if decoded.len() > MAX_HTTP_BODY {
        return Err(());
    }
    Ok(decoded)
}

fn decode_chunked(mut input: &[u8]) -> Result<Vec<u8>, ()> {
    let mut output = Vec::new();
    loop {
        let line_end = input
            .windows(2)
            .position(|window| window == b"\r\n")
            .ok_or(())?;
        let size_text = std::str::from_utf8(&input[..line_end]).map_err(|_| ())?;
        let size = usize::from_str_radix(size_text.split(';').next().ok_or(())?.trim(), 16)
            .map_err(|_| ())?;
        input = &input[line_end + 2..];
        if size == 0 {
            if input == b"\r\n" || input.starts_with(b"\r\n") {
                return Ok(output);
            }
            return Err(());
        }
        if size > MAX_HTTP_BODY.saturating_sub(output.len())
            || input.len() < size + 2
            || &input[size..size + 2] != b"\r\n"
        {
            return Err(());
        }
        output.extend_from_slice(&input[..size]);
        input = &input[size + 2..];
    }
}

fn submit_secret(path: &Path, handoff_id: &str, secret: &[u8]) -> Result<(), ()> {
    if handoff_id.is_empty()
        || handoff_id.len() > MAX_HANDOFF_ID
        || secret.is_empty()
        || secret.len() > MAX_SECRET
    {
        return Err(());
    }

    let id_length = u16::try_from(handoff_id.len()).map_err(|_| ())?;
    let secret_length = u32::try_from(secret.len()).map_err(|_| ())?;
    let mut stream = UnixStream::connect(path).map_err(|_| ())?;
    stream.set_read_timeout(Some(IO_TIMEOUT)).map_err(|_| ())?;
    stream.set_write_timeout(Some(IO_TIMEOUT)).map_err(|_| ())?;

    let mut header = [0u8; 12];
    header[..4].copy_from_slice(REQUEST_MAGIC);
    header[4] = PROTOCOL_VERSION;
    header[6..8].copy_from_slice(&id_length.to_be_bytes());
    header[8..12].copy_from_slice(&secret_length.to_be_bytes());
    stream.write_all(&header).map_err(|_| ())?;
    stream.write_all(handoff_id.as_bytes()).map_err(|_| ())?;
    stream.write_all(secret).map_err(|_| ())?;
    stream.shutdown(Shutdown::Write).map_err(|_| ())?;

    let mut response_header = [0u8; 8];
    stream.read_exact(&mut response_header).map_err(|_| ())?;
    if &response_header[..4] != REQUEST_MAGIC
        || response_header[4] != PROTOCOL_VERSION
        || response_header[5] != 0
    {
        return Err(());
    }
    let message_length = u16::from_be_bytes([response_header[6], response_header[7]]) as usize;
    if message_length > MAX_SOCKET_RESPONSE {
        return Err(());
    }
    let mut message = vec![0u8; message_length];
    stream.read_exact(&mut message).map_err(|_| ())?;
    let accepted = message == b"accepted";
    message.zeroize();
    if !accepted {
        return Err(());
    }
    Ok(())
}

fn generic_failure() -> String {
    "secure handoff failed".to_string()
}
