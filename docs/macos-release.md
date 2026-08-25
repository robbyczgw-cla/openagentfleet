# macOS release runbook

OpenAgentFleet is an open-source public alpha. This runbook documents the
reproducible release gate for macOS distribution and the steps used to publish
future signed builds. The public `v0.1.0-alpha` release has already passed the
Developer ID, notarization, stapling, Gatekeeper, and checksum checks below.

## Measured state of the current checkout

- Product version: `0.3.1`.
- Target: Apple Silicon macOS.
- The normal Tauri configuration uses `signingIdentity: "-"` for local
  development, which produces an ad-hoc signature.
- GitHub prerelease `v0.3.1-alpha` has a Developer ID, notarized, stapled
  Apple Silicon DMG (`OpenAgentFleet_0.3.1_aarch64.dmg`).
- The public GitHub prerelease `v0.3.0-alpha` still contains its notarized
  Apple Silicon DMG. Do not delete it.

Do not change the default Tauri identity merely to make a local build look
released. Use the release override below only after the correct distribution
certificate is installed.

## Required gates

1. Start from a reviewed commit on the `main` branch. The tree must be clean
   and the exact commit must be recorded beside the artifact.
2. Run the code and packaging checks from [CI and QA](ci-qa.md), plus the fresh
   native smoke checklist.
3. Install a valid **Developer ID Application** certificate for the Apple team
   that owns the bundle identifier `com.openagentfleet.desktop`.
4. Configure an App Store Connect API key or a keychain-stored Apple ID
   application-specific password for `notarytool`. Never commit either one.
5. Build with the release identity override, verify the app signature, submit
   the DMG to Apple, staple the ticket, and validate it.
6. Generate a SHA-256 file from the final DMG after notarization/stapling.
7. Only then create a public GitHub prerelease with clear architecture and
   alpha notes.

## Check the signing prerequisite

```sh
security find-identity -v -p codesigning
```

The output must contain an identity beginning with `Developer ID Application:`.
`Apple Development:` is useful for development but is not the distribution
certificate required for a notarized download.

## Build a signed candidate

Keep the normal `tauri.conf.json` ad-hoc-safe for local development. Supply the
distribution identity only for this build:

```sh
export OPENAGENTFLEET_SIGNING_IDENTITY='Developer ID Application: Your Name (TEAMID)'

pnpm --dir client run prepare:sidecar
pnpm --dir client exec tauri build --bundles app,dmg --ci \
  --config "{\"bundle\":{\"macOS\":{\"signingIdentity\":\"${OPENAGENTFLEET_SIGNING_IDENTITY}\"}}}"
```

Verify the produced paths before continuing. Do not use the previous local DMG
just because its filename matches.

```sh
APP='client/src-tauri/target/release/bundle/macos/OpenAgentFleet.app'
DMG='client/src-tauri/target/release/bundle/dmg/OpenAgentFleet_0.3.0_aarch64.dmg'
./scripts/verify-macos-release.sh "$APP" "$DMG"
```

The verifier rejects ad-hoc signatures, missing Team IDs, missing Developer ID
authority, and missing stapled notarization tickets.

## Notarize and staple

Store credentials in the macOS keychain, not in the repository or shell history:

```sh
xcrun notarytool store-credentials openagentfleet-notary \
  --apple-id "$APPLE_ID" \
  --team-id "$APPLE_TEAM_ID" \
  --password "$APPLE_APP_SPECIFIC_PASSWORD"
xcrun notarytool submit "$DMG" \
  --keychain-profile openagentfleet-notary --wait
xcrun stapler staple "$APP"
xcrun stapler validate "$APP"
xcrun stapler staple "$DMG"
xcrun stapler validate "$DMG"
```

If using an App Store Connect API key instead, use the documented `notarytool`
`--key`/`--issuer`/`--key-id` form and keep the `.p8` file outside this repo.

## Checksum and public prerelease

Hash the final, notarized DMG:

```sh
shasum -a 256 "$DMG" > SHA256SUMS
cat SHA256SUMS
```

The current public download remains [`v0.2.0-alpha`](https://github.com/robbyczgw-cla/openagentfleet/releases/tag/v0.2.0-alpha)
until this version is notarized. After the signature, ticket, Gatekeeper and
checksum gates pass:

```sh
gh release create v0.3.0-alpha \
  "$DMG#OpenAgentFleet 0.3.0 Apple Silicon DMG" \
  SHA256SUMS \
  --repo robbyczgw-cla/openagentfleet \
  --title 'OpenAgentFleet v0.3.0-alpha' \
  --prerelease
```

Paste the `0.3.0-alpha` section of [CHANGELOG.md](../CHANGELOG.md) as the
prerelease notes. Keep the alpha label. This checkout is source-ready, not a
claim that Gatekeeper already passed for `0.3.0`. Unsigned Linux packages from
the same commit go on that same prerelease.
