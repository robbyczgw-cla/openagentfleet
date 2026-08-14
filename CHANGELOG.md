# Changelog

All notable OpenAgentFleet changes are recorded here.

## Unreleased

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
