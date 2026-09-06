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
- The package bundles `collaboration-mcp` next to `botd` and `browser-mcp`.
  That bridge carries the Agent collaboration tools and the read-only GitHub
  tools. If it is missing, the native app reports "bundled collaboration-mcp
  executable is unavailable" and cannot start its backend. See
  [how the bridge is wired](workspace-productivity.md#how-the-tools-reach-github).
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

`prepare:sidecar` also builds `collaboration-mcp`, so the native dev window
gets GitHub and collaboration tools the same way the package does. The native
shell also generates a local API token and passes it to `botd` as
`OPENAGENTFLEET_REMOTE_TOKEN`; the bridge refuses to start without one.

With plain `go run ./cmd/botd` there is no sidecar directory and no token, so
the GitHub and collaboration tools are unavailable in that setup. To use them
outside the native window, build the bridge, point `botd` at it, and run
`botd` with a token that the browser client also sends:

```sh
go build -o /tmp/collaboration-mcp ./cmd/openagentfleet-collaboration-mcp
OPENAGENTFLEET_REMOTE_TOKEN='generated-high-entropy-token' \
OPENAGENTFLEET_COLLABORATION_MCP_BINARY=/tmp/collaboration-mcp \
  go run ./cmd/botd --data-dir /tmp/openagentfleet-dev --addr 127.0.0.1:4317

cd client
VITE_BOTD_URL=http://127.0.0.1:4317 \
VITE_BOTD_TOKEN='generated-high-entropy-token' pnpm dev
```

`botd` also accepts the bridge on `PATH` under its own name,
`openagentfleet-collaboration-mcp`, if you prefer not to set the variable.

The Agent Computer remains a separate Linux desktop container. On Linux,
OpenAgentFleet uses the selected Docker-compatible engine directly; remote
computer workers can still be reached over the existing authenticated
Tailscale path.

## Docker group troubleshooting

When Docker is installed and running but the app reports "Docker is installed
but this user cannot talk to the daemon", the socket is owned by the `docker`
group and your user is not in it yet, or is in it on paper only. The app's
message says to add the user to the group and start a new login session. The
second half is the part that gets skipped.

Group membership is read when a session logs in. `sudo usermod -aG docker
"$USER"` changes the account, not the processes already running. `newgrp
docker` fixes the one shell you ran it in. A desktop session that was already
logged in when you ran `usermod`, and every app launched from it, still carries
the old group list.

The sequence that works:

1. `sudo usermod -aG docker "$USER"`.
2. Quit OpenAgentFleet and confirm `botd` is gone with `pgrep botd`.
3. Log out of the desktop session and log back in. This is what refreshes the
   groups for the launcher and everything it starts.
4. Start OpenAgentFleet again. `id -nG` in a terminal opened after the new
   login should list `docker`.

Restarting the Docker daemon is not part of this. `systemctl restart docker`
does not change who may open the socket, and it may interrupt containers that
are running on the host. Leave the daemon alone; the fix is on your side of
the socket.

If `id -nG` in a fresh terminal lists `docker` but the app still reports
permission denied, the app is still running from the old session. Quit it,
check `pgrep botd`, and launch it from a shell where `id -nG` already shows
`docker`, or complete a full logout and login and launch it from there.

## Native check on Ubuntu 26.04

The packaged app was run on an existing Ubuntu 26.04 XFCE desktop with host
Docker Engine, using an isolated data directory populated with test tasks and
routines. This was not a fresh OS install. What passed:

- Tasks & results listed the test Agent's runs, showed a saved answer, and
  downloaded a linked `.csv`. The fixture used the legacy `outputs/report.csv`
  path, not `outputs/<run-id>/`, so this exercised the shared-path fallback
  described in [Files](workspace-productivity.md#files).
- Routines turned a completed task into a workflow with Save as workflow,
  created a new weekly workflow from the simple picker, and exported and
  re-imported the Markdown. The shipped starter templates were not exercised.
- Connected apps completed the grant, save and disconnect flow against a local
  stand-in `gh` that answered fixed JSON. The bundled bridge was started
  natively and answered MCP `initialize` and `tools/list` with exactly the
  four `github_*` tool names and nothing else. The four reads themselves were
  not called. No live GitHub account was used and nothing on GitHub was
  written.
- Agent Computer started from a stopped state, reported the desktop ready with
  a live frame, accepted Take control, navigated Chromium to `example.com`,
  released control, and stopped the container. Containers already running on
  the host for other purposes were unchanged afterwards.
- Quitting the app stopped `botd`; nothing was left running.

Not covered by this check: a paid engine run, a real GitHub login or a real
GitHub read, the starter templates, a fresh OS install, and any distribution
other than Ubuntu. The [fresh-user smoke checklist](fresh-user-smoke-test.md)
still applies for first-run and engine sign-in evidence.

## Scope of the first Linux alpha

The first Linux alpha will ship the core workspace, local `botd`, provider
selection, chat attachments, model settings, approvals, memory, browser use,
and the observable Agent Computer. Packaging and installer polish come after
the native shell has passed the same fresh-user and computer-use checks as
macOS. Windows is a later target; see [Windows desktop research](windows-desktop.md).
