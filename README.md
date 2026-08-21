# OpenAgentFleet

Local-first AI coworkers on a computer you run. Chat with named Agents,
give them a Linux desktop you can watch, and approve the sensitive steps.

**Public alpha `v0.3.0`.** Linux and Windows packages are on
[the release](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
(unsigned). The signed Mac download is still
[`v0.2.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/download/v0.2.0-alpha/OpenAgentFleet_0.2.0_aarch64.dmg)
until a Developer ID `0.3.0` DMG is stapled. Not production-ready.
[Apache-2.0](LICENSE).

[Linux](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
· [Windows](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
· [macOS 0.2.0 (signed)](https://github.com/robbyczgw-cla/openagentfleet/releases/download/v0.2.0-alpha/OpenAgentFleet_0.2.0_aarch64.dmg)
· [Product site](https://openagentfleet.xyz)
· [Security model](docs/architecture.md)

![OpenAgentFleet Computer View: live Linux desktop with Chromium, Terminal and Files](presentation/assets/openagentfleet-computer-view.png)

The desktop app (Tauri) starts a local Go controller (`botd`) and shows this
UI. The Agent Computer is a separate Linux guest — Chromium, Terminal, Files —
not your host desktop.

## What it does

- **Engines:** Grok Build (default), Codex App Server, bundled OpenCode, or
  optional Pi. The product never silently swaps providers. Pi is opt-in
  (`pi /login`); it has no Agent Computer and no extra search connectors.
- **Agents:** named coworkers with their own chat and memory. Create as many
  as you want. Mention a teammate, or open a group chat. Collaboration tools
  stay off until you enable them on that Agent.
- **One Agent Computer per workspace.** Start it when you need browser or
  desktop work. Agents take turns on that same PC; creating an Agent does not
  spawn a VM. Pi cannot use it.
- **Control:** watch live, stop a run, take over, approve sensitive actions.
  Conversations, memory and run records stay in the local controller.

Workers, extra search connectors (Web Search Plus, Hound, Donsetch), Fleet
Host, and remote Computer workers live under **Advanced**. Claude Code, Codex
CLI and Cursor are detected as future or bounded adapters; they are not silent
stand-ins for the workspace engine.

## Download

| Host | What to install |
| --- | --- |
| Linux | Unsigned `.deb` / `.rpm` / `.AppImage` from [`v0.3.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha). Docker Engine is recommended for the Agent Computer and is not started by the installer. |
| Windows | Unsigned NSIS installer on the same tag. Docker Desktop is recommended; Computer View on WSL2 is not proven by that installer. |
| macOS Apple Silicon | Signed [`v0.2.0-alpha` DMG](https://github.com/robbyczgw-cla/openagentfleet/releases/download/v0.2.0-alpha/OpenAgentFleet_0.2.0_aarch64.dmg) (SHA-256 `ed466a491051f4facd7696ee67ef2e98235750c5c31a79ff1ffe3060d2f80c06`). Intel Macs are not supported. |

Checksums: [v0.3.0-alpha](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha).
Linux and Windows builds are unsigned. Source runbooks:
[Linux](docs/linux-release.md), [Windows](docs/windows-release.md),
[macOS notarization](docs/macos-release-handoff.md).

## First run

1. Choose an engine (Grok Build is the default).
2. Create an Agent and chat.
3. When browser or desktop work is needed, start the Agent Computer. Opening
   the app does not start Docker or a VM.
4. Create another Agent when you want a teammate.

## Phone

iOS and Android are companions of the **desktop host** (Linux, Windows, or
Mac), not a second runtime. Pair over private Tailscale. The phone can chat,
approve, stop a run, and watch the computer; typing passwords and driving the
full desktop stay on the host. There is no Play Store / App Store build yet —
see [`mobile/`](mobile/).

## Engines

Provider CLIs run on the host. The Agent Computer isolates browser and
desktop work, not those processes.

- **Grok Build** and **Codex App Server** route sensitive commands and file
  changes through `botd` approvals.
- **OpenCode** keeps its own `ask` mode (dangerous auto mode is off).
- **Pi** signs in with `pi /login` (`~/.pi`). Picking a Grok or GPT id in Pi
  still runs through Pi.

## Agent Computer

One Linux desktop (Ubuntu 24.04 default; Ubuntu 26.04 and Debian 13 optional)
with Chromium, Xfce, Terminal and Files. Default budget: 4 CPU, 4 GiB RAM,
25 GiB disk. It starts only when you open Computer View or an approved task
needs it.

- Linux: Docker Engine
- Windows: Docker Desktop
- macOS: Colima (recommended) or Docker Desktop / OrbStack

No host root mount, no Docker socket in the guest. Details:
[Agent Computer backends](docs/macos-agent-computer-backends.md),
[FAQ](docs/faq.md).

## Build from source

Go, Node.js + `pnpm`, Rust, `uv`/`uvx`, and OpenCode **1.18.10**.

```sh
git clone https://github.com/robbyczgw-cla/openagentfleet.git
cd openagentfleet
go test ./...
cd client
pnpm install
pnpm run prepare:sidecar
pnpm run tauri dev
```

Browser-only UI: `cd client && pnpm install && pnpm dev` with
`VITE_BOTD_URL`. That is not a replacement for the packaged app.

## License

[Apache-2.0](LICENSE). Runtime notices: [NOTICE.md](NOTICE.md).
Docs map: [docs/README.md](docs/README.md).
