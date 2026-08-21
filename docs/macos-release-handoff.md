# Prompt for the macOS v0.3.0-alpha release agent

Copy everything below the line into a Mac builder agent. Do not invent
signing identities or claim notarization that did not happen.

---

You are releasing **OpenAgentFleet v0.3.0-alpha** for **Apple Silicon macOS**.
Linux `.deb`/`.rpm`/AppImage and Windows NSIS are already on GitHub prerelease
`v0.3.0-alpha`. Your job is the notarized DMG only.

## Source of truth

- Repo: `https://github.com/robbyczgw-cla/openagentfleet`
- **Build the tag** `v0.3.0-alpha` (commit `2295d4f`). Do not move the tag.
- `main` has later docs/Claude-adapter commits. Do not retag `main` as
  `v0.3.0-alpha`. If you must ship those commits, cut `v0.3.0-alpha.1`.
- Version in `client/src-tauri/tauri.conf.json`: `0.3.0`
- Bundle id: `com.openagentfleet.desktop`
- Runbook: `docs/macos-release.md`
- Notes: `docs/releases/v0.3.0-alpha.md` (already on the GitHub prerelease)
- OpenCode pin: **exactly** `1.18.10` via `OPENAGENTFLEET_OPENCODE_BINARY`

## Do this

1. `git fetch --tags && git checkout v0.3.0-alpha`. Tree clean. Record `git rev-parse HEAD`.
2. `go test ./...`
3. `pnpm --dir client exec tsc --noEmit`
4. `cargo test --manifest-path client/src-tauri/Cargo.toml --locked`
5. `security find-identity -v -p codesigning` must show
   `Developer ID Application:` for this bundle id. Not `Apple Development:`.
6. Confirm `"$OPENAGENTFLEET_OPENCODE_BINARY" --version` prints `1.18.10`.
   Do not bundle Homebrew `opencode` if the version differs.
7. Build:

```sh
export OPENAGENTFLEET_SIGNING_IDENTITY='Developer ID Application: <Name> (<TEAMID>)'
pnpm --dir client run prepare:sidecar
pnpm --dir client exec tauri build --bundles app,dmg --ci \
  --config "{\"bundle\":{\"macOS\":{\"signingIdentity\":\"${OPENAGENTFLEET_SIGNING_IDENTITY}\"}}}"
```

8. Paths:

```sh
APP=client/src-tauri/target/release/bundle/macos/OpenAgentFleet.app
DMG=client/src-tauri/target/release/bundle/dmg/OpenAgentFleet_0.3.0_aarch64.dmg
./scripts/verify-macos-release.sh "$APP" "$DMG"
```

9. Notarize with `notarytool`. Staple the app and the DMG. Re-run the verifier.
   Reject ad-hoc signatures. `spctl --assess` must pass.
10. SHA-256 the **stapled** DMG. Upload only:
    - `OpenAgentFleet_0.3.0_aarch64.dmg`
    - its checksum line (append to the existing `SHA256SUMS` or upload a Mac
      checksum file; do not delete Linux/Windows assets)
11. Update README Mac download from `v0.2.0-alpha` to this DMG **only after**
    Gatekeeper on a second Mac or a clean user.
12. Comment on the GitHub release: commit SHA, macOS version, Apple Silicon,
    notarization ticket id, staple ok/fail, what you did not verify.

## Honest claims

- Dictation and native secure password handoff are macOS-only. Say that.
- Computer View needs Colima or Docker Desktop. The DMG does not start a VM
  on launch.
- Intel Macs unsupported.
- Do not claim Linux/Windows Computer proofs for this DMG.

## Do not

- Rewrite tag `v0.3.0-alpha`.
- Replace Linux or Windows artifacts.
- Commit Apple API keys, notary passwords, or the Developer ID cert.
- Publish a DMG without a staple.
- Touch Windows/Linux packaging scripts.
