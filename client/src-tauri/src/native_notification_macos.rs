//! Native macOS desktop notifications via UserNotifications.
//!
//! Linux uses notify-send from lib.rs. This module is the Mac path: request
//! UNUserNotificationCenter authorization, post a local alert, and fall back
//! to osascript if the user denied the prompt or the framework call fails.
//!
//! MAC AGENT VERIFY (cannot be done on the Linux build host):
//! 1. `pnpm run tauri dev` on Apple Silicon.
//! 2. Create two Agents; start a run that needs approval (or trigger
//!    `show_desktop_notification` from the composer by finishing a run in
//!    another Agent while this window is unfocused).
//! 3. macOS must show the system permission dialog on first notify. Allow it.
//! 4. A Notification Center banner titled "OpenAgentFleet" / "Approval needed"
//!    must appear. Clicking it should focus the app window (system default).
//! 5. Settings → Desktop notifications → Enable must call this same
//!    authorization path, not only the WebView Notification API.
//! 6. Deny the permission once and confirm the in-app desktop-alert bar still
//!    works and osascript is used as a best-effort fallback.
//! 7. Signed/notarized DMG: confirm the banner still shows under Gatekeeper.

use std::process::{Command, Stdio};
use std::sync::Once;
use std::time::{SystemTime, UNIX_EPOCH};

use block2::RcBlock;
use objc2::runtime::Bool;
use objc2_foundation::{NSError, NSString};
use objc2_user_notifications::{
    UNAuthorizationOptions, UNMutableNotificationContent, UNNotificationRequest,
    UNNotificationSound, UNUserNotificationCenter,
};
use tauri::AppHandle;

pub fn show(app: &AppHandle, title: String, body: String) -> Result<(), String> {
    let posted = app.run_on_main_thread(move || {
        request_and_post(title, body);
    });
    match posted {
        Ok(()) => Ok(()),
        Err(error) => Err(error.to_string()),
    }
}

pub fn request_permission(app: &AppHandle) -> Result<(), String> {
    show(
        app,
        "OpenAgentFleet".to_string(),
        "Desktop notifications are on for this Mac.".to_string(),
    )
}

fn request_and_post(title: String, body: String) {
    request_authorization_once();
    if post_local(&title, &body).is_err() {
        let _ = osascript_fallback(&title, &body);
    }
}

fn request_authorization_once() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        let center = UNUserNotificationCenter::currentNotificationCenter();
        let options = UNAuthorizationOptions::Alert | UNAuthorizationOptions::Sound;
        let handler = RcBlock::new(move |_granted: Bool, _error: *mut NSError| {});
        // Apple retains the block past this return. One leaked owner covers
        // process lifetime; do not leak a block per notification.
        let leaked: &'static RcBlock<dyn Fn(Bool, *mut NSError)> = Box::leak(Box::new(handler));
        center.requestAuthorizationWithOptions_completionHandler(options, leaked);
    });
}

fn post_local(title: &str, body: &str) -> Result<(), String> {
    let content = UNMutableNotificationContent::new();
    content.setTitle(&NSString::from_str(title));
    content.setBody(&NSString::from_str(body));
    content.setSound(Some(&UNNotificationSound::defaultSound()));
    let identifier = NSString::from_str(&format!(
        "oaf-{}",
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|duration| duration.as_millis())
            .unwrap_or(0)
    ));
    let request = UNNotificationRequest::requestWithIdentifier_content_trigger(
        &identifier,
        content.as_ref(),
        None,
    );
    UNUserNotificationCenter::currentNotificationCenter()
        .addNotificationRequest_withCompletionHandler(&request, None);
    Ok(())
}

fn osascript_fallback(title: &str, body: &str) -> Result<(), String> {
    let script = format!(
        "display notification \"{}\" with title \"{}\"",
        escape_osascript(body),
        escape_osascript(title)
    );
    Command::new("osascript")
        .args(["-e", &script])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map(|_| ())
        .map_err(|error| error.to_string())
}

fn escape_osascript(value: &str) -> String {
    value.replace('\\', "\\\\").replace('"', "\\\"")
}
