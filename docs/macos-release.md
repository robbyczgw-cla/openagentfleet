# macOS release runbook

OpenAgentFleet is currently a private, source-build alpha. This runbook is the
gate for the first signed macOS release; it deliberately does not publish a
repository or release by itself.

## Measured state of the current checkout

- Product version: `0.1.0`.
- Target: Apple Silicon macOS.
- The normal Tauri configuration uses `signingIdentity: "-"` for local
  development, which produces an ad-hoc signature.
- The current keychain has an Apple Development identity, but no verified
  `Developer ID Application` identity.
- The existing local DMG is therefore a debug/alpha artifact, not a release
  asset. It has a SHA-256 hash, but it is not notarized.

Do not change the default Tauri identity merely to make a local build look
released. Use the release override below only after the correct distribution
certificate is installed.

## Required gates

1. Start from a reviewed commit on the private `main` branch. The tree must be
   clean and the exact commit must be recorded beside the artifact.
2. Run the code and packaging checks from [CI and QA](ci-qa.md), plus the fresh
   native smoke checklist.
3. Install a valid **Developer ID Application** certificate for the Apple team
   that owns the bundle identifier `com.openagentfleet.desktop`.
4. Configure an App Store Connect API key or a keychain-stored Apple ID
   application-specific password for `notarytool`. Never commit either one.
5. Build with the release identity override, verify the app signature, submit
   the DMG to Apple, staple the ticket, and validate it.
6. Generate a SHA-256 file from the final DMG after notarization/stapling.
7. Only then create a draft GitHub release. The repository may remain private
   while invited testers validate the asset. Making either repository public is
   a separate, explicit decision.

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
DMG='client/src-tauri/target/release/bundle/dmg/OpenAgentFleet_0.1.0_aarch64.dmg'
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
```

If using an App Store Connect API key instead, use the documented `notarytool`
`--key`/`--issuer`/`--key-id` form and keep the `.p8` file outside this repo.

## Checksum and private draft release

Hash the final, notarized DMG:

```sh
shasum -a 256 "$DMG" > SHA256SUMS
cat SHA256SUMS
```

Only after the signature, ticket and checksum pass may a maintainer create a
private draft release for invited testers:

```sh
gh release create v0.1.0-alpha \
  "$DMG#OpenAgentFleet 0.1.0 Apple Silicon DMG" \
  SHA256SUMS \
  --repo robbyczgw-cla/openagentfleet \
  --draft \
  --title 'OpenAgentFleet 0.1.0 alpha'
```

Do not run the final release command until the artifact is signed and
notarized. A GitHub release in a private repository is still not a public
download; repository visibility must be changed separately and deliberately.
