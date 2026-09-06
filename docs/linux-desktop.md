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
  That bridge carries the Agent collaboration tools, the read-only GitHub
  tools and the opt-in `propose_memory` tool. If it is missing, the native app
  reports "bundled collaboration-mcp executable is unavailable" and cannot
  start its backend. See
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
`OPENAGENTFLEET_REMOTE_TOKEN`. The bridge refuses to start without one, and
the projects, memory proposal and GitHub tasks routes answer 503 until one is
set. Task retry and revise follow the existing API authentication rules: they
remain available in tokenless loopback mode, and once a token is set they need
the normal bearer like every other task route.

With plain `go run ./cmd/botd` there is no sidecar directory and no token, so
in that setup the GitHub and collaboration tools are unavailable and the
Projects, GitHub tasks and Suggested memories features report that a
controller token is needed. To use them outside the native window, build the
bridge, point `botd` at it, and run `botd` with a token that the browser client
also sends:

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

The check predates Projects, task retries, Suggested memories and GitHub
tasks. The bridge shape it verified, `tools/list` returning exactly four
`github_*` names, is what a granted Agent gets with Memory proposals off. The
newer features were checked separately, below.

## Native check of the project and task continuity branch

This is a check of an unreleased branch, not of a tagged build. A debug `.deb`
built from it was unpacked with `dpkg-deb -x`, not installed system-wide, and
the app was started from that payload on the same existing Ubuntu 26.04 XFCE
desktop, driven through WebKitWebDriver against an isolated copy of a QA
database. No
paid engine was involved: the Grok executable on `PATH` was a stand-in that
fails on purpose, so every Agent run below ends as Failed by design. Nothing
from this check has been released or merged.

What passed:

- Startup. The app reached the workspace in about 6 seconds. The `/health`
  reply that the fixed check now reads in full was 559 bytes with the JSON
  body starting at byte 530, past the old 512-byte cut that caused the
  earlier startup timeout.
- Projects. Created a project, edited the brief to version 2, saw both
  versions in Brief history, assigned a conversation to the project, then
  archived the project and watched the conversation's picker flip to
  "(unavailable)". Create and edit had already been exercised on an earlier
  build of this branch.
- Tasks. From a completed task with a CSV result: Change the request and run
  produced attempt 2, Run this again on the failed attempt produced attempt 3.
  The original task kept its status, answer and CSV throughout.
- Suggested memories. Three seeded proposals: one accepted as is, one
  dismissed, one accepted with edits typed but not saved first. The resulting
  database rows matched in each case, including the approved memory written
  for the two accepts.
- GitHub tasks. With a stand-in `gh` answering a fixed identity and a fixed
  issue, and a throwaway local Git repository whose origin named the granted
  repository: the grant completed, Start from an issue created a real isolated
  worktree on the dedicated branch and started a run. That run failed with the
  stand-in provider, as expected. A rebuilt package showed "Task did not
  finish" and explained that review requires a successful task, with a link
  back to the conversation.
- Quit. The app's own `botd` was gone and port 4317 free afterwards. Docker
  containers already running on the host were unchanged.

Not covered by this check: a real engine run, a real GitHub account, any
GitHub API call, push or pull request, and therefore the review and publish
steps of GitHub tasks in the native app. Their server-side logic is covered by
Go tests only.

## Scope of the first Linux alpha

The first Linux alpha will ship the core workspace, local `botd`, provider
selection, chat attachments, model settings, approvals, memory, browser use,
and the observable Agent Computer. Packaging and installer polish come after
the native shell has passed the same fresh-user and computer-use checks as
macOS. Windows is a later target; see [Windows desktop research](windows-desktop.md).
