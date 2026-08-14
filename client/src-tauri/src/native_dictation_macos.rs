//! Typed macOS Speech/AVFAudio bridge for the desktop composer.
//!
//! This module intentionally keeps audio inside Apple's on-device speech path.
//! It emits text events to the WebView and never writes an audio file or sends
//! audio through botd. The browser MediaRecorder path remains the explicit
//! fallback for browser/mobile clients and Macs where on-device recognition is
//! unavailable.

use std::{
    ptr::NonNull,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc, Condvar, Mutex,
    },
    time::Duration,
};

use block2::RcBlock;
use objc2::{rc::Retained, runtime::Bool};
use objc2_avf_audio::{
    AVAudioApplication, AVAudioEngine, AVAudioInputNode, AVAudioPCMBuffer, AVAudioTime,
};
use objc2_foundation::NSError;
use objc2_speech::{
    SFSpeechAudioBufferRecognitionRequest, SFSpeechRecognitionResult, SFSpeechRecognitionTask,
    SFSpeechRecognizer, SFSpeechRecognizerAuthorizationStatus,
};
use serde::Serialize;
use tauri::{AppHandle, Emitter, State};

const PERMISSION_TIMEOUT: Duration = Duration::from_secs(30);
const STOP_GRACE: Duration = Duration::from_millis(350);

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct NativeDictationEvent {
    pub session_id: String,
    pub state: String,
    pub text: Option<String>,
    pub detail: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct NativeDictationStatus {
    pub available: bool,
    pub running: bool,
    pub detail: String,
}

pub struct NativeDictationState {
    session: Mutex<Option<NativeDictationLifecycle>>,
}

impl Default for NativeDictationState {
    fn default() -> Self {
        Self {
            session: Mutex::new(None),
        }
    }
}

// AVAudioEngine and Speech callbacks are designed to be used from audio and
// recognition queues. Access from commands is serialized by `session`; Apple
// owns the actual thread-safe audio graph. RcBlock itself intentionally does
// not advertise Send/Sync, so this is the single, documented unsafe boundary
// of the native bridge.
unsafe impl Send for NativeDictationState {}
unsafe impl Sync for NativeDictationState {}

struct NativeDictationSession {
    id: String,
    engine: Retained<AVAudioEngine>,
    input: Retained<AVAudioInputNode>,
    request: Retained<SFSpeechAudioBufferRecognitionRequest>,
    task: Retained<SFSpeechRecognitionTask>,
    completion: Arc<(Mutex<bool>, Condvar)>,
    // The Objective-C APIs retain the blocks after start/installTap returns.
    // Keep Rust owners alive until the session is stopped as well.
    _recognition_handler: RcBlock<dyn Fn(*mut SFSpeechRecognitionResult, *mut NSError)>,
    _tap_block: RcBlock<dyn Fn(NonNull<AVAudioPCMBuffer>, NonNull<AVAudioTime>)>,
}

enum NativeDictationLifecycle {
    Starting {
        id: String,
        cancelled: Arc<AtomicBool>,
    },
    Running(NativeDictationSession),
}

fn emit(app: &AppHandle, event: NativeDictationEvent) {
    let _ = app.emit("native-dictation", event);
}

fn emit_state(app: &AppHandle, session_id: &str, state: &str, detail: Option<String>) {
    emit(
        app,
        NativeDictationEvent {
            session_id: session_id.to_string(),
            state: state.to_string(),
            text: None,
            detail,
        },
    );
}

fn authorization_error(status: SFSpeechRecognizerAuthorizationStatus) -> String {
    if status == SFSpeechRecognizerAuthorizationStatus::Denied {
        "Speech recognition permission was denied in macOS System Settings.".to_string()
    } else if status == SFSpeechRecognizerAuthorizationStatus::Restricted {
        "Speech recognition is restricted by this Mac or its management profile.".to_string()
    } else {
        "Speech recognition permission was not granted.".to_string()
    }
}

fn await_speech_authorization(cancelled: &AtomicBool) -> Result<(), String> {
    if cancelled.load(Ordering::Acquire) {
        return Err("Native dictation start was cancelled.".to_string());
    }
    let status = unsafe { SFSpeechRecognizer::authorizationStatus() };
    if status == SFSpeechRecognizerAuthorizationStatus::Authorized {
        return Ok(());
    }
    if status != SFSpeechRecognizerAuthorizationStatus::NotDetermined {
        return Err(authorization_error(status));
    }

    let (sender, receiver) = mpsc::sync_channel(1);
    let callback = RcBlock::new(move |next: SFSpeechRecognizerAuthorizationStatus| {
        let _ = sender.send(next);
    });
    unsafe { SFSpeechRecognizer::requestAuthorization(&callback) };
    let deadline = std::time::Instant::now() + PERMISSION_TIMEOUT;
    let next = loop {
        if cancelled.load(Ordering::Acquire) {
            return Err("Native dictation start was cancelled.".to_string());
        }
        let remaining = deadline.saturating_duration_since(std::time::Instant::now());
        if remaining.is_zero() {
            return Err("Timed out waiting for speech recognition permission.".to_string());
        }
        match receiver.recv_timeout(remaining.min(Duration::from_millis(100))) {
            Ok(status) => break status,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                return Err("Speech recognition permission request failed.".to_string())
            }
        }
    };
    if next == SFSpeechRecognizerAuthorizationStatus::Authorized {
        Ok(())
    } else {
        Err(authorization_error(next))
    }
}

fn await_microphone_permission(cancelled: &AtomicBool) -> Result<(), String> {
    if cancelled.load(Ordering::Acquire) {
        return Err("Native dictation start was cancelled.".to_string());
    }
    let audio = unsafe { AVAudioApplication::sharedInstance() };
    let permission = unsafe { audio.recordPermission() };
    if permission == objc2_avf_audio::AVAudioApplicationRecordPermission::Granted {
        return Ok(());
    }
    if permission == objc2_avf_audio::AVAudioApplicationRecordPermission::Denied {
        return Err("Microphone permission was denied in macOS System Settings.".to_string());
    }

    let (sender, receiver) = mpsc::sync_channel(1);
    let callback: RcBlock<dyn Fn(Bool)> = RcBlock::new(move |granted: Bool| {
        let _ = sender.send(granted.as_bool());
    });
    unsafe { AVAudioApplication::requestRecordPermissionWithCompletionHandler(&callback) };
    let deadline = std::time::Instant::now() + PERMISSION_TIMEOUT;
    let granted = loop {
        if cancelled.load(Ordering::Acquire) {
            return Err("Native dictation start was cancelled.".to_string());
        }
        let remaining = deadline.saturating_duration_since(std::time::Instant::now());
        if remaining.is_zero() {
            return Err("Timed out waiting for microphone permission.".to_string());
        }
        match receiver.recv_timeout(remaining.min(Duration::from_millis(100))) {
            Ok(granted) => break granted,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                return Err("Microphone permission request failed.".to_string())
            }
        }
    };
    if granted {
        Ok(())
    } else {
        Err("Microphone permission was denied in macOS System Settings.".to_string())
    }
}

fn start_session(
    app: &AppHandle,
    session_id: String,
    cancelled: &AtomicBool,
) -> Result<NativeDictationSession, String> {
    await_speech_authorization(cancelled)?;
    await_microphone_permission(cancelled)?;
    if cancelled.load(Ordering::Acquire) {
        return Err("Native dictation start was cancelled.".to_string());
    }

    let recognizer = unsafe { SFSpeechRecognizer::new() };
    if !unsafe { recognizer.isAvailable() } {
        return Err("Apple speech recognition is currently unavailable.".to_string());
    }
    if !unsafe { recognizer.supportsOnDeviceRecognition() } {
        return Err(
            "On-device speech recognition is unavailable for the current macOS locale. Audio was not uploaded."
                .to_string(),
        );
    }

    let request = unsafe { SFSpeechAudioBufferRecognitionRequest::new() };
    unsafe {
        request.setShouldReportPartialResults(true);
        request.setRequiresOnDeviceRecognition(true);
        request.setAddsPunctuation(true);
    }

    let result_app = app.clone();
    let result_session_id = session_id.clone();
    let completion = Arc::new((Mutex::new(false), Condvar::new()));
    let completion_for_handler = Arc::clone(&completion);
    let recognition_handler = RcBlock::new(
        move |result: *mut SFSpeechRecognitionResult, error: *mut NSError| {
            if !error.is_null() {
                let (lock, signal) = &*completion_for_handler;
                if let Ok(mut complete) = lock.lock() {
                    *complete = true;
                    signal.notify_all();
                }
                emit_state(
                    &result_app,
                    &result_session_id,
                    "failed",
                    Some("On-device speech recognition failed.".to_string()),
                );
                return;
            }
            if result.is_null() {
                return;
            }
            let result = unsafe { &*result };
            let final_result = unsafe { result.isFinal() };
            let text = unsafe { result.bestTranscription().formattedString().to_string() };
            if !text.trim().is_empty() {
                emit(
                    &result_app,
                    NativeDictationEvent {
                        session_id: result_session_id.clone(),
                        state: if final_result { "final" } else { "partial" }.to_string(),
                        text: Some(text),
                        detail: None,
                    },
                );
            }
            if final_result {
                let (lock, signal) = &*completion_for_handler;
                if let Ok(mut complete) = lock.lock() {
                    *complete = true;
                    signal.notify_all();
                }
            }
        },
    );
    let task = unsafe {
        recognizer.recognitionTaskWithRequest_resultHandler(&request, &recognition_handler)
    };

    if cancelled.load(Ordering::Acquire) {
        unsafe { task.cancel() };
        return Err("Native dictation start was cancelled.".to_string());
    }

    let engine = unsafe { AVAudioEngine::new() };
    let input = unsafe { engine.inputNode() };
    let request_for_tap = request.clone();
    let tap_block = RcBlock::new(
        move |buffer: NonNull<AVAudioPCMBuffer>, _when: NonNull<AVAudioTime>| {
            unsafe { request_for_tap.appendAudioPCMBuffer(buffer.as_ref()) };
        },
    );
    unsafe {
        input.installTapOnBus_bufferSize_format_block(0, 1024, None, RcBlock::as_ptr(&tap_block));
        engine.prepare();
        if let Err(_error) = engine.startAndReturnError() {
            input.removeTapOnBus(0);
            task.cancel();
            return Err("The Mac microphone engine could not be started.".to_string());
        }
    }
    if cancelled.load(Ordering::Acquire) {
        unsafe {
            input.removeTapOnBus(0);
            engine.stop();
            request.endAudio();
            task.cancel();
        }
        return Err("Native dictation start was cancelled.".to_string());
    }
    Ok(NativeDictationSession {
        id: session_id,
        engine,
        input,
        request,
        task,
        completion,
        _recognition_handler: recognition_handler,
        _tap_block: tap_block,
    })
}

pub fn status(state: State<'_, NativeDictationState>) -> NativeDictationStatus {
    let running = state
        .session
        .lock()
        .map(|session| session.is_some())
        .unwrap_or(false);

    let speech_authorization = unsafe { SFSpeechRecognizer::authorizationStatus() };
    if speech_authorization == SFSpeechRecognizerAuthorizationStatus::Denied {
        return NativeDictationStatus {
            available: false,
            running,
            detail: authorization_error(speech_authorization),
        };
    }
    if speech_authorization == SFSpeechRecognizerAuthorizationStatus::Restricted {
        return NativeDictationStatus {
            available: false,
            running,
            detail: authorization_error(speech_authorization),
        };
    }

    let audio = unsafe { AVAudioApplication::sharedInstance() };
    if unsafe { audio.recordPermission() }
        == objc2_avf_audio::AVAudioApplicationRecordPermission::Denied
    {
        return NativeDictationStatus {
            available: false,
            running,
            detail: "Microphone permission was denied in macOS System Settings.".to_string(),
        };
    }

    let recognizer = unsafe { SFSpeechRecognizer::new() };
    if !unsafe { recognizer.isAvailable() } {
        return NativeDictationStatus {
            available: false,
            running,
            detail: "Apple speech recognition is currently unavailable.".to_string(),
        };
    }
    if !unsafe { recognizer.supportsOnDeviceRecognition() } {
        return NativeDictationStatus {
            available: false,
            running,
            detail: "On-device speech recognition is unavailable for the current macOS locale."
                .to_string(),
        };
    }

    let detail = if speech_authorization == SFSpeechRecognizerAuthorizationStatus::NotDetermined
        || unsafe { audio.recordPermission() }
            == objc2_avf_audio::AVAudioApplicationRecordPermission::Undetermined
    {
        "On-device dictation is ready; macOS will ask for permission when you start recording."
    } else {
        "macOS on-device dictation"
    };
    NativeDictationStatus {
        available: true,
        running,
        detail: detail.to_string(),
    }
}

pub fn start(
    app: AppHandle,
    state: State<'_, NativeDictationState>,
    session_id: String,
) -> Result<NativeDictationStatus, String> {
    let cancelled = Arc::new(AtomicBool::new(false));
    let mut guard = state
        .session
        .lock()
        .map_err(|_| "Native dictation state is unavailable.".to_string())?;
    if guard.is_some() {
        return Err("A native dictation session is already running.".to_string());
    }
    *guard = Some(NativeDictationLifecycle::Starting {
        id: session_id.clone(),
        cancelled: Arc::clone(&cancelled),
    });
    drop(guard);

    let session = match start_session(&app, session_id.clone(), &cancelled) {
        Ok(session) => session,
        Err(error) => {
            if let Ok(mut guard) = state.session.lock() {
                if matches!(
                    guard.as_ref(),
                    Some(NativeDictationLifecycle::Starting { id, .. }) if id == &session_id
                ) {
                    guard.take();
                }
            }
            return Err(error);
        }
    };

    let mut guard = state
        .session
        .lock()
        .map_err(|_| "Native dictation state is unavailable.".to_string())?;
    let accepted = matches!(
        guard.as_ref(),
        Some(NativeDictationLifecycle::Starting {
            id,
            cancelled: pending,
        }) if id == &session_id && !pending.load(Ordering::Acquire)
    );
    if !accepted {
        drop(guard);
        unsafe {
            session.input.removeTapOnBus(0);
            session.engine.stop();
            session.request.endAudio();
            session.task.cancel();
        }
        return Err("Native dictation start was cancelled.".to_string());
    }
    *guard = Some(NativeDictationLifecycle::Running(session));
    emit_state(&app, &session_id, "started", None);
    Ok(NativeDictationStatus {
        available: true,
        running: true,
        detail: "macOS on-device dictation".to_string(),
    })
}

fn stop_inner(
    app: &AppHandle,
    state: &State<'_, NativeDictationState>,
    session_id: &str,
    cancel: bool,
) -> Result<(), String> {
    let mut guard = state
        .session
        .lock()
        .map_err(|_| "Native dictation state is unavailable.".to_string())?;
    let Some(lifecycle) = guard.as_ref() else {
        return Ok(());
    };
    if let NativeDictationLifecycle::Starting { id, cancelled } = lifecycle {
        if id != session_id {
            return Err("Native dictation session does not match the active composer.".to_string());
        }
        cancelled.store(true, Ordering::Release);
        guard.take();
        drop(guard);
        emit_state(app, session_id, "cancelled", None);
        return Ok(());
    }
    let NativeDictationLifecycle::Running(session) = guard.take().expect("lifecycle exists") else {
        unreachable!();
    };
    if session.id != session_id {
        *guard = Some(NativeDictationLifecycle::Running(session));
        return Err("Native dictation session does not match the active composer.".to_string());
    }
    drop(guard);

    unsafe {
        session.input.removeTapOnBus(0);
        session.engine.stop();
        session.request.endAudio();
        if cancel {
            session.task.cancel();
        } else {
            session.task.finish();
        }
    }
    if !cancel {
        let (lock, signal) = &*session.completion;
        if let Ok(complete) = lock.lock() {
            let _ = signal.wait_timeout_while(complete, STOP_GRACE, |done| !*done);
        }
    }
    emit_state(
        app,
        session_id,
        if cancel { "cancelled" } else { "stopped" },
        None,
    );
    Ok(())
}

pub fn stop(
    app: AppHandle,
    state: State<'_, NativeDictationState>,
    session_id: String,
) -> Result<(), String> {
    stop_inner(&app, &state, &session_id, false)
}

pub fn cancel(
    app: AppHandle,
    state: State<'_, NativeDictationState>,
    session_id: String,
) -> Result<(), String> {
    stop_inner(&app, &state, &session_id, true)
}
