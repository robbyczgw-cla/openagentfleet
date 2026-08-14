# Remote Mac architecture

The Mac is the host. iOS and Android are remote control clients.

## Runtime topology

```text
iPhone / Android
       |
       | private tailnet HTTPS
       v
Tailscale Serve on the Mac
       |  localhost only
       v
botd :4317
       |
       +-- SQLite / event log
       +-- Docker or VM Agent Computer
       +-- Pi / Claude / Codex / Grok Build
       +-- browser preview / takeover
```

The mobile apps must not try to run Docker, CLI coding agents, browser sessions, or the orchestration loop locally. They send commands and receive the durable state of the Mac-hosted runtime.

## Network boundary

`botd` should continue listening on `127.0.0.1`, even in remote mode. The
local Mac application uses its administrative listener on `4317`. The remote
mobile listener is intentionally separate on `4318`, also loopback-only;
Tailscale Serve must proxy only the latter:

```sh
tailscale serve --bg --https=443 http://127.0.0.1:4318
tailscale serve status --json
```

Serve provisions HTTPS for the tailnet DNS name and keeps the backend on
localhost. Do not bind `botd` to `0.0.0.0` just to make the phone connect.
The setup helper defaults to this remote listener, first inspects existing
Serve state, refuses Funnel, and will not replace an unrelated Serve
configuration without an explicit opt-in. Until the separate listener is
present in a build, do not point Serve at `4317` as a workaround: that is the
Mac-local administrative API.

The legacy local daemon also accepts an optional daemon-wide bearer token. It
is not the mobile authentication design and must not be pasted into a released
mobile client. The production mobile pairing flow creates a short-lived
one-time grant and exchanges it for a per-device, revocable credential stored
in the platform secure store, never in a URL or checked-in configuration. The
alpha contract is in [`mobile-remote.md`](mobile-remote.md).

```sh
OPENAGENTFLEET_REMOTE_TOKEN='generated-high-entropy-token' \
OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION=1 \
go run ./cmd/botd
```

The mobile client will send `Authorization: Bearer ...`. Tailscale grants/ACLs remain the outer device and user access policy; the application token protects the Bot API if another authorized tailnet device can reach the endpoint.

## What works remotely

- Conversation messages and Bot selection.
- Run status, tool summaries, handoffs, artifacts, and event history.
- Approval and denial of pending actions.
- Computer screenshot stream.
- Human takeover of the Agent Computer.
- Routine enable/disable and manual run.
- Notifications while the mobile app is open.
- Push notifications through an optional APNs/FCM relay when the app is suspended.
- Tailscale is transport and access control; it does not itself wake a sleeping Mac or deliver mobile push notifications.

## Mac lifecycle requirements

For this to behave like a background teammate:

- Install `botd` as a macOS LaunchAgent.
- Keep Tailscale running and configure Serve in background mode.
- Start the Agent Computer on demand or at login, according to the user's setting.
- Keep the Mac awake while active runs exist only when the user explicitly enables that policy.
- Persist all runs/events before sending them to a mobile client.
- Reconnect mobile clients from the last event cursor after network loss.
- Use SSE or WebSocket for live events; polling is only the initial client fallback.

The Mac must be awake and connected for local execution. A future cloud-hosted Agent Computer can preserve work when the Mac is off, but that is a separate deployment mode, not something Tailscale solves.

## Security rules

- Never expose the Docker socket to the mobile client.
- Never let a mobile request invoke an arbitrary shell command.
- Mobile approval is a signed, single-action decision bound to a run and action hash.
- Browser takeover must be a separate authenticated control channel.
- Password and OTP entry is Mac-native only for a focused browser field: the
  browser-accessible API never accepts secret bytes. The macOS secure field
  hands values to the local daemon over an owner-only Unix socket, and delivery
  is bound to the captured computer, document, and concrete input element. A
  changed target fails closed and requires re-entry.
- Do not treat the secure handoff as containment from a malicious same-user
  process, or as proof that secret bytes never transiently exist in memory.
- CAPTCHA, payment, and desktop credential entry are not supported by this
  path.
- Do not expose raw VNC/noVNC; use the authenticated frame/action surfaces.
- Store remote tokens in macOS Keychain / iOS Keychain / Android Keystore.
- Redact credentials and secrets from event history and screenshots.
- Keep Tailscale Serve private; never use Funnel for the Bot API by default.

## Sources

- [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve)
- [Tailscale Serve CLI](https://tailscale.com/docs/reference/tailscale-cli/serve)
- [Tailscale Grants](https://tailscale.com/docs/features/access-control/grants)
