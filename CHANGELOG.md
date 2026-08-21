# Changelog

All notable OpenAgentFleet changes are recorded here.

## Unreleased

Nothing yet. Next work lands here.

## 0.3.0-alpha - 2026-08-21

Public alpha from `455ab81` plus Windows host notes and the Git Bash NSIS
packaging fixes on `main`. Linux ships unsigned `.deb` / `.rpm` / `.AppImage`.
Windows ships an unsigned current-user NSIS installer. The notarized Mac
download remains
[`v0.2.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha)
until a Developer ID DMG is built, notarized, and stapled.

This is still a public alpha, not a production stability promise.

### Living teammates

Sidebar Agents show derived presence: idle, working, using computer, needs
approval, needs takeover, collaborating, or failed. Pin, mark unread, or hide
an Agent. Desktop notifications fire when an Agent finishes, fails, or needs
approval (Linux `notify-send`; macOS Notification Center with osascript
fallback). In-app Approve / Deny / Stop / Open Agent stay authoritative.

The Review panel lists pending approvals across Agents, then each Agent’s last
finished run (completed, failed, blocked, or stopped). Optional per-Agent
engine override in Agent Builder; a missing engine fails closed.

### Group chat and collaboration

Create a group, pick members, and talk in one thread. Group messages stay in
that group. Only Agents you mention on a send start work. Agent-to-Agent tools
stay off until collaboration is enabled on that Agent. Enabled Agents get an
allowlist, a depth cap, ping-pong rejection, and a concurrent-peer limit.

### Routines

Inspect an enabled Skill, create a Routine, then enable it. Create always
starts disabled. botd claims due occurrences, runs a visible Agent turn,
renews a 15-minute lease, then advances next-run. Heartbeats stay off until
Routines, Heartbeat, and opt-in are all on.

Always-approval waits for Allow. Deny skips that occurrence. Enable/Resolve
ignore a past next-run instead of firing immediately. Test-run does real work
without consuming next-run. Enabled routines can expose a signed loopback
webhook on `127.0.0.1:4319`; the secret is hashed at rest and shown once. The
request body is discarded, not injected into the prompt.

### Mobile

Pair an iPhone or Android over private Tailscale. Settings shows a QR of the
same bundle as the copyable JSON. Controller/owner devices can list and
resolve pending approvals, stop a run, and pause or enable routines. Observer
devices stay read-only. Keyboard, secret handoff, and push stay Mac-local.

### Fleet Host

`GET /api/host/status` reports the controller as `authority`. Pairing accepts
`desktop`, `ios`, and `android`. Linux can install an always-on loopback
systemd unit. Tailscale Serve targets `:4318`, never `:4317`. Funnel is out
of scope.

### Windows host

botd and the Go suite run on Windows. Guest Agent Computer paths stay POSIX
(`/workspace`, `/tmp`); native harness workdirs may be `C:\...`. NTFS modes
are not fail-closed as POSIX 0600/0700. Connector state replace retries
sharing violations. Default Computer runtime is Docker Desktop. Dictation and
the native secure prompt stay macOS-only. Computer View against Docker
Desktop is not claimed by this release.

### Boundaries

- Group work that needs a live engine stays queued until that engine runs.
- Linux and Windows packages are unsigned. The Mac `v0.3.0-alpha` DMG is not
  in this tag.
- Intel Macs are still unsupported.
- Scheduled Grok runs still need a harness-enabled host.

## 0.2.0-alpha

Public prerelease from `87e648c`. The Apple Silicon DMG is signed, notarized
and stapled. Linux packages are unsigned and may be rebuilt on this same tag
when the Agent Computer and desktop controller move forward. See the
[v0.2.0-alpha release](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha).

### Agent Computer: inspect, checkpoints, teach replay

The isolated computer now observes with an AX-first snapshot (element and
window refs, then pixel fallback). Humans can save and restore named guest
checkpoints (image + Chromium profile); Agents cannot. Teach a Task still
writes a Skill Workshop draft with `auto_enabled: false`, and after Stop the
redacted trajectory is reviewable in the UI without re-executing it.

### Visible Agent handoff and saved approvals

A user can mention one other Agent from chat. That is a visible handoff, not
worker delegation. Saved approval rules are exact: Allow once still prompts
next time; Always allow / Always deny persist one principal + resource +
operation.

### Pi as an optional workspace engine

Pi is now a selectable lead, not a “future worker” placeholder. The default
engine is still Grok Build (`grok-4.6`). Choosing Pi never silently substitutes
Grok, Codex, or OpenCode.

A Pi lead starts `pi --mode rpc --no-session` with an exact `--tools`
allowlist. `--no-session` keeps Agent memory in OpenAgentFleet; Pi does not
write its own chat session. Auth is `pi /login` / `~/.pi` (or Pi’s provider
env keys). OpenAgentFleet does not run OAuth for Pi and does not copy those
keys into SQLite or chat.

Lead `ask` confirms through a bundled Pi extension and RPC `extension_ui`,
not a native OpenAgentFleet permission popup. Workspace usage `default` and
`plan` map to Pi lead permission `workspace`. `auto` is rejected.

Pi still has no MCP injection: no Hound, no Web Search Plus, no Donsetch,
and no Computer MCP. A Pi engine therefore has no Agent Computer. Enabling those connectors
on a Pi lead is an error, not a silent drop.

The model picker lists real Pi `provider/model` IDs from `@mariozechner/pi-ai`
(`xai/grok-4.3`, `anthropic/claude-sonnet-4.6`, `openai/gpt-5.5`,
`deepseek/deepseek-v4-flash`) plus Pi automatic and a custom ID. Those routes
run **through Pi RPC**. They are not the Grok Build harness, not Codex App
Server, and not bundled OpenCode.

### Pi as a bounded worker

Pi can also sit in the optional worker pool via the same RPC path, with
`--tools` limited to `read_only` or `workspace`. Worker Pi has no `bash`, no
bundled extension, no MCP, and no Agent Computer.

### Docs

README and the Agent / lead-worker docs now treat Pi as a first-class
optional engine and state the Computer, MCP, and auth boundaries in the
same place as Grok, Codex, and OpenCode.

- Added Donsetch as a third optional, off-by-default search connector beside
  Web Search Plus and Hound, launched through a pinned `npx donsetch@2.1.0 mcp`
  contract.
- Started Windows desktop research: NSIS + Docker Desktop/WSL2 is the first
  alpha contract; see `docs/windows-desktop.md`.
- Added a native Linux desktop alpha with `.deb`, `.rpm` and `.AppImage`
  packages. Fresh Linux installs default the Agent Computer to Docker Engine.
- Docker is recommended by the Linux packages, not silently started; Computer
  View builds the isolated container on demand.
- Refined the first-run Agent Computer flow and optional runtime settings.
- Added configurable CPU, memory, disk and swap limits with host-capacity guards.
- Improved Colima/Docker lifecycle handling, desktop frames, keyboard input and
  Computer View controls.
- Clarified the local/provider security boundary and OpenCode permission
  limitations in the README and maintained documentation.
- Added release hygiene checks for macOS signing, notarization and DMG hashes.

## 0.1.0-alpha (public prerelease)

The first public Apple Silicon macOS alpha. The signed and notarized DMG and
matching SHA-256 checksum are published on the
[GitHub release page](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.1.0-alpha).
This release is not feature-complete or production-ready.
