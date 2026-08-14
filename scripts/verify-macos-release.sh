#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  printf 'usage: %s /path/to/OpenAgentFleet.app /path/to/OpenAgentFleet.dmg\n' "$0" >&2
  exit 2
fi

app_path="$1"
dmg_path="$2"

[[ -d "$app_path" ]] || { printf 'missing app: %s\n' "$app_path" >&2; exit 1; }
[[ -f "$dmg_path" ]] || { printf 'missing dmg: %s\n' "$dmg_path" >&2; exit 1; }

codesign --verify --deep --strict --verbose=2 "$app_path"

signature_details="$(codesign -dvv --verbose=4 "$app_path" 2>&1 || true)"
if grep -q 'Signature=adhoc' <<<"$signature_details" ||
  grep -q 'TeamIdentifier=not set' <<<"$signature_details"; then
  printf 'release verification failed: app is ad-hoc signed\n' >&2
  exit 1
fi
if ! grep -q 'Authority=Developer ID Application:' <<<"$signature_details"; then
  printf 'release verification failed: Developer ID Application authority missing\n' >&2
  exit 1
fi

if ! xcrun stapler validate "$app_path" >/dev/null 2>&1; then
  printf 'release verification failed: notarization ticket is not stapled\n' >&2
  exit 1
fi

dmg_sha256="$(shasum -a 256 "$dmg_path" | awk '{print $1}')"
printf 'macOS release verification passed\n'
printf 'app: %s\n' "$app_path"
printf 'dmg: %s\n' "$dmg_path"
printf 'dmg_sha256: %s\n' "$dmg_sha256"
