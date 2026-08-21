#!/usr/bin/env bash
set -euo pipefail

# Verify a Windows NSIS installer or an unpacked Tauri release directory.
# Does not launch the app or claim a working Agent Computer.

if [[ "$#" -lt 1 ]]; then
  printf 'usage: %s /path/to/OpenAgentFleet_x64-setup.exe | /path/to/release/dir\n' "$0" >&2
  exit 2
fi

target="$1"
fail() { printf 'release verification failed: %s\n' "$1" >&2; exit 1; }

if [[ -d "$target" ]]; then
  dir="$target"
else
  [[ -f "$target" ]] || fail "missing artifact $target"
  case "$target" in
    *.exe) ;;
    *) fail "expected an NSIS .exe or an unpacked release directory" ;;
  esac
  [[ "$(stat -c%s "$target" 2>/dev/null || stat -f%z "$target")" -gt 1000000 ]] || fail "installer is too small to contain sidecars"
  if [[ -f "$(dirname "$target")/SHA256SUMS" ]]; then
    (cd "$(dirname "$target")" && grep -F "$(basename "$target")" SHA256SUMS >/dev/null) || fail "SHA256SUMS does not list $(basename "$target")"
  fi
  printf 'NSIS installer present: %s\n' "$target"
  printf 'Unpack on Windows and re-run this script on the install directory to check sidecars.\n'
  exit 0
fi

need() {
  local needle="$1"
  if ! find "$dir" -iname "$needle" | grep -q .; then
    fail "directory is missing $needle"
  fi
}

need "OpenAgentFleet.exe"
need "botd.exe"
need "browser-mcp.exe"
need "uv.exe"
need "uvx.exe"
need "opencode.exe"
need "Dockerfile"

printf 'Windows release directory looks complete: %s\n' "$dir"
