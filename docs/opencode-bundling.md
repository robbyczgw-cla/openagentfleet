# OpenCode macOS bundling

The Tauri package carries OpenCode `1.18.10` as an external sidecar. The
packaging script chooses the macOS executable matching Rust's host target:
`aarch64-apple-darwin` requires arm64 and `x86_64-apple-darwin` requires x86_64.

Before writing any sidecar artifact, `scripts/build-tauri-sidecar.sh` requires
all of the following:

- an executable OpenCode binary (`opencode` on `PATH`, or
  `OPENAGENTFLEET_OPENCODE_BINARY`);
- an exact `opencode --version` result of `1.18.10`;
- an executable that contains the target architecture.

It exits with the discovered path and version when the pin does not match, so
the release cannot silently absorb a Homebrew or other local upgrade. The
script copies the verified executable to
`client/src-tauri/binaries/opencode-$TARGET_TRIPLE`; Tauri's `externalBin`
setting packages it as the sibling `opencode` executable.

At runtime, the existing sidecar environment prepends that sibling directory
to `PATH`. `botd` therefore resolves the bundled OpenCode before any system
installation. The same setup intentionally continues to expose the bundled
`uv` and `uvx` launchers through `PATH` and the `OPENAGENTFLEET_UV_BINARY` and
`OPENAGENTFLEET_UVX_BINARY` environment variables.

The version pin is a release policy, not a substitute for supply-chain
verification: the script verifies the executable version and architecture but
does not currently pin a per-architecture upstream checksum. Update the pin,
third-party notice, and checksum policy together when upgrading OpenCode.

## Release supply-chain gate

The Agent Computer image carries `runtime/agent-computer/package-lock.json`,
uses `npm ci --omit=dev --ignore-scripts`, and pins the Node base image by
digest in its Dockerfile. Keep the lockfile and digest in sync with deliberate
image upgrades; a distributable image build must fail on a stale lockfile.

The macOS sidecar still needs a recorded SHA-256 for each supported
architecture, alongside the upstream release URL, version, and verification
command. Version and architecture checks alone are not a complete provenance
claim. Until those hashes are present, this document remains a release
checklist rather than a completed distribution attestation.
