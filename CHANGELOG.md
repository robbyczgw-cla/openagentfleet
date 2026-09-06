# Changelog

All notable OpenAgentFleet changes are recorded here. Each version uses
`Added` / `Changed` / `Fixed` (or `Not in this tag`) as bullet lists. Do not
ship paragraph dumps.

## Unreleased

### Added

- Engine adapters wrap the existing provider runners and emit normalized `agent.*` events.
- Tool registry with canonical names and existing MCP aliases.
- Computer backend interface wrapping the existing Docker runtime, with native execution available for tests.
- Per-Agent queue shared by chat, group, delegation, and routine turns.
- Coordinator delegation events on the existing handoff records.
- README 19-second first-run storyboard with voiceover, plus roster / gated-routine / Routines screenshots.
- Tasks & results panel: every Agent's runs in one filtered list, each with its brief, saved answer, and snapshotted output files.
- Routines workspace: simple schedule picker with server-computed next runs, agenda and week calendar, safe edits on paused routines, and Markdown workflow templates.
- Connected apps: read-only GitHub issues and pull requests for selected repositories and Agents, using the `gh` login on this computer.
- [Workspace productivity](docs/workspace-productivity.md) guide for the workspace panels and their limits, including how the bridge is scoped for chat runs and routines.
- [Linux desktop](docs/linux-desktop.md) docker group troubleshooting, the Ubuntu 26.04 XFCE native check of the panels and the Agent Computer, and a debug-build check of projects, task attempts, memory proposals and GitHub task start with stand-in `gh` and engine (no real GitHub, no review or publish).
- Task retry and revise: `POST /api/tasks/{id}/retry` on a failed or stopped task, `POST /api/tasks/{id}/revise` on a completed one. Same Agent only, at most 10 linked attempts, `Idempotency-Key` required, original input files copied on request, new provider session. The earlier task keeps its result and files. Task detail now returns the original `input`.
- Projects: named brief shared with chosen Agents, append-only brief versions, optimistic `expected_version` on edit and archive, explicit assignment to one conversation or routine. Each run snapshots the brief version at queue time; membership and archive state are re-checked before the engine starts and revoke the run as Blocked with `project_access_revoked`. Provider sessions are bound to conversation, project, brief version and working directory, so a changed brief starts a fresh session.
- Memory proposals behind the existing `memory_proposals` feature flag: a `propose_memory` bridge tool (not offered to Pi leads), pending review in the Review dialog with edit, accept and reject, provenance to the source run and message, 5 per run and 25 pending per Agent. Accepting writes one approved memory with source `agent_proposal`; deleting that memory later does not let a second accept recreate it.
- GitHub tasks: hand a granted issue to a granted Agent in a `git worktree` next to the local checkout on branch `oaf/issue-<n>-<id>`. Review requires a completed task, a clean committed branch on the recorded base, and an explicit argv test command (no shell, 10 minute and 2 MiB limits). Publish re-verifies the review digest, pinned login, grant and tree, then pushes the branch and opens a draft pull request. Nothing writes to GitHub before that button.

### Changed

- The collaboration bridge no longer receives the controller bearer. Each run's bridge token is its only credential, accepted solely on the collaboration, GitHub read and memory-proposal routes its scope covers, and refused after the run ends.
- Runs execute in a per-run working directory when one is bound (GitHub task worktrees, retries of such tasks); otherwise the shared workspace as before. Task deliverables and artifact capture follow that directory.
- The projects, memory-proposal and GitHub task routes require a configured controller token and answer 503 without one. The native app already sets that token at startup.

### Fixed

- Project snapshots no longer fail with a SQLite lock error when another Agent turn writes at the same time. Snapshot transactions reserve the writer before reading the project, and unassigned routine runs retain their origin for later retries.

- Desktop startup no longer times out while `botd` is already healthy. The native shell's health check read only the first 512 bytes of the `/health` reply, so once response headers grew past that the JSON body never arrived and the app reported a startup timeout. It now reads the whole reply up to 8192 bytes, and a regression test covers a fragmented reply with large headers. Found during the native Linux test of this branch.
- Packaged desktop apps bundle `collaboration-mcp` and hand its path to `botd`, so GitHub and collaboration tools work outside a source checkout. The Linux, macOS and Windows release verification scripts fail when it is missing.
- Computer and collaboration capability leases start when a turn executes, so queue waits do not consume their lifetime. Routine turns now bind their configured Computer MCP capability at execution too.
- Canceling a running routine waits for executor cleanup before completing its occurrence. Canceling a queued routine preserves the user's stop reason.
- Delegations emit their started event after the target run starts, rather than when it joins the queue.
- Queued Agents explain that tasks run one at a time and do not show another turn's active computer as their own work.

## 0.3.1-alpha - 2026-08-25

Mini alpha. Notarized Apple Silicon DMG and unsigned Linux packages are on
GitHub prerelease
[`v0.3.1-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.1-alpha).
Windows NSIS is not on that tag yet.

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
- Windows `0.3.1` NSIS on the GitHub tag

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
