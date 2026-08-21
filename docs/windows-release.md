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
# Git Bash, from the repo root
export OPENAGENTFLEET_UV_BINARY=/c/tools/uv/uv.exe
export OPENAGENTFLEET_UVX_BINARY=/c/tools/uv/uvx.exe
export OPENAGENTFLEET_OPENCODE_BINARY=/c/tools/opencode-1.18.10/opencode.exe
bash scripts/build-windows-release.sh
bash scripts/verify-windows-release.sh dist/windows/OpenAgentFleet_*_x64-setup.exe
```

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
- Computer View against Docker Desktop WSL2 is not proven by this script.
