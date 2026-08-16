# Changelog

All notable OpenAgentFleet changes are recorded here.

## Unreleased

- Started Windows desktop research: NSIS + Docker Desktop/WSL2 is the first
  alpha contract; see `docs/windows-desktop.md`.
- Added a native Linux desktop alpha with `.deb`, `.rpm` and `.AppImage`
  packages. Fresh Linux installs default the Agent Computer to Docker Engine.
- Docker is recommended by the Linux packages, not silently started; Computer
  View builds the isolated container on demand.
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
