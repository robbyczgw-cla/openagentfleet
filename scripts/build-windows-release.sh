#!/usr/bin/env bash
set -euo pipefail

# Produce the first Windows artifact: an NSIS current-user installer.
# Must run on Windows (Git Bash) with MSVC, WebView2, and the sidecar toolchain.
# This does not Authenticode-sign and does not claim a working Computer session.

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

if [[ "$(uname -s)" != MINGW* && "$(uname -s)" != MSYS* && "$(uname -s)" != CYGWIN* && "$(uname -s)" != Windows_NT ]]; then
  if [[ "${OS:-}" != "Windows_NT" ]]; then
    printf 'Windows release artifacts must be built on Windows.\n' >&2
    exit 1
  fi
fi

triple="$(rustc --print host-tuple)"
case "$triple" in
  x86_64-pc-windows-msvc) ;;
  *)
    printf 'unsupported Windows release target: %s (want x86_64-pc-windows-msvc)\n' "$triple" >&2
    exit 1
    ;;
esac

version="$(python - <<'PY'
import json
from pathlib import Path
print(json.loads(Path("client/src-tauri/tauri.conf.json").read_text())["version"])
PY
)"

out_dir="${OPENAGENTFLEET_WINDOWS_DIST:-$repo_root/dist/windows}"
mkdir -p "$out_dir"

export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:${PATH}"
bash "$repo_root/scripts/build-tauri-sidecar.sh"

pnpm --dir client exec tauri build --bundles nsis --ci

bundle_root="$repo_root/client/src-tauri/target/release/bundle"
copied=0
for artifact in "$bundle_root"/nsis/*.exe "$bundle_root"/nsis/*.msi; do
  if [[ -f "$artifact" ]]; then
    cp -a "$artifact" "$out_dir/"
    copied=$((copied + 1))
  fi
done
if [[ "$copied" -lt 1 ]]; then
  printf 'no NSIS installer was produced under %s/nsis\n' "$bundle_root" >&2
  exit 1
fi

(
  cd "$out_dir"
  if command -v sha256sum >/dev/null; then
    sha256sum ./* > SHA256SUMS
  else
    certutil -hashfile "$(ls -1 | head -1)" SHA256 >/dev/null
    python - <<'PY'
import hashlib, pathlib
root = pathlib.Path(".")
lines = []
for path in sorted(root.iterdir()):
    if path.name == "SHA256SUMS" or not path.is_file():
        continue
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    lines.append(f"{digest}  {path.name}")
pathlib.Path("SHA256SUMS").write_text("\n".join(lines) + "\n")
PY
  fi
)

printf 'Windows NSIS artifacts for %s written to %s\n' "$version" "$out_dir"
ls -l "$out_dir"
