#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

if [[ "$(uname -s)" != "Linux" ]]; then
  printf 'Linux release artifacts must be built on GNU/Linux.\n' >&2
  exit 1
fi

version="$(python3 - <<'PY'
import json
from pathlib import Path
print(json.loads(Path("client/src-tauri/tauri.conf.json").read_text())["version"])
PY
)"
triple="$(rustc --print host-tuple)"
case "$triple" in
  x86_64-unknown-linux-gnu) arch="amd64" ;;
  aarch64-unknown-linux-gnu) arch="arm64" ;;
  *)
    printf 'unsupported Linux release target: %s\n' "$triple" >&2
    exit 1
    ;;
esac

out_dir="${OPENAGENTFLEET_LINUX_DIST:-$repo_root/dist/linux}"
mkdir -p "$out_dir"

export PATH="${HOME}/.cargo/bin:${PATH}"
bash "$repo_root/scripts/build-tauri-sidecar.sh"

# Tauri 2.11's in-process rpm-rs bundler hangs after "Bundling …rpm".
# Produce the .deb with Tauri, then build RPM and AppImage from that payload.
pnpm --dir client exec tauri build --bundles deb --ci

bundle_root="$repo_root/client/src-tauri/target/release/bundle"
deb_artifact=""
for artifact in "$bundle_root"/deb/OpenAgentFleet_"${version}"_*.deb; do
  if [[ -f "$artifact" ]]; then
    deb_artifact="$artifact"
    cp -a "$artifact" "$out_dir/"
  fi
done
if [[ -z "$deb_artifact" ]]; then
  printf 'no Debian package was produced under %s/deb\n' "$bundle_root" >&2
  exit 1
fi

bash "$repo_root/scripts/package-linux-from-deb.sh" "$deb_artifact" "$out_dir" "$version"

(
  cd "$out_dir"
  sha256sum OpenAgentFleet*.deb OpenAgentFleet*.rpm OpenAgentFleet*.AppImage > SHA256SUMS
)

copied="$(find "$out_dir" -maxdepth 1 \( -name '*.deb' -o -name '*.rpm' -o -name '*.AppImage' \) | wc -l)"
if [[ "$copied" -lt 3 ]]; then
  printf 'expected deb, rpm and AppImage under %s\n' "$out_dir" >&2
  exit 1
fi

printf 'Linux release artifacts for %s (%s) written to %s\n' "$version" "$arch" "$out_dir"
ls -l "$out_dir"
