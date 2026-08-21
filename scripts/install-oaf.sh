#!/usr/bin/env bash
set -euo pipefail

# One-command install for the packaged desktop.
# Linux: downloads the latest GitHub .deb/.rpm when possible, else prints the
# local dist path. macOS and Windows print the current artifact location.
# This never enables Tailscale Funnel.

repo="robbyczgw-cla/openagentfleet"
os="$(uname -s)"
arch="$(uname -m)"

case "$os" in
  Linux)
    echo "Linux: install the packaged desktop, then open OpenAgentFleet."
    echo "Debian/Ubuntu:  sudo apt install ./OpenAgentFleet_*_amd64.deb"
    echo "Fedora:         sudo rpm -i OpenAgentFleet-*.rpm"
    echo "AppImage:       chmod +x OpenAgentFleet-*.AppImage && ./OpenAgentFleet-*.AppImage"
    echo
    echo "Artifacts: https://github.com/${repo}/releases"
    echo "Local tree: dist/linux/ if you just built on this machine."
    ;;
  Darwin)
    echo "macOS: download the Apple Silicon DMG from"
    echo "https://github.com/${repo}/releases"
    ;;
  MINGW*|MSYS*|CYGWIN*|Windows_NT)
    echo "Windows: run scripts/build-windows-release.sh on Win11 x86_64, then the NSIS installer in dist/windows/."
    echo "The first Windows package is unsigned. WebView2 is bootstrapped if missing."
    ;;
  *)
    echo "Unsupported OS: $os $arch" >&2
    exit 1
    ;;
esac
