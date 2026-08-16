#!/usr/bin/env bash
# Build Fedora-class RPM and a portable AppImage from a finished .deb payload.
# Tauri 2.11's in-process rpm-rs bundler hangs after "Bundling …rpm" on this
# CLI; AppImage is produced here so the release gate does not depend on that.
set -euo pipefail

if [[ "$#" -lt 3 ]]; then
  printf 'usage: %s /path/to/OpenAgentFleet.deb /output/dir <version>\n' "$0" >&2
  exit 2
fi

deb_path="$(readlink -f "$1")"
out_dir="$(mkdir -p "$2" && readlink -f "$2")"
version="$3"
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/oaf-linux-pkg.XXXXXX")"
cleanup() { rm -rf "$work"; }
trap cleanup EXIT

if [[ ! -f "$deb_path" ]]; then
  printf 'missing deb: %s\n' "$deb_path" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64) rpm_arch="x86_64"; appimage_arch="x86_64"; appimage_suffix="amd64" ;;
  aarch64) rpm_arch="aarch64"; appimage_arch="aarch64"; appimage_suffix="arm64" ;;
  *)
    printf 'unsupported architecture: %s\n' "$(uname -m)" >&2
    exit 1
    ;;
esac

payload="$work/payload"
mkdir -p "$payload"
dpkg-deb -x "$deb_path" "$payload"

if [[ ! -x "$payload/usr/bin/OpenAgentFleet" ]]; then
  printf 'deb payload is missing /usr/bin/OpenAgentFleet\n' >&2
  exit 1
fi

# --- RPM via rpmbuild (native, not Tauri rpm-rs) ---
rpm_root="$work/rpm"
mkdir -p "$rpm_root"/{BUILD,RPMS,SOURCES,SPECS,BUILDROOT}
spec="$rpm_root/SPECS/open-agent-fleet.spec"
cat >"$spec" <<EOF
Name:           open-agent-fleet
Version:        ${version}
Release:        1
Summary:        Local-first AI agents with an isolated Linux computer
License:        Apache-2.0
URL:            https://openagentfleet.xyz
BuildArch:      ${rpm_arch}
AutoReq:        no
AutoProv:       no

Requires:       webkit2gtk4.1
Requires:       gtk3
Requires:       libayatana-appindicator-gtk3
Recommends:     docker
Recommends:     moby-engine

%description
OpenAgentFleet runs Grok Build, Codex App Server, or OpenCode in a local
workspace and gives approved runs an isolated Linux desktop with Chromium,
Terminal and Files. The Linux package starts the local Go controller
automatically. Docker Engine is recommended for the Agent Computer and is
not started until Computer View or an approved desktop task needs it.

%prep

%build

%install
rm -rf %{buildroot}
mkdir -p %{buildroot}
cp -a ${payload}/. %{buildroot}/

%post
if [ -x /usr/bin/update-desktop-database ]; then
  /usr/bin/update-desktop-database -q /usr/share/applications || :
fi
echo "OpenAgentFleet is installed. The Agent Computer needs a local Docker Engine."
echo "  sudo dnf install -y docker   # or: sudo zypper install -y docker"
echo "Then add your user to the docker group and start a new session:"
echo "  sudo usermod -aG docker \"\$USER\""
echo "  sudo systemctl enable --now docker"
echo "Opening the app does not start a container; Computer View does that on demand."

%files
%defattr(-,root,root,-)
/usr/bin/OpenAgentFleet
/usr/bin/botd
/usr/bin/browser-mcp
/usr/bin/opencode
/usr/bin/uv
/usr/bin/uvx
/usr/lib/OpenAgentFleet
/usr/share/applications/OpenAgentFleet.desktop
/usr/share/icons/hicolor
EOF

rpmbuild \
  --define "_topdir $rpm_root" \
  --define "_build_id_links none" \
  --define "_binary_payload w2.xzdio" \
  --nocheck \
  -bb "$spec"

rpm_built="$(find "$rpm_root/RPMS" -name '*.rpm' -type f | head -n 1)"
if [[ -z "$rpm_built" ]]; then
  printf 'rpmbuild produced no RPM\n' >&2
  exit 1
fi
rpm_name="OpenAgentFleet-${version}-1.${rpm_arch}.rpm"
cp -a "$rpm_built" "$out_dir/$rpm_name"
printf 'Built RPM: %s\n' "$out_dir/$rpm_name"

# --- AppImage from the same payload ---
appdir="$work/OpenAgentFleet.AppDir"
mkdir -p "$appdir"
cp -a "$payload/." "$appdir/"
cp "$appdir/usr/share/applications/OpenAgentFleet.desktop" "$appdir/OpenAgentFleet.desktop"
if [[ -f "$appdir/usr/share/icons/hicolor/128x128/apps/OpenAgentFleet.png" ]]; then
  cp "$appdir/usr/share/icons/hicolor/128x128/apps/OpenAgentFleet.png" "$appdir/OpenAgentFleet.png"
elif [[ -f "$repo_root/client/src-tauri/icons/128x128.png" ]]; then
  cp "$repo_root/client/src-tauri/icons/128x128.png" "$appdir/OpenAgentFleet.png"
fi
# AppImage desktop files must use a path-less Exec that matches AppRun.
sed -i 's/^Exec=.*/Exec=OpenAgentFleet/' "$appdir/OpenAgentFleet.desktop"
cat >"$appdir/AppRun" <<'EOF'
#!/bin/sh
set -eu
HERE="$(dirname "$(readlink -f "$0")")"
export APPDIR="$HERE"
export PATH="$HERE/usr/bin:${PATH:-}"
exec "$HERE/usr/bin/OpenAgentFleet" "$@"
EOF
chmod 755 "$appdir/AppRun" "$appdir/usr/bin/"*

tool_dir="${OPENAGENTFLEET_APPIMAGE_TOOL_DIR:-$repo_root/.cache/linux-packaging}"
mkdir -p "$tool_dir"
appimagetool="$tool_dir/appimagetool-${appimage_arch}.AppImage"
if [[ ! -x "$appimagetool" ]]; then
  url="https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-${appimage_arch}.AppImage"
  printf 'Downloading appimagetool from %s\n' "$url"
  curl -fsSL "$url" -o "$appimagetool"
  chmod 755 "$appimagetool"
fi

appimage_name="OpenAgentFleet_${version}_${appimage_suffix}.AppImage"
export ARCH="$appimage_arch"
export APPIMAGE_EXTRACT_AND_RUN=1
"$appimagetool" --no-appstream "$appdir" "$out_dir/$appimage_name"
chmod 755 "$out_dir/$appimage_name"
printf 'Built AppImage: %s\n' "$out_dir/$appimage_name"
