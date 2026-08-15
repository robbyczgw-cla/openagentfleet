# Windows desktop track

OpenAgentFleet's first product target is macOS. Windows is the next desktop
target, but it must keep the same product contract rather than becoming a
separate, weaker agent implementation.

## The Windows contract

The Windows app should provide the same user-visible flow as macOS:

- a native Tauri desktop shell;
- the Go controller and the selected provider harness on the host;
- the local Agent Computer started only when a run requests it;
- an observable Linux desktop with Chromium, Terminal, Files, browser/CDP
  actions, desktop mouse/keyboard actions, frames, approvals and takeover;
- the same memory, conversations, runs, MCP/plugin policy and notification
  behaviour;
- the same optional remote-worker route over a private authenticated network.

The Agent Computer is still Linux on Windows. The first implementation should
use Docker Desktop's WSL 2 backend and the existing Ubuntu 24.04 image. The
Windows host does not need a second Windows desktop VM for browser or desktop
agent work; the controlled Linux desktop remains the isolation boundary.

## First supported Windows shape

The first release candidate should target Windows 11 x64. Windows 10 support
can follow once the same installer, WebView2 and Docker smoke tests pass on the
supported Windows 10 baseline.

Development prerequisites:

1. Microsoft C++ Build Tools with **Desktop development with C++**.
2. Rust with the MSVC host toolchain.
3. Node.js and pnpm.
4. Microsoft Edge WebView2 Runtime. The Evergreen Runtime is the default
   distribution choice; the installer must detect it and offer the official
   bootstrapper if it is missing.
5. Docker Desktop with WSL 2 enabled, current WSL, hardware virtualization
   enabled, and a Linux-container context available to the Docker CLI.

Docker Desktop's per-user installation is the preferred simple path for most
users. Do not ask users to install a second Docker Engine inside their WSL
distribution; Docker documents that this can conflict with the Docker Desktop
WSL backend.

## Resource and filesystem policy

The Agent Computer defaults stay consistent across desktop hosts:

- Ubuntu 24.04;
- 4 vCPU;
- 4 GiB RAM;
- 25 GiB computer disk;
- 1 GiB guest swap;
- user-adjustable values within the controller's validated limits.

On Windows, Docker Desktop/WSL owns the Linux VM resources while OpenAgentFleet
controls the container contract. The onboarding and Settings copy must explain
that the selected values are container/WSL resources, not a promise that the
entire Docker Desktop VM has exactly that footprint. Before startup, the
controller must check available host storage and fail with a clear retryable
message when the requested workspace/runtime cannot fit.

Keep active repositories and bind-mounted workspaces inside the WSL Linux
filesystem for the Linux computer path where possible. Windows-mounted paths
remain supported only when the runtime health check confirms that file events,
permissions and performance are acceptable for the selected task.

## Packaging plan

The first Windows distribution should be an NSIS setup executable. MSI is a
second installer target for managed environments and requires the Windows MSI
toolchain plus the optional VBScript feature on machines where it is disabled.

Windows signing is a separate release gate. A Windows build without a trusted
Authenticode certificate is a development artifact, not a public release. The
release workflow must produce checksums and record the certificate/timestamp
verification just as the macOS notarization workflow records Apple's ticket.

Cross-compiling an NSIS installer from macOS or Linux may be useful for CI, but
the official Tauri guidance treats it as a fallback. The primary Windows
release build should run on Windows so WebView2 detection, Docker integration,
installer behaviour and GUI startup are tested on the real platform.

## Work sequence

1. Add a Windows CI job for frontend, Go, Rust and sidecar target validation.
2. Add explicit `x86_64-pc-windows-msvc` sidecar naming and `.exe` handling;
   keep macOS and GNU/Linux mappings unchanged.
3. Build the Windows Tauri app and NSIS installer on a Windows runner.
4. Run a real Windows smoke test: launch the app, connect botd, start the
   Docker Agent Computer, verify health and frames, navigate Chromium, send
   mouse/keyboard actions, and exercise stop/restart recovery.
5. Add Windows-specific onboarding checks for WebView2, Docker Desktop/WSL2,
   disk space, and firewall/private-network errors.
6. Add a physical or VM GUI acceptance run before calling Windows alpha ready.
7. Add Authenticode signing and installer update/release verification.

Until steps 3–6 pass, the Windows label is **in development**. The shared
React/Go contracts are portable; native installer, WebView2, Docker/WSL2 and
GUI evidence are not inferred from macOS or Linux CI.

## Official references

- [Tauri Windows prerequisites](https://v2.tauri.app/start/prerequisites/)
- [Tauri Windows installers](https://v2.tauri.app/distribute/windows-installer/)
- [Microsoft WebView2 distribution](https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution)
- [Docker Desktop for Windows](https://docs.docker.com/desktop/setup/install/windows-install/)
- [Docker Desktop WSL 2 backend](https://docs.docker.com/desktop/features/wsl/)
