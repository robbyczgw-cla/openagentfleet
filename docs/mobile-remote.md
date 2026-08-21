# Mobile remote protocol

## Status

The Expo client in [`mobile/`](../mobile/) is a working remote-alpha UI and
transport: it validates a short-lived pairing bundle, exchanges it once for a
device-specific bearer credential, stores that credential through the platform
secure-store abstraction, bootstraps a conversation, reconnects SSE, and
renders an authenticated Agent Computer frame and, for controller/owner
devices, can request a short server-side click-control lease.

The transport and pairing lifecycle are implemented as an alpha contract.
Device-specific release validation remains environment-dependent; this
document does not claim that every Android or iOS artifact has passed release
QA.

The current alpha remains deliberately limited: the shipped mobile lease is a
30-second device-bound lease for browser-frame click actions. It does not yet
carry the full frame/epoch/action-id contract below. Password/OTP handoff,
keyboard input, and desktop control remain on the trusted Mac app. Controller
and owner devices can resolve approvals, stop an active run, and pause or
enable routines.

## Boundary

```text
OpenAgentFleet desktop app / local administration
             |
             | local API :4317 (never Tailscale Serve)
             v
           botd
             |
             | dedicated mobile API :4318, loopback only
             v
       Tailscale Serve HTTPS :443
             |
             v
  paired iOS / Android OpenAgentFleet companion
```

`botd` stays bound to loopback. Tailscale Serve is the private HTTPS transport,
not a substitute for application authentication. The Serve target must become
the dedicated mobile listener (`4318`) before a mobile release; it must never
be a public Funnel endpoint.

The existing local API remains deliberately broader for the trusted macOS app.
The mobile listener is an allowlist, not a filtered copy of every `/api/**`
route.

## Device pairing: target contract

1. The Mac creates a one-time pairing grant after an explicit local user
   action. The grant is 32 random bytes, valid for at most five minutes, and
   only ever travels in the QR payload or direct local handoff. It is never
   written to logs, SQLite, event history, or a URL query string.
2. The phone scans the QR payload and checks that `base_url` is a Tailnet HTTPS
   hostname, never loopback, a raw IP address, or a public URL.
3. The phone proves a per-installation P-256 device key. Production builds use
   Secure Enclave / Android Keystore backed signing through a small native
   module; Expo Secure Store alone remains useful for opaque refresh material
   but is not a hardware signing primitive.
4. The Mac shows a short authentication string (SAS), device name, platform,
   requested profile, and the authenticated Tailscale identity if Serve
   identity headers are available. The user approves or rejects locally.
5. The daemon issues a short-lived DPoP-bound access token plus a rotating
   refresh token. The database stores hashes only. Revoking a device invalidates
   all its access/refresh material, active event streams, and computer leases.

The pairing design follows standard DPoP sender-constrained-token behavior;
the full proof details are recorded during implementation rather than
approximated with a client-set header.

## Scope profiles

| Profile | Permitted mobile capabilities |
| --- | --- |
| `observer` | State, conversations, event stream, Agent Computer frames, pending approvals, routines list |
| `controller` | Observer + chat, approvals, run stop, routine pause/enable, scoped computer-control lease |
| `owner` | Controller + explicit Agent Computer start/delegation controls |

Remote alpha V1 implements `observer` (snapshot, events, Computer View,
approvals, and routines as read-only) and `controller` (the same plus chat, a
short click-control lease, approval resolve, run stop, and routine
pause/enable); `owner` shares the controller boundary. Attachments,
transcription, keyboard input, typing, and secure secret handoff remain
Mac-local.

The following remain Mac-local in the first remote release regardless of
profile: preferences, MCP/plugin administration, harness OAuth, native Grok
launch/session management, Teach a Task administration, Skill Workshop
enablement/rollback, raw workspace access, Docker/CDP/VNC access, and all
secret-handoff transports.

## Mobile API allowlist

The dedicated listener will expose only versioned `/api/v1/**` routes:

| Route family | Required scope | Notes |
| --- | --- | --- |
| `GET /api/v1/meta`, `GET /api/v1/bootstrap` | `state:read` | Mobile-safe DTO only; no workdirs, command paths, container IDs, or native sessions. Bootstrap includes pending approvals and conversation runs. |
| Conversations and message submission | `state:read`, `chat:write` | Mutations require idempotency keys |
| Attachments and transcription | `attachments:*`, `stt:use` | Not implemented on the mobile listener |
| Events | `events:read` | Durable cursor, reconnect, stream close on revocation |
| `GET /api/v1/approvals`, `POST /api/v1/approvals/{id}` | `state:read`, `approvals:*` | Pending approvals only. Resolve body is `{status, option_id}`. No `persist` / always-allow. Mutations require Idempotency-Key. |
| `POST /api/v1/runs/{id}/stop` | `chat:write` | 409 if the run is not active. Requires Idempotency-Key. |
| `GET /api/v1/routines`, `POST /api/v1/routines/{id}/pause`, `POST /api/v1/routines/{id}/enable` | `state:read`, `chat:write` | Mobile-safe routine DTO. Enable ignores a past next-run timestamp. Mutations require Idempotency-Key. |
| Computer status and frames | `computer:view` | No raw desktop protocol |
| Computer takeover/action | `computer:control` | Device-bound 30-second browser click lease; typing, navigation, desktop actions, and frame/epoch/action IDs remain out of scope |

No mobile endpoint may invoke arbitrary shell commands, expose a Docker socket,
expose Chrome DevTools, or accept password/OTP bytes.

## Control leases

Remote control is not a boolean. A future mobile control action must contain a
server-issued `lease_id`, `control_epoch`, `frame_id`, and unique `action_id`.
Leases expire quickly (target: 30 seconds) and are bound to exactly one device.
Changing owner, revoking a device, restarting the Agent Computer, or expiring a
lease increments the epoch and makes stale actions fail closed. Agent control
and human control are structurally exclusive.

The current mobile client exposes the read-only frame to every paired device.
Controller/owner devices additionally get a server-checked, short-lived click
lease. The Mac-local secure handoff remains the only supported path for
passwords and one-time codes.

## Sync contract

The mobile bootstrap response records an event high-water mark in the same
database transaction as its snapshot. The subsequent SSE connection resumes
after that cursor. An unknown or expired cursor returns a clear reset response;
the client reloads bootstrap rather than receiving an unbounded history.

Events use one stable SSE event name and a structured JSON envelope. Durable
run state and final messages are replayable; token/thought streaming is not a
durability promise.

## Release sequence

1. Add the separate loopback mobile listener and make Tailscale Serve target
   only that listener.
2. Add stable host identity, paired-device persistence, per-device credential
   hashes, revocation, and audit metadata.
3. Add locally approved QR pairing and DPoP/device-key proof.
4. Replace manual token entry in the Expo client with QR pairing and multiple
   environment profiles.
5. Add mobile-safe DTOs and cursor-safe SSE.
6. Add action hashes, idempotency, rate limits, and device-aware approval logs.
7. Add frame IDs, control epochs, action IDs and rate limits; then consider
   expanding the existing click lease to keyboard input.
8. Test two devices, revoked devices with live SSE, network changes,
   sleep/wake, stale cursor, lost phones, and accidental Funnel/Serve
   misconfiguration.

## Related material

- [Remote Mac architecture](remote-mac-architecture.md)
- [Mobile setup](../mobile/README.md)
- [Tailscale Serve](https://tailscale.com/docs/features/tailscale-serve)
- [RFC 9449: DPoP](https://www.rfc-editor.org/rfc/rfc9449)
- [Third-party notices](../NOTICE.md)
