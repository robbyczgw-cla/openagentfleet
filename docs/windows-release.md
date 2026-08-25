# Windows release

Windows is the next packaged desktop after Linux. Build on a real Win11 x86_64
machine with MSVC. Do not cross-compile NSIS from Linux. Do not claim a working
Agent Computer session until Docker Desktop Linux-engine evidence exists.

## What this produces

An unsigned NSIS current-user installer. WebView2 is bootstrapped if missing.
Sidecars are `.exe` files next to `OpenAgentFleet.exe`. There is no Authenticode
or Store listing.

## Builder

Need Git, Go 1.26, Node 22 + pnpm, rustup `x86_64-pc-windows-msvc`, WebView2,
VS 2022 Build Tools (VCTools + Windows 11 SDK), NSIS, uv, OpenCode 1.18.10.
The dockurr Win11 guest bootstrap in `/root/windows/shared` installs that set.

## Commands

```
# VS 2022 x64 Native Tools, or Git Bash after vcvars64.bat
export OPENAGENTFLEET_UV_BINARY=/c/tools/uv/uv.exe
export OPENAGENTFLEET_UVX_BINARY=/c/tools/uv/uvx.exe
export OPENAGENTFLEET_OPENCODE_BINARY=/c/tools/opencode-1.18.10/opencode.exe
bash scripts/build-windows-release.sh
bash scripts/verify-windows-release.sh dist/windows/OpenAgentFleet_*_x64-setup.exe
```

The script reads the version with `node` (do not use the Windows Store `python`
stub) and prepends MSVC `link.exe` so Git Bash `/usr/bin/link` cannot shadow it.
`pnpm` 11 needs `client/pnpm-workspace.yaml` `allowBuilds.esbuild: true`.

After installing locally:

```
bash scripts/verify-windows-release.sh "$LOCALAPPDATA/OpenAgentFleet"
```

`tauri build --bundles nsis` is the only bundle. MSI is out of scope for this
first pass.

## Honest claims

- The window starts bundled `botd`.
- Default Computer runtime is Docker Desktop, Linux engine.
- Native dictation and the secure password prompt stay macOS-only.
- Computer View on Docker Desktop WSL2 failed live QA 2026-08-25 (daemon not up, then image build). That was a live fail, not a README gap.
- Starting the Docker Desktop *service* (Running) is not enough. `Docker Desktop.exe` must be open as the signed-in user; wait until the Linux engine is ready.
- `ubuntu:24.04` Hub pull without login is unproven on this box. `wincred` / `Anmeldesitzung` session errors are a real live fail. Public images do not need a Docker Hub login, but that pull was not completed.
