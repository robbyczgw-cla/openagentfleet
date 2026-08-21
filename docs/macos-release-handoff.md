# Prompt for the macOS v0.3.0-alpha release agent

Copy everything below the line into a Mac builder agent. Do not invent
signing identities or claim notarization that did not happen.

---

You are releasing **OpenAgentFleet v0.3.0-alpha** for **Apple Silicon macOS**.
Linux `.deb`/`.rpm`/AppImage and Windows NSIS are already published on GitHub
prerelease `v0.3.0-alpha`. Your job is the notarized DMG only.

## Source of truth

- Repo: `https://github.com/robbyczgw-cla/openagentfleet`
- Branch: `main`
- Tag: `v0.3.0-alpha` (if the tag already exists, **do not move it**; build
  that exact commit. If you must add only Mac artifacts, upload them to the
  existing prerelease.)
- Version in `client/src-tauri/tauri.conf.json`: `0.3.0`
- Runbook: `docs/macos-release.md`
- Notes already written: `docs/releases/v0.3.0-alpha.md` and `CHANGELOG.md`
- Bundle id: `com.openagentfleet.desktop`

## What you must do

1. `git fetch && git checkout v0.3.0-alpha` (or the recorded SHA). Tree clean.
2. Run `go test ./...`, `pnpm --dir client exec tsc --noEmit`,
   `cargo test --manifest-path client/src-tauri/Cargo.toml --locked`.
3. Confirm `security find-identity -v -p codesigning` shows
   `Developer ID Application:` for the team that owns the bundle id.
   Do not use `Apple Development:` for the downloadable DMG.
4. Pin OpenCode **exactly** `1.18.10` via `OPENAGENTFLEET_OPENCODE_BINARY`.
   Do not bundle Homebrew `opencode` if `--version` is not `1.18.10`.
5. Build:

```sh
export OPENAGENTFLEET_SIGNING_IDENTITY='Developer ID Application: <Name> (<TEAMID>)'
pnpm --dir client run prepare:sidecar
pnpm --dir client exec tauri build --bundles app,dmg --ci \
  --config "{\"bundle\":{\"macOS\":{\"signingIdentity\":\"${OPENAGENTFLEET_SIGNING_IDENTITY}\"}}}"
```

6. `APP=client/src-tauri/target/release/bundle/macos/OpenAgentFleet.app`
   `DMG=client/src-tauri/target/release/bundle/dmg/OpenAgentFleet_0.3.0_aarch64.dmg`
   `./scripts/verify-macos-release.sh "$APP" "$DMG"`
7. Notarize with `notarytool`, staple the app and the DMG, re-run the
   verifier. Reject ad-hoc signatures.
8. SHA-256 the **final stapled** DMG. Upload `OpenAgentFleet_0.3.0_aarch64.dmg`
   and its checksum to the existing `v0.3.0-alpha` GitHub prerelease. Do not
   replace Linux/Windows assets.
9. Update README download links from `v0.2.0-alpha` to the new DMG **only
   after** Gatekeeper verification on a second Mac or a clean user.
10. Record in the release notes: commit SHA, macOS version, architecture
    (Apple Silicon only), notarization ticket id. Intel Macs stay unsupported.

## Honest claims only

- Dictation and native secure password handoff are macOS-only and should work
  in this DMG. Say so.
- Computer View still needs Colima or Docker Desktop; the DMG does not start
  a VM on launch.
- Do not claim the Linux/Windows Computer proofs apply to this Mac build.
  Run `docs/fresh-user-smoke-test.md` on the packaged app if you can.

## Do not

- Bump the version to 0.3.1 unless `main` moved and the tag is already
  immutable — then cut `v0.3.0-alpha.1` instead of rewriting history.
- Commit Apple API keys, notary passwords, or the Developer ID cert.
- Publish a DMG that fails `spctl --assess` or has no staple.
- Touch Windows/Linux packaging scripts “while you are here”.

When done, comment on the GitHub release with checksum, staple status, and
what you could not verify (second-machine Gatekeeper, live Grok scheduled
runs, etc.).
