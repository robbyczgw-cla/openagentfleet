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

# Git for Windows ships /usr/bin/link (coreutils hardlink). rustc windows-msvc
# searches PATH for link.exe and will pick that GNU binary first from Git Bash.
if [[ -x /usr/bin/link ]]; then
  msvc_link=""
  if [[ -n "${VCINSTALLDIR:-}" ]]; then
    vc_unix="$VCINSTALLDIR"
    if command -v cygpath >/dev/null 2>&1; then
      vc_unix="$(cygpath -u "$VCINSTALLDIR")"
    fi
    msvc_link="$(ls "$vc_unix"/Tools/MSVC/*/bin/Hostx64/x64/link.exe 2>/dev/null | tail -1 || true)"
  fi
  if [[ -z "$msvc_link" ]]; then
    msvc_link="$(ls /c/Program\ Files\ \(x86\)/Microsoft\ Visual\ Studio/*/BuildTools/VC/Tools/MSVC/*/bin/Hostx64/x64/link.exe 2>/dev/null | tail -1 || true)"
  fi
  if [[ -z "$msvc_link" || ! -f "$msvc_link" ]]; then
    printf 'MSVC link.exe not found. Run from a VS 2022 x64 Native Tools prompt; Git Bash /usr/bin/link cannot link PE binaries.\n' >&2
    exit 1
  fi
  export PATH="$(dirname "$msvc_link"):$PATH"
fi

triple="$(rustc --print host-tuple)"
case "$triple" in
  x86_64-pc-windows-msvc) ;;
  *)
    printf 'unsupported Windows release target: %s (want x86_64-pc-windows-msvc)\n' "$triple" >&2
    exit 1
    ;;
esac

# Node is a Windows builder dependency. Do not call `python`: the Store alias
# on a machine without Python exits 9009 and looks like a missing interpreter.
if ! command -v node >/dev/null 2>&1; then
  printf 'node is required to read client/src-tauri/tauri.conf.json\n' >&2
  exit 1
fi
version="$(node -p "require('./client/src-tauri/tauri.conf.json').version")"

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
    node -e '
const fs = require("fs");
const crypto = require("crypto");
const lines = fs.readdirSync(".").sort().flatMap((name) => {
  if (name === "SHA256SUMS") return [];
  const st = fs.statSync(name);
  if (!st.isFile()) return [];
  const digest = crypto.createHash("sha256").update(fs.readFileSync(name)).digest("hex");
  return [`${digest}  ${name}`];
});
fs.writeFileSync("SHA256SUMS", lines.join("\n") + "\n");
'
  fi
)

printf 'Windows NSIS artifacts for %s written to %s\n' "$version" "$out_dir"
ls -l "$out_dir"
