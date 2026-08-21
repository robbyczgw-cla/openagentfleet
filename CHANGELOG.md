# Changelog

All notable OpenAgentFleet changes are recorded here.

## Unreleased

macOS `v0.3.0-alpha` is not published until a Developer ID DMG is notarized
and stapled from this version. Linux `.deb` / `.rpm` / `.AppImage` can be
built from the same commit. The current public download remains
[`v0.2.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha).

### Living teammates

- Sidebar Agents now show live presence: idle, working, using computer, needs
  approval, needs takeover, collaborating, or failed.
- Optional per-Agent engine override in Agent Builder. Workspace default still
  applies unless you opt in; a missing engine fails closed.
- Desktop notifications fire when an Agent finishes, fails, or needs
  approval, with in-app Approve / Deny / Stop / Open Agent actions.
  Linux uses notify-send. macOS uses Notification Center
  (UNUserNotificationCenter, osascript fallback).
- Sidebar Agents can be pinned, marked unread, or hidden.
- Mobile pairing in Settings shows a QR code of the same Tailnet bundle
  JSON as the copyable text.

### Mobile control surface

- Paired controller/owner phones can list and resolve pending approvals, stop
  an active run, and pause or enable routines. Observer devices stay
  read-only. Bootstrap includes pending approvals so the phone sees gates
  without a second round trip.

### Routines scheduler

- botd claims due Routines, starts a visible Agent turn, then advances the
  next run. The same occurrence is claimed once. Heartbeats stay off until
  Routines and Heartbeat are enabled and the schedule is opted in.
- Always-approval occurrences wait for an in-app Approve before the claim.
  Deny skips that occurrence and schedules the next one. Enable/Resolve
  ignore a past next-run time instead of firing immediately.
- Leases last 15 minutes and renew while the Agent turn is running, so a
  long job is not marked unknown mid-run.
- The workspace Routines panel lists next run, pause/enable, and history.
- Routines can be test-run without enabling or consuming the next scheduled
  occurrence.

## 0.3.0-alpha

Coworkers first: Agents can share a group chat, opt into agent-to-agent work,
turn an enabled Skill into a Routine, and run an always-on Fleet Host. This
is still a public alpha, not a production stability promise.

### Group chat

Create a group, pick members, and talk in one thread from the sidebar. Group
messages stay in that group; they never land in an Agent’s private chat.
Only Agents you mention on a send start work. Mentioning a teammate from a
group does **not** require collaboration to be enabled — that gate is for
Agents tasking each other.

### Opt-in Agent collaboration

A user `@mention` is still a visible handoff. Agent-to-Agent tools
(message, delegate, cancel) stay off until collaboration is enabled on that
Agent. Enabled Agents get an allowlist, a depth cap, ping-pong rejection, and
a limit on concurrent peer tasks. Collaboration is not a hidden worker pool
and is not on by default.

### Teach → Skill → Routine

Inspect an enabled Skill, create a Routine from it, and enable the Routine
explicitly. Create always starts disabled. A Skill that is not enabled cannot
become a Routine. Heartbeat Routines still require the heartbeat opt-in.
Skills never auto-enable. This ships the inspect/create/enable contract. The
Unreleased scheduler loop, test-run, and Routines panel sit on top of it.

### Fleet Host MVP

`GET /api/host/status` reports the controller as `authority`. Pairing accepts
`desktop` alongside `ios` and `android`. Linux can install an always-on user
systemd unit that stays on loopback. The Agent Computer worker is not the
host. The desktop app still starts a local controller for a laptop-owned
workspace; using that laptop as a remote client of a Fleet Host is the
intended shape, not the first-run default. Tailscale Serve stays on the
mobile listener (`:4318`), not the desktop API (`:4317`). Funnel is out of
scope.

### Copy

Onboarding and the README lead with coworkers: a real computer, learned
workflows, and collaboration — on infrastructure you control.

### Boundaries

- Group work that needs a live engine still needs that engine actually
  running; otherwise mentioned members stay queued.
- Native signed packages for this version are the Mac/Linux publish step;
  this commit is source-ready, not a claim that Gatekeeper or Linux packages
  are already published.
- Intel Macs are still unsupported. Linux packages remain unsigned.

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
