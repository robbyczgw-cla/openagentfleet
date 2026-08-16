# Changelog

All notable OpenAgentFleet changes are recorded here.

## Unreleased

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

Pi still has no MCP injection: no Hound, no Web Search Plus, and no Computer
MCP. A Pi engine therefore has no Agent Computer. Enabling those connectors
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
