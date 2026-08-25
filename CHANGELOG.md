# Changelog

All notable OpenAgentFleet changes are recorded here. Each version uses
`Added` / `Changed` / `Fixed` (or `Not in this tag`) as bullet lists. Do not
ship paragraph dumps.

## Unreleased

## 0.3.1-alpha - 2026-08-25

Mini alpha. Unsigned Linux `.deb` / `.rpm` / AppImage and unsigned Windows
NSIS. Mac download stays the notarized
[`v0.3.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
DMG until a Developer ID `0.3.1` DMG is notarized.

### Added

- Computer View Start when the isolated computer is stopped.
- Appearance: Paper / Ink / Forest / Dusk accents, roomy density, 85–135% text size, soft or sharp corners, and a reduce-motion toggle.
- Agent Computer CPU, RAM, disk, swap, OS image, and presets are visible in Settings instead of a collapsed Advanced block.

### Changed

- Android companion: Chat / Computer / Routines / Settings screens, system light/dark, pull-to-refresh chat, reconnect banner.
- Pairing still scans the QR first; JSON paste is secondary.

### Fixed

- Claude Code `--print --output-format stream-json` now passes `--verbose`, which the CLI requires. Headless Claude runs no longer exit 1 on that flag pair.
- Onboarding footer stays above the Windows taskbar on 1280x800 with a ~48px taskbar.
- Windows and Linux voice copy no longer claims on-device Mac dictation.
- Docker daemon and image-build errors keep the full output, including German wincred `Anmeldesitzung` failures. Windows public pulls skip wincred via an empty `DOCKER_CONFIG`.

### Not in this tag

- Proven Agent Computer session on Windows Docker Desktop WSL2
- `ubuntu:24.04` Hub pull without login on the live Windows box
- Authenticode / Microsoft Store
- Agent runtime foundation (EngineAdapter / Tool Registry / ComputerBackend)

## 0.3.0-alpha - 2026-08-21

Public alpha. Unsigned Linux `.deb` / `.rpm` / AppImage and unsigned Windows
NSIS. Mac download stays
[`v0.2.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha)
until a Developer ID DMG is notarized.

### Added

- Roster presence: idle, working, using computer, needs approval, needs
  takeover, collaborating, failed
- Pin, unread, and hide on the Agent list
- Desktop notifications on finish, fail, or approval (Linux `notify-send`;
  macOS Notification Center)
- Review panel: pending approvals, then each Agent’s last finished run
  (completed, failed, blocked, stopped)
- Per-Agent engine override in Agent Builder; missing engine fails closed
- Group chat: one thread, mention-gated work, no leak into private chats
- Opt-in Agent-to-Agent tools with allowlist, depth cap, ping-pong reject,
  concurrent-peer limit
- Routines: create disabled, claim once, 15-minute lease, then advance
  next-run
- Routine test-run (real work, does not consume next-run)
- Signed loopback webhook on `127.0.0.1:4319`; secret hashed at rest, shown
  once; body discarded
- Mobile pairing QR (same JSON as copy); controller can approve, stop, and
  pause/enable routines; observer is read-only
- Windows host: botd and Go tests; default Computer runtime is Docker Desktop
- Fleet Host `GET /api/host/status` as `authority`; pairing `desktop` /
  `ios` / `android`

### Changed

- Heartbeats stay off until Routines, Heartbeat, and opt-in are all on
- Enable/Resolve ignore a past next-run instead of firing immediately
- Deny on a routine skips that occurrence
- Always-approval still waits for Allow
- Tailscale Serve targets `:4318`, never `:4317`

### Not in this tag

- Signed or notarized Mac `0.3.0` DMG
- Authenticode / Microsoft Store
- Intel Macs
- Computer View proven on Windows Docker Desktop WSL2
- Funnel, push, or cloud relay
- Production stability

## 0.2.0-alpha

Signed, notarized Apple Silicon DMG. Unsigned Linux packages.
[Release](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha).

### Added

- Agent Computer AX-first inspect (element/window refs, pixel fallback)
- Human-only named checkpoints (guest image + Chromium profile)
- Teach a Task review of the redacted trajectory after Stop; Skill Workshop
  drafts stay `auto_enabled: false`
- Visible one-Agent mention handoff from chat (not worker delegation)
- Saved approval rules: Allow once still prompts; Always allow / Always deny
  persist principal + resource + operation
- Pi as optional lead: `pi --mode rpc --no-session`, exact `--tools`
  allowlist, auth via `pi /login` / `~/.pi`
- Pi as optional worker: `read_only` or `workspace` only, no `bash`, no MCP,
  no Agent Computer
- Donsetch search connector (`npx donsetch@2.1.0 mcp`), off by default
- Linux `.deb` / `.rpm` / AppImage; Docker Engine recommended, not started
  until Computer View needs it
- Computer CPU / memory / disk / swap limits with host-capacity guards

### Changed

- Default engine remains Grok Build (`grok-4.6`); Pi never substitutes
- Pi `ask` confirms through the bundled extension + RPC `extension_ui`
- Pi `default` / `plan` map to lead permission `workspace`; `auto` is rejected
- Enabling Hound, Web Search Plus, Donsetch, or Computer MCP on a Pi lead
  errors instead of dropping the connector
- First-run Agent Computer flow and Colima/Docker lifecycle, frames, keyboard
- macOS release checks for signing, notarization, and DMG hashes

## 0.1.0-alpha (public prerelease)

The first public Apple Silicon macOS alpha. The signed and notarized DMG and
matching SHA-256 checksum are published on the
[GitHub release page](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.1.0-alpha).
This release is not feature-complete or production-ready.
