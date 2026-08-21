# OpenAgentFleet Mobile

An Expo React Native companion for the macOS-hosted OpenAgentFleet daemon. It
connects privately over Tailscale HTTPS; iOS and Android are clients, never
Docker, VM, browser, or harness hosts.

## Remote alpha V1

The app only calls the mobile V1 API:

- `GET /api/v1/meta` before pairing.
- `POST /api/v1/pair` once with an exact, short-lived pairing bundle.
- Authenticated bootstrap, conversations, messages, pending approvals, run
  stop, routines list/pause/enable, computer status/frame, V1 SSE, and
  optional session logout routes.

There is no manual Tailnet URL or global-token field. Scan the pairing QR
from the desktop app (camera permission required). If the camera is denied,
paste exactly this JSON bundle:

```json
{
  "version": 1,
  "base_url": "https://your-mac.tailnet.ts.net",
  "host_id": "host_example",
  "grant_id": "grant_example",
  "pairing_secret": "one-time-secret",
  "expires_at": "2026-08-12T12:00:00Z"
}
```

The parser rejects malformed, expired, non-HTTPS, non-`*.ts.net`, URL-wrapped,
or extended bundles. The one-time secret is sent only in the pairing request
and is never persisted. The companion follows the system light/dark scheme.
On Android the composer stays above the keyboard, the tab bar hides while
typing, pull-to-refresh reloads the chat snapshot, and a banner shows when
the live connection is reconnecting.
The pairing response is accepted only when its V1 version, Bearer token type,
unexpired credential, and host identity match the bundle.

After pairing, the device-specific Bearer credential and its non-secret public
profile are stored only through `expo-secure-store` (iOS Keychain / Android
Keystore abstraction). Local **Disconnect this device** immediately removes
both secure-store entries; it also best-effort calls the optional server logout
route without relying on it for local removal.

Controller and owner devices can Allow/Deny pending approvals, stop an
active run, and pause or enable routines. Observer devices can list those
states. Secret handoff, typing on the Agent Computer, and preferences stay
in the desktop app.

## Computer boundary

The Agent Computer is an authenticated browser frame. Controller/owner
devices may request a short server-checked click lease for that frame. The
mobile API rejects typing, key presses, navigation, and secret bytes; the
desktop frame/protocol remains Mac-local. Password, 2FA, CAPTCHA, payment,
and all keyboard input stay in the trusted macOS app.

## Events

The client listens only for the named V1 SSE events:

- `ofb.event` with a numeric durable cursor.
- `ofb.reset`, which clears the cursor and safely reloads the snapshot before
  creating a new stream.

SSE closes while the app is backgrounded and reconnects with bounded
exponential backoff when active again.

## Development and verification

```sh
pnpm install
pnpm typecheck
pnpm exec expo export --platform all --output-dir /tmp/openagentfleet-mobile
```

Run a native development session with `pnpm ios` or `pnpm android`. The Mac
must expose only its dedicated mobile V1 router through private Tailscale
Serve; do not publish the API with Funnel or a public reverse proxy.

For a standalone Android artifact, use the release variant on a host with the
Android SDK configured:

```sh
cd android
./gradlew :app:assembleRelease
```

The debug variant intentionally expects Metro and is not the downloadable
phone build. Release signing currently uses the development keystore; a
distribution keystore is required before publishing outside a private alpha.
