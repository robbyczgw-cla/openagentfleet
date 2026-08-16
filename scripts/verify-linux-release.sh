#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -lt 1 ]]; then
  printf 'usage: %s /path/to/OpenAgentFleet.deb [/path/to.rpm] [/path/to.AppImage]\n' "$0" >&2
  exit 2
fi

deb_path=""
rpm_path=""
appimage_path=""
for artifact in "$@"; do
  case "$artifact" in
    *.deb) deb_path="$artifact" ;;
    *.rpm) rpm_path="$artifact" ;;
    *.AppImage) appimage_path="$artifact" ;;
    *)
      printf 'unsupported Linux artifact: %s\n' "$artifact" >&2
      exit 1
      ;;
  esac
done

if [[ -z "$deb_path" ]]; then
  printf 'a .deb package is required for the Linux release gate\n' >&2
  exit 1
fi
[[ -f "$deb_path" ]] || { printf 'missing deb: %s\n' "$deb_path" >&2; exit 1; }

if ! command -v dpkg-deb >/dev/null 2>&1; then
  printf 'dpkg-deb is required to verify the Debian package\n' >&2
  exit 1
fi

control="$(dpkg-deb -I "$deb_path")"
if ! grep -qiE 'Package:[[:space:]]*(openagentfleet|open-agent-fleet)' <<<"$control"; then
  printf 'release verification failed: unexpected Debian package name\n%s\n' "$control" >&2
  exit 1
fi
if ! grep -qiE 'Recommends:.*docker' <<<"$control"; then
  printf 'release verification failed: Debian package must recommend Docker\n%s\n' "$control" >&2
  exit 1
fi

contents="$(dpkg-deb -c "$deb_path")"
for needle in \
  botd \
  browser-mcp \
  '.desktop' \
  'agent-computer/Dockerfile'
do
  if ! grep -q "$needle" <<<"$contents"; then
    printf 'release verification failed: deb is missing %s\n' "$needle" >&2
    exit 1
  fi
done
if ! grep -Eq 'usr/bin/[^ ]*openagentfleet|usr/bin/OpenAgentFleet' <<<"$contents"; then
  printf 'release verification failed: deb is missing the OpenAgentFleet binary\n' >&2
  exit 1
fi

if [[ -n "$rpm_path" ]]; then
  [[ -f "$rpm_path" ]] || { printf 'missing rpm: %s\n' "$rpm_path" >&2; exit 1; }
  if ! command -v rpm >/dev/null 2>&1; then
    printf 'rpm is required to verify the Fedora package\n' >&2
    exit 1
  fi
  rpm_meta="$(rpm -qip "$rpm_path")"
  if ! grep -qiE 'Name[[:space:]]*:[[:space:]]*(openagentfleet|open-agent-fleet)' <<<"$rpm_meta"; then
    printf 'release verification failed: unexpected RPM name\n%s\n' "$rpm_meta" >&2
    exit 1
  fi
  rpm_files="$(rpm -qlp "$rpm_path")"
  for needle in \
    '/usr/bin/OpenAgentFleet' \
    '/usr/bin/botd' \
    '/usr/bin/browser-mcp' \
    'OpenAgentFleet.desktop' \
    'agent-computer/Dockerfile'
  do
    if ! grep -q "$needle" <<<"$rpm_files"; then
      printf 'release verification failed: rpm is missing %s\n' "$needle" >&2
      exit 1
    fi
  done
fi

if [[ -n "$appimage_path" ]]; then
  [[ -f "$appimage_path" && -x "$appimage_path" ]] || {
    printf 'missing or non-executable AppImage: %s\n' "$appimage_path" >&2
    exit 1
  }
  if ! grep -aq 'AppImage' "$appimage_path"; then
    printf 'release verification failed: file is not an AppImage\n' >&2
    exit 1
  fi
fi

printf 'Linux release verification passed\n'
printf 'deb: %s\n' "$deb_path"
printf 'deb_sha256: %s\n' "$(sha256sum "$deb_path" | awk '{print $1}')"
if [[ -n "$rpm_path" ]]; then
  printf 'rpm: %s\n' "$rpm_path"
fi
if [[ -n "$appimage_path" ]]; then
  printf 'appimage: %s\n' "$appimage_path"
fi
