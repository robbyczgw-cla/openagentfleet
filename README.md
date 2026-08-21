# OpenAgentFleet

AI coworkers that run on your computer, not someone else's cloud.

Create named Agents, chat with them one-on-one or in a group, and give them a
Linux desktop you can watch. They use the AI subscriptions you already have.
Sensitive steps wait for your approval.

**Public alpha `v0.3.0`** — not production-ready.
[Apache-2.0](LICENSE).

[Linux](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
· [Windows](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha)
· [macOS 0.2.0 (signed)](https://github.com/robbyczgw-cla/openagentfleet/releases/download/v0.2.0-alpha/OpenAgentFleet_0.2.0_aarch64.dmg)
· [Product site](https://openagentfleet.xyz)
· [Security model](docs/architecture.md)

![OpenAgentFleet Computer View: live Linux desktop with Chromium, Terminal and Files](presentation/assets/openagentfleet-computer-view.png)

That screenshot is the Agent Computer: a separate Linux guest with Chromium,
Terminal and Files — never your host desktop. The app (Tauri) starts a local
Go controller (`botd`) that owns every conversation, memory and run record.
Nothing leaves your machine unless you wire it up to.

## Why this exists

The commercial "AI teammate" products are cloud-hosted, bundle-priced, and
opaque about what their bots do between your messages. OpenAgentFleet takes
the opposite bet:

- **Your machine.** The controller, the chats, the memory — all local.
- **Your subscriptions.** It drives Grok Build (default), Codex App Server,
  bundled OpenCode, or optional Pi. It never silently swaps providers.
- **Your eyes on everything.** Watch runs live, stop them, take over the
  desktop, approve sensitive actions. Agent-to-agent collaboration is off
  until you switch it on per Agent — and when it's on, handoffs happen in
  the chat where you can see them.

## What you get

- **Named Agents** with their own chat and memory. Create as many as you
  want. Mention a teammate, or open a group chat.
- **One Agent Computer per workspace** — a Linux desktop (Ubuntu 24.04 by
  default) that starts only when browser or desktop work needs it. Agents
  take turns on that same PC; creating an Agent does not spawn a VM. Pi has
  no Agent Computer.
- **Approvals with memory.** Sensitive commands and file changes route
  through `botd`. Rules you save (this principal, this resource, this
  operation) stop the app from asking twice.
- **Teach → Skill → Routine.** Show an Agent a task once and keep it.

Extra search connectors (Web Search Plus, Hound, Donsetch), Workers, Fleet
Host, and remote Computer workers live under **Advanced**. Claude Code, Codex
CLI and Cursor are detected as future or bounded adapters — not silent
stand-ins for the workspace engine.

## Install

| Host | What to install |
| --- | --- |
| Linux | Unsigned `.deb` / `.rpm` / `.AppImage` from [`v0.3.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha). Docker Engine recommended for the Agent Computer; the installer does not start it. |
| Windows | Unsigned NSIS installer on the same tag. Docker Desktop recommended; Computer View on WSL2 is not proven by that installer. |
| macOS (Apple Silicon) | Signed [`v0.2.0-alpha` DMG](https://github.com/robbyczgw-cla/openagentfleet/releases/download/v0.2.0-alpha/OpenAgentFleet_0.2.0_aarch64.dmg), SHA-256 `ed466a491051f4facd7696ee67ef2e98235750c5c31a79ff1ffe3060d2f80c06`. A signed `0.3.0` DMG follows once notarization is stapled. Intel Macs are not supported. |

Checksums live on the [release page](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.3.0-alpha).
Release runbooks: [Linux](docs/linux-release.md) ·
[Windows](docs/windows-release.md) ·
[macOS notarization](docs/macos-release-handoff.md).

## First five minutes

1. Pick an engine — Grok Build is the default.
2. Create an Agent and chat.
3. When a task needs a browser or desktop, start the Agent Computer.
   Opening the app never starts Docker or a VM on its own.
4. Create a second Agent and put both in a group chat.

## Phone

iOS and Android pair with the **desktop host** over private Tailscale — a
remote control, not a second runtime. From the phone you can chat, approve,
stop a run, and watch the computer. Typing passwords and driving the full
desktop stay on the host. No store builds yet; see [`mobile/`](mobile/).

## Engines

Provider CLIs run on the host. The Agent Computer isolates browser and
desktop work, not those processes.

- **Grok Build** and **Codex App Server** route sensitive commands and file
  changes through `botd` approvals.
- **OpenCode** keeps its own `ask` mode; its dangerous auto mode stays off.
- **Pi** signs in with `pi /login` (`~/.pi`). Picking a Grok or GPT model id
  inside Pi still runs through Pi.

## Agent Computer

One Linux desktop — Ubuntu 24.04 default, Ubuntu 26.04 and Debian 13
optional — with Chromium, Xfce, Terminal and Files. Default budget: 4 CPU,
4 GiB RAM, 25 GiB disk. It starts only when you open Computer View or an
approved task needs it.

Backends: Docker Engine (Linux), Docker Desktop (Windows), Colima
recommended on macOS (Docker Desktop / OrbStack also work).

No host root mount. No Docker socket in the guest. Details:
[Agent Computer backends](docs/macos-agent-computer-backends.md) ·
[FAQ](docs/faq.md).

## Build from source

Requires Go, Node.js + `pnpm`, Rust, `uv`/`uvx`, and OpenCode **1.18.10**.

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
`VITE_BOTD_URL` set. Useful for development; not a replacement for the
packaged app.

## License

[Apache-2.0](LICENSE) · Runtime notices: [NOTICE.md](NOTICE.md) ·
Docs map: [docs/README.md](docs/README.md)
