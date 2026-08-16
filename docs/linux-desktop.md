# Linux desktop development

Linux is the next native desktop target for OpenAgentFleet. The Linux build
uses the same React client, Go controller (`botd`), harness adapters, memory,
approvals and Agent Computer contract as macOS. The platform-specific shell is
Tauri; the Linux host supplies Docker directly instead of Colima.

## Current status

- Ubuntu 24.04/26.04 x86_64 is the first development target.
- The native Tauri shell compiles on GNU/Linux: it owns the window, starts
  bundled `botd`, and uses the same React client as macOS.
- The shared client and Tauri Rust crate are checked in Ubuntu CI.
- Linux sidecar packaging accepts `x86_64-unknown-linux-gnu` and
  `aarch64-unknown-linux-gnu` targets.
- Fresh Linux installs default the Agent Computer runtime to Docker Engine.
  Colima remains a macOS recommendation.
- Packaged Linux alpha artifacts are `.deb`, `.rpm` and `.AppImage`. See
  [Linux release](linux-release.md). There is no store signature.
- Native macOS dictation and the macOS secure handoff prompt are intentionally
  unavailable on Linux; the web speech/transcription fallback and normal
  approval flow remain available.

## Development prerequisites

On Ubuntu 24.04:

```sh
sudo apt update
sudo apt install -y \
  build-essential curl file libayatana-appindicator3-dev \
  libdbus-1-dev libgtk-3-dev libssl-dev libwebkit2gtk-4.1-dev \
  librsvg2-dev patchelf
```

Install Go, Node.js with pnpm, and Rust with the stable toolchain. The full
sidecar build also needs `uv`, `uvx`, and OpenCode exactly at `1.18.10`; these
are checked before they are bundled. Provider CLIs remain optional and are
detected at runtime.

Run the shared development surface:

```sh
go run ./cmd/botd --data-dir /tmp/openagentfleet-dev --addr 127.0.0.1:4317

cd client
pnpm install
VITE_BOTD_URL=http://127.0.0.1:4317 pnpm dev
```

For a native Tauri development window, prepare Linux sidecars on the Linux
machine and then run:

```sh
cd client
pnpm run prepare:sidecar
pnpm run tauri dev
```

The Agent Computer remains a separate Linux desktop container. On Linux,
OpenAgentFleet uses the selected Docker-compatible engine directly; remote
computer workers can still be reached over the existing authenticated
Tailscale path.

## Scope of the first Linux alpha

The first Linux alpha will ship the core workspace, local `botd`, provider
selection, chat attachments, model settings, approvals, memory, browser use,
and the observable Agent Computer. Packaging and installer polish come after
the native shell has passed the same fresh-user and computer-use checks as
macOS. Windows is a later target; see [Windows desktop research](windows-desktop.md).
