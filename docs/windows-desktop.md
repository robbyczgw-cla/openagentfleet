# Windows desktop research

Windows is a later native desktop target for OpenAgentFleet. It is **not**
implemented as a product on this branch. The official packaged product remains
the Apple Silicon macOS Tauri app. Linux is the next shipping desktop path;
see [Linux desktop](linux-desktop.md) and [Linux release](linux-release.md).

This note records what a Windows groundwork branch would have to change, and
what it must not claim. It is research plus a file-level contract. It is not
evidence that a Windows shell, installer, or Agent Computer session has been
built or smoke-tested.

The terms **implemented**, **compiles**, and **contract** are deliberate:

- **Implemented** means this repository already contains the behavior and,
  where practical, a matching test.
- **Compiles** means the source is written so a Windows `GOOS`/`cfg` path
  exists, or the shared code is portable enough that a Windows toolchain
  would likely accept it. It is not a measured `go test` / `cargo test` /
  `tauri build` result on a Windows host.
- **Contract** is a specified target. It is not available today.

## Current status

- There is no Windows installer, no Windows CI job, and no Windows sidecar
  target. `scripts/build-tauri-sidecar.sh` exits for any rustc triple other
  than Apple Darwin and GNU/Linux.
- `docs/linux-desktop.md` still states the product fact: Windows remains a
  later target.
- The Agent Computer image stays a Linux guest (Ubuntu 24.04 default,
  Ubuntu 26.04 / Debian 13 alternatives) with Xvfb, Xfce, Playwright
  Chromium, Terminal and Files. A Windows host would run that same image
  through Docker Desktop's Linux engine. It would not become a Windows
  container or a native Win32 desktop.
- Colima and OrbStack are not Windows products. A Windows host must not
  inherit the macOS Colima default.
- Native macOS dictation and the macOS secure handoff prompt are already
  stubbed off for every non-macOS OS. Windows would keep the same
  unavailability as Linux: web speech / controller STT, and the normal
  approval flow.

## What already compiles or is Windows-aware

These are repository observations, not a passing Windows build.

| Area | State |
| --- | --- |
| Tauri crate type | `client/src-tauri/Cargo.toml` already uses `client_lib` because Cargo's Windows lib/bin name clash is a known issue. |
| Console window | `client/src-tauri/src/main.rs` already sets `windows_subsystem = "windows"` in release. |
| Icons | `icon.ico` and the Microsoft Store tile set already exist under `client/src-tauri/icons/`. |
| Native dictation | Non-macOS stub reports `available: false`. |
| Secure prompt | `prompt_secret` returns `"secure prompt is supported on macOS only"` off macOS. |
| SIGTERM | `request_sigterm` is a no-op on non-unix; shutdown then force-kills after two seconds. |
| Docker child processes | `internal/compute/process_windows.go` exists. It is a plain `exec.CommandContext` with no process group and no job object. Docker CLI plugins can therefore survive cancellation. |
| Host free space | `internal/compute/host_capacity_other.go` compiles on Windows and returns `"host free-space checks are not implemented on this platform"`. |
| SQLite path | `internal/store/store.go` already prefixes a Windows drive path for the `file:` DSN. |
| Data-dir chmod | The same file skips POSIX `0700` enforcement on Windows. That is honesty, not an ACL substitute. |
| ACP tests | `internal/harness/acp_test.go` already treats Windows like a permission-skip host. |
| Store mode tests | `internal/store/store_test.go` skips POSIX directory-mode checks on Windows. |
| Search connector env | `internal/websearchplus/manager.go` already allowlists `SYSTEMROOT`, `WINDIR`, `TEMP`, and `TMP`. |
| Host OS in bootstrap | `botd` already sends `host_os: runtime.GOOS`. The client only special-cases `"linux"`; a Windows `host_os` would currently fall through to the **macOS** Settings branch (Colima recommended). |
| Go module | `go 1.26.0` and `modernc.org/sqlite` are portable. `cmd/botd` uses `syscall.SIGTERM`, which Go defines on Windows. |

The shared React client, HTTP API, memory, approvals, harness adapters and
Agent Computer **image** are OS-neutral. They do not prove a Windows Tauri
window or a Docker Desktop Computer.

## What is unix-only or would misbehave on Windows

### Sidecar packaging — unimplemented

`scripts/build-tauri-sidecar.sh` only accepts:

- `aarch64-apple-darwin` / `x86_64-apple-darwin`
- `aarch64-unknown-linux-gnu` / `x86_64-unknown-linux-gnu`

A Windows rustc triple is `x86_64-pc-windows-msvc` (first target) or
`aarch64-pc-windows-msvc` (later). Tauri `externalBin` on Windows requires
the suffix **and** `.exe`:

```text
client/src-tauri/binaries/botd-x86_64-pc-windows-msvc.exe
client/src-tauri/binaries/browser-mcp-x86_64-pc-windows-msvc.exe
client/src-tauri/binaries/uv-x86_64-pc-windows-msvc.exe
client/src-tauri/binaries/uvx-x86_64-pc-windows-msvc.exe
client/src-tauri/binaries/opencode-x86_64-pc-windows-msvc.exe
```

The script also:

- sets `GOOS=darwin` or `GOOS=linux`, never `windows`;
- architecture-checks with `lipo` (macOS) or `file` (Linux), not a PE
  check;
- `chmod 755` copies, which is meaningless on NTFS;
- is invoked as `bash ../scripts/build-tauri-sidecar.sh` from
  `client/package.json`. A first Windows groundwork can keep bash via Git
  for Windows. A later contract is a PowerShell sibling, not a silent
  rewrite of the macOS/Linux path.

OpenCode `1.18.10` publishes `opencode-windows-x64.zip`. `uv` / `uvx` have
official Windows builds. The pin stays exactly `1.18.10`. Version and
architecture checks are still not a checksum attestation; see
[OpenCode bundling](opencode-bundling.md).

### Tauri sidecar environment — unix-shaped

`configure_sidecar_environment` in `client/src-tauri/src/lib.rs` clears the
environment and then copies a unix-oriented allowlist (`HOME`, `USER`,
`LOGNAME`, `TMPDIR`, `XDG_*`, `DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`,
`DBUS_SESSION_BUS_ADDRESS`, `DOCKER_*`). On Windows, `docker.exe` and most
provider CLIs also need at least:

- `USERPROFILE`, `HOMEDRIVE`, `HOMEPATH`
- `APPDATA`, `LOCALAPPDATA`, `PROGRAMDATA`, `PROGRAMFILES`
- `SYSTEMROOT`, `WINDIR`, `COMSPEC`, `PATHEXT`
- `TEMP`, `TMP` (not only `TMPDIR`)

`default_sidecar_path()` is Homebrew/FHS. A Windows fallback must include
`%SystemRoot%\System32`, `%SystemRoot%`, Docker Desktop's CLI directory
(`%ProgramFiles%\Docker\Docker\resources\bin` and the per-user
`%LOCALAPPDATA%\Programs\DockerDesktop\resources\bin`), and the inherited
user `PATH`.

`bundled_executable_path("botd")` looks for a sibling named `botd`. Tauri
renames the packaged sidecar to `botd.exe` on Windows. Groundwork must
resolve `botd.exe` / `uv.exe` / `uvx.exe` / `opencode.exe` /
`browser-mcp.exe` or the owned child will never start.

### Runtime selection — would recommend Colima

`internal/preferences/preferences.go` `defaultComputerRuntime()` returns
Docker Engine on `linux` and **Colima** on every other OS. Windows is
"every other OS" today.

`DiscoverRuntimes` on non-linux still inventories Colima, OrbStack and
Apple Container. `classifyDockerContext` recognizes `desktop-linux` and
unix socket paths under `~/.docker`, `~/.colima`, `~/.orbstack`. It does
not recognize the Docker Desktop Windows default:

```text
npipe:////./pipe/docker_engine
```

`findExecutable` searches `/opt/homebrew/bin`, `/usr/local/bin`,
`/usr/bin`, `/snap/bin` after `LookPath`. That will miss a per-user Docker
Desktop install unless the Tauri PATH inheritance is fixed first.

The Settings runtime `<select>` in `client/src/App.tsx` uses `isLinuxHost()`,
which is `host_os === "linux"` else the macOS list. Windows would be offered
Colima as recommended. A Windows groundwork must add an `isWindowsHost()`
branch that recommends Docker Desktop and hides Colima, OrbStack and Apple
Container.

### Agent Computer host I/O — unix assumptions

- `checkHostStorage` runs only for Colima or `runtime.GOOS == "linux"`.
  Docker Desktop on Windows currently skips the free-space preflight, the
  same as Docker Desktop on macOS. `linuxDockerStorageRoot()` looks at
  `/var/lib/docker`, which is inside the WSL2 VM and is not a Windows host
  path.
- Isolation `forbiddenHostPath` is unix-centric (`/etc`, `/Users`, `/home`,
  `docker.sock`, `.colima`, `.orbstack`). It does not mention
  `\\.\pipe\docker_engine`, `C:\Windows`, or `C:\Program Files`. Extra
  mounts under the Windows user profile would still be rejected if
  `UserHomeDir()` works, because the planner forbids the entire home tree.
- Workspace bind mounts from an NTFS path into a Linux container go through
  Docker Desktop's WSL2 file share. That is the Windows analogue of the
  macOS virtiofs problem already documented for Chromium profile locks.
  The current implementation already keeps the Chromium profile on a
  **Docker-managed volume**, which is the right contract on Windows too.
- `runtime/agent-computer/entrypoint.sh` already treats chmod-on-bind-mount
  failures as non-fatal for the same reason.

### Secret handoff transport — unix socket

`internal/secrethandoff/native_socket.go` listens on a Unix-domain socket,
`chmod 0600`, and checks `os.ModeSocket` plus parent-directory `077` bits.
Windows 10+ has `AF_UNIX`, but those POSIX permission checks will fail
closed. Combined with the already-disabled native prompt, Windows first
alpha has **no** secure password handoff. That matches Linux. It is not a
Windows CredUI implementation.

### Tests that assume a POSIX shell

Several compute tests write `#!/bin/sh` fakes (`runtime_test.go`,
`docker_test.go`). Those are not `//go:build !windows` and will not pass
on `windows-latest` without skips or Go-native fakes. A groundwork CI job
must not pretend `go test ./...` is green until those are fixed or gated.

## Packaging formats

Tauri 2 on Windows produces two official installers. MSI can only be built
on Windows (WiX v3). NSIS can be cross-compiled from Linux/macOS with
`cargo-xwin`, but Tauri documents that path as a last resort.

| Format | Who it is for | First-alpha contract |
| --- | --- | --- |
| NSIS `-setup.exe` | Everyday download. Default **current-user** install into `%LOCALAPPDATA%`, no Administrator prompt. | **Yes.** Primary artifact. |
| MSI `.msi` | Per-machine / enterprise. Requires the VBSCRIPT optional feature on the build machine. | Later. Not required to prove the shell. |
| Portable zip | `.exe` plus sidecars plus `agent-computer/` resources. Still needs WebView2 on the machine. | Later. Useful for CI smoke, not the user-facing alpha. |
| Microsoft Store | Requires the offline WebView2 installer option and Store listing work. | Out of scope. |

`tauri.conf.json` currently has `bundle.targets: "all"` and a `linux` /
`macOS` block but **no** `bundle.windows` block. A groundwork branch should
add an explicit Windows section rather than inherit "all":

- `webviewInstallMode.type: downloadBootstrapper` (Tauri default). WebView2
  is preinstalled on Windows 11 and recent Windows 10. Do not embed the
  ~180 MB fixed runtime for an alpha.
- NSIS `installMode: currentUser`.
- Do **not** declare Docker Desktop as a hard installer dependency. Same
  honesty as Linux `Recommends: docker.io \| docker-ce`: the app starts
  without it; Computer View explains the missing engine.

Signing is a later gate. Tauri 2 can sign through Azure Artifact Signing
(formerly Trusted Signing) via `signCommand`. An unsigned NSIS alpha will
show a SmartScreen warning. That is acceptable for an internal or
clearly-labelled research build. It is not a public-release claim.

Windows 7 is not a target. First alpha is **Windows 11 x86_64**, with
Windows 10 22H2 as a stretch if WebView2 is present. ARM64 Windows and
Docker Desktop ARM are Early Access on the Docker side and later on ours.

## Agent Computer on Windows

**Contract:** the Computer remains a Linux container. The Windows host
supplies Docker Desktop with the **Linux engine** (WSL2 backend). Opening
the app still must not start a VM or a container.

**Implemented today:** none of this is selected or documented as a Windows
product path.

### Runtime

| Runtime | Windows status |
| --- | --- |
| Docker Desktop, Linux containers, WSL2 | **Contract default.** `docker.exe` on PATH; default context endpoint `npipe:////./pipe/docker_engine`. |
| Docker Desktop, Windows containers | **Forbidden.** The Agent Computer image is Ubuntu/Debian. Switching the engine to Windows containers must be detected and refused with an explicit error. |
| Colima | Not a Windows product. Do not offer it. |
| OrbStack | macOS only. Do not offer it. |
| Apple Container | macOS only. Do not offer it. |
| Docker Engine in a user-owned WSL distro | Possible later for advanced users. Not the first-alpha default. The packaged Tauri app is a Win32 process, not a WSL process, so it talks to Docker Desktop's named pipe, not `/var/run/docker.sock` inside WSL. |
| Remote Computer worker | Already implemented as an optional Tailscale path. A Windows host can keep using a remote Linux worker if local Docker Desktop is absent. That is an escape hatch, not the local-computer claim. |

Docker Desktop system requirements that matter to us (from Docker's current
Windows install docs, retrieved 2026-08-15):

- WSL 2.1.5+ (2.6+ if Enhanced Container Isolation is ever considered);
- Windows 10 22H2 or Windows 11 23H2+ in a still-serviced SKU;
- 8 GB RAM, SLAT, firmware virtualization;
- Docker Desktop is **not** supported on Windows Server;
- commercial use in large enterprises needs a paid Docker Desktop
  subscription. The installer must not hide that.

Per-user Docker Desktop installs to
`%LOCALAPPDATA%\Programs\DockerDesktop` and does not require admin. First
enablement of WSL2 on the machine **does** require elevation. The
OpenAgentFleet installer must not try to install WSL2 or Docker Desktop.

`botd` never opens `docker.sock` itself. It shells out to `docker`. If
`docker.exe` is on the inherited PATH and Docker Desktop is running in
Linux-container mode, the existing CLI flow can work. The named-pipe vs
unix-socket distinction is therefore a **discovery and diagnostics**
problem, not a new transport inside `botd`.

### Linux GUI container on Docker Desktop Windows

The guest already runs Xvfb + Xfce + Playwright Chromium with
`--shm-size 256m` and the existing `--no-sandbox` Chromium launch. Those
processes execute **inside the Linux VM**, not on Win32. Official Docker
sources do not document this exact Xfce/Chromium workload on Windows.
What is documented, and what this repo already assumes on macOS Docker
Desktop, is enough to state the risks without claiming a passing run:

- The workload is a Linux container. WSL2 vs Hyper-V changes the VM, not
  the Dockerfile.
- Bind-mounting the Windows home into the container is slow (9P/file
  share) and is already forbidden by isolation policy.
- The Chromium profile volume stays inside the Linux VM. That avoids the
  POSIX symlink-lock failure already seen on macOS virtiofs.
- The workspace bind from `%APPDATA%\com.openagentfleet.desktop\...` will
  be the slow path. First-alpha can accept that, the same way macOS
  accepts a virtiofs workspace. Do not silently move the workspace into
  WSL's ext4 without a product decision.
- `/dev/shm` is already sized. Leaving it at 64 MB is a known Chromium
  crash.
- WSL2 clock skew can break TLS inside the guest. That would show up as
  Chromium or provider failures, not as a missing window.
- Docker Desktop must remain in **Linux containers** mode. A user who
  "Switch to Windows containers" will get a failed image build. Surface
  that as `runtime_detail`, not a generic Docker error.
- First image build of `openagentfleet-agent-computer:ubuntu-24.04` on a
  cold Docker Desktop is slow. The 90 second Chromium-ready wait in
  `Docker.ensure` may need a Windows-specific budget after measurement.
  Do not change it before a live run.

This is **not** evidence that Computer View has been seen on Windows.

## Native Windows gaps

| Capability | macOS | Linux alpha | Windows first alpha |
| --- | --- | --- | --- |
| Window + bundled `botd` | Implemented | Implemented development path | Contract |
| Agent Computer | Colima / Docker Desktop / OrbStack | Docker Engine | Docker Desktop Linux engine |
| Secure password / OTP prompt | `NSSecureTextField` + unix socket | Unavailable; normal approvals | Unavailable; same as Linux |
| Native dictation | Apple Speech | Unavailable; web speech / STT | Unavailable; same as Linux |
| Host free-space preflight | `Statfs` | `Statfs` | Unimplemented (`GetDiskFreeSpaceEx` later) |
| Child-process teardown | process group + SIGKILL | process group + SIGKILL | no-op SIGTERM, then `kill`; Job Objects later |
| Data-dir privacy | POSIX `0700` | POSIX `0700` | chmod skipped; ACLs later |
| Code signature | Developer ID + notarization | none | none in first alpha; Authenticode later |

A later native secure prompt would be a Win32 dialog with a password edit
control or `CredUI`, talking to `botd` over a locked-down local transport.
Windows `AF_UNIX` is a candidate only after the permission model is
rewritten; do not reuse `chmod 0600` checks. Desktop secret entry stays
out of scope, same as macOS.

A later native dictation path would use the Windows Speech API / WinRT
speech recognizer. First alpha must not claim it.

## Security and install story

Honest first-alpha install:

1. User installs the NSIS current-user `-setup.exe`.
2. Windows 11 usually already has WebView2; otherwise the bootstrapper
   downloads it.
3. The app starts, owns `botd` on `127.0.0.1:4317`, and is usable for
   chat, memory, approvals and provider login **without Docker**.
4. Computer View, or an approved desktop/browser task, requires Docker
   Desktop with the WSL2 Linux engine. The UI explains that. It does not
   install Docker, enable WSL2, or start the engine.
5. The bundled `runtime/agent-computer` context is used to build
   `openagentfleet-agent-computer:ubuntu-24.04` on demand.

Residual risks that must stay visible:

- Docker Desktop's daemon pipe is equivalent to admin on the Linux VM.
  Isolation already forbids mounting `docker.sock`; Windows groundwork
  must also forbid `\\.\pipe\docker_engine` as a guest mount.
- Docker Desktop's own license is not Apache-2.0. Personal and small-team
  use is free; large-enterprise commercial use is paid. OpenAgentFleet
  must not imply that Docker Desktop is bundled or relicensed.
- An unsigned installer will trip SmartScreen. Label the build alpha.
- `env_clear()` without Windows system variables is a security foot-gun
  in the other direction: `docker.exe` will not start, so Computer looks
  "safe" because it is broken.
- Provider CLIs still run on the **Windows host**, not in the Linux
  guest. That is the same boundary as macOS.

## First-alpha scope versus later work

### Honest first-alpha scope (groundwork + first packaged try)

Ship only after these are true, and say nothing stronger:

- Windows 11 x86_64 Tauri window starts bundled `botd`.
- Sidecar script accepts `x86_64-pc-windows-msvc`, writes `.exe` names,
  sets `GOOS=windows`, and checks PE/arch without `lipo`.
- Default Computer runtime is `docker_desktop`. Settings hide Colima,
  OrbStack and Apple Container.
- Sidecar environment inherits the Windows variables listed above and
  can resolve `docker.exe`.
- Native dictation and secure prompt remain unavailable, with the same
  copy as Linux.
- NSIS current-user installer is produced on a Windows builder (GitHub
  `windows-latest` or a real Win11 machine). Checksums are published.
  There is no Authenticode, Store listing, or "works offline without
  WebView2" claim.
- CI gains a Windows job that typechecks the client, `cargo test`s the
  Tauri crate with `.exe` sidecar placeholders, and runs the subset of
  `go test` that does not require a POSIX shell. A red `go test ./...`
  on Windows is not a silent skip.
- Computer View is attempted against Docker Desktop WSL2 and the result
  is recorded as live evidence or as a failure. **Do not claim a working
  Chromium/Xfce session until that evidence exists.**

Packaging polish, ARM64, MSI, portable zip, and signing come after the
shell has passed the same fresh-user checklist as macOS/Linux, on a real
Windows machine, with the proof labelled "packaged Windows app + Docker
Desktop Linux engine", not Colima.

### Later work (not first alpha)

- Authenticode / Azure Artifact Signing and SmartScreen reputation.
- MSI and Microsoft Store.
- `aarch64-pc-windows-msvc` once Docker Desktop ARM is no longer Early
  Access for our workload.
- `GetDiskFreeSpaceEx` host free-space preflight.
- Windows Job Objects so cancelled `docker` trees die.
- ACL lockdown of the app data directory.
- Native dictation and a CredUI / password-dialog secure prompt.
- Classify `npipe://` endpoints as Docker Desktop and refuse Windows
  container mode in `runtime_detail`.
- Optional named volume for the workspace if NTFS bind-mount I/O is
  measured unusable.
- PowerShell sidecar script.
- A Windows release runbook analogous to [linux-release.md](linux-release.md).

## File-level change list for a Windows groundwork branch

Do not implement these in this research commit. If a groundwork branch is
opened, this is the starting list.

1. `scripts/build-tauri-sidecar.sh` — accept `x86_64-pc-windows-msvc`
   (and later `aarch64-pc-windows-msvc`); `GOOS=windows`; emit `.exe`
   names; replace `lipo`/`file` with a PE/arch check; keep the OpenCode
   `1.18.10` pin.
2. `client/src-tauri/src/lib.rs` — Windows PATH / env allowlist;
   resolve `botd.exe` siblings; optional Ctrl+C / `taskkill` instead of
   a SIGTERM no-op.
3. `client/src-tauri/tauri.conf.json` — `bundle.windows` with NSIS
   current-user and `downloadBootstrapper`. Restrict `targets` on a
   Windows release script rather than shipping an accidental MSI.
4. `scripts/build-windows-release.sh` and
   `scripts/verify-windows-release.sh` — NSIS-only first artifacts,
   checksums, sidecar `.exe` presence, bundled `agent-computer/Dockerfile`.
   Build on Windows; do not claim cross-compile.
5. `internal/preferences/preferences.go` — `defaultComputerRuntime()`
   returns `docker_desktop` on `windows`.
6. `internal/compute/runtime.go` — Windows discovery order; hide
   Colima / OrbStack / Apple Container; classify `npipe://` /
   `docker_engine`; Docker Desktop install URL instead of `brew` /
   `apt`; refuse Windows-container OS in health text.
7. `internal/compute/resources.go` and `host_capacity_other.go` — either
   implement `GetDiskFreeSpaceEx` or keep the explicit "not implemented"
   error and skip the Linux `/var/lib/docker` probe.
8. `internal/compute/process_windows.go` — document the missing job
   object; add a test that at least compiles on Windows.
9. `internal/isolation/planner.go` — forbid `\\.\pipe\docker_engine`,
   `npipe:`, and Windows system roots as guest mounts.
10. `client/src/App.tsx` — `isWindowsHost()` Settings branch;
    Docker Desktop recommended; Colima install CTA hidden.
11. `.github/workflows/ci.yml` — `windows-latest` job for client
    typecheck, placeholder `.exe` sidecars, `cargo test`, and a
    Windows-safe Go subset.
12. `docs/windows-release.md` — only when an artifact has actually been
    built. Do not create a release runbook that implies a shipping
    installer.
13. `CONTRIBUTING.md` and `README.md` — mention Windows as research /
    groundwork only until a live Computer proof exists.
14. Tests: skip or rewrite `#!/bin/sh` fakes; add
    `classifyDockerContext` cases for `npipe:////./pipe/docker_engine`
    and `desktop-windows` (the latter must **not** be treated as a
    supported Agent Computer backend).

## What this note does not prove

- That `cargo tauri build` succeeds on Windows.
- That WebView2, SmartScreen, or a Defender false-positive was handled.
- That Docker Desktop WSL2 can start the Xfce/Chromium Agent Computer
  and return a non-blank desktop frame.
- That NTFS workspace bind mounts preserve the permissions the
  entrypoint expects.
- That OpenCode `1.18.10` windows-x64, `uv.exe`, and `uvx.exe` have been
  pinned by checksum in this repo.
- That Windows is scheduled, staffed, or part of the current Linux alpha.

Use the [fresh-user smoke checklist](fresh-user-smoke-test.md) on a real
Windows 11 + Docker Desktop machine before any public Windows artifact.
Record that the proof was a packaged Windows app plus Docker Desktop's
Linux engine, not Colima, and not this document.
