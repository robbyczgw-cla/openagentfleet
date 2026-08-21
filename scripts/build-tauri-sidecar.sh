#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
target_triple="$(rustc --print host-tuple)"
target_os=""
go_arch=""
expected_arch=""
exe_suffix=""

case "$target_triple" in
  aarch64-apple-darwin)
    target_os="darwin"
    go_arch="arm64"
    expected_arch="arm64"
    ;;
  x86_64-apple-darwin)
    target_os="darwin"
    go_arch="amd64"
    expected_arch="x86_64"
    ;;
  aarch64-unknown-linux-gnu)
    target_os="linux"
    go_arch="arm64"
    expected_arch="aarch64"
    ;;
  x86_64-unknown-linux-gnu)
    target_os="linux"
    go_arch="amd64"
    expected_arch="x86-64"
    ;;
  x86_64-pc-windows-msvc)
    target_os="windows"
    go_arch="amd64"
    expected_arch="x86-64"
    exe_suffix=".exe"
    ;;
  aarch64-pc-windows-msvc)
    target_os="windows"
    go_arch="arm64"
    expected_arch="ARM64"
    exe_suffix=".exe"
    ;;
  *)
    printf 'OpenAgentFleet currently packages desktop sidecars for macOS, GNU/Linux, and Windows MSVC (got %s).\n' "$target_triple" >&2
    exit 1
    ;;
esac

# WebSearchPlus and its optional Hound provider run through exact PyPI pins.
# Bundle uv's two launchers so a fresh Mac does not need a pre-existing Python
# or uv install. Package downloads still happen only after the user enables the
# optional connector; this build step never installs provider packages.
uv_binary="${OPENAGENTFLEET_UV_BINARY:-$(command -v uv || command -v uv.exe || true)}"
uvx_binary="${OPENAGENTFLEET_UVX_BINARY:-$(command -v uvx || command -v uvx.exe || true)}"
if [[ -z "$uv_binary" || ! -x "$uv_binary" || -z "$uvx_binary" || ! -x "$uvx_binary" ]]; then
  printf 'uv and uvx are required to package the optional WebSearchPlus runtime.\n' >&2
  printf 'Install uv or set OPENAGENTFLEET_UV_BINARY and OPENAGENTFLEET_UVX_BINARY.\n' >&2
  exit 1
fi

opencode_version_pin="1.18.10"
opencode_binary="${OPENAGENTFLEET_OPENCODE_BINARY:-$(command -v opencode || command -v opencode.exe || true)}"
if [[ -z "$opencode_binary" || ! -x "$opencode_binary" ]]; then
  printf 'OpenCode %s is required to package the bundled worker, but no executable was found.\n' "$opencode_version_pin" >&2
  printf 'Install that exact version or set OPENAGENTFLEET_OPENCODE_BINARY to its executable.\n' >&2
  exit 1
fi
if ! opencode_version="$("$opencode_binary" --version 2>/dev/null)"; then
  printf 'Could not run OpenCode at %s to verify the required version %s.\n' "$opencode_binary" "$opencode_version_pin" >&2
  exit 1
fi
if [[ "$opencode_version" != "$opencode_version_pin" ]]; then
  printf 'OpenCode version mismatch: required %s, found %s at %s.\n' \
    "$opencode_version_pin" "$opencode_version" "$opencode_binary" >&2
  printf 'Install the required version or set OPENAGENTFLEET_OPENCODE_BINARY to it; do not bundle an unpinned release.\n' >&2
  exit 1
fi

check_binary_arch() {
  local tool="$1"
  local tool_arches
  case "$target_os" in
    darwin)
      tool_arches="$(lipo -archs "$tool" 2>/dev/null || true)"
      if [[ " $tool_arches " != *" $expected_arch "* ]]; then
        printf '%s does not contain the required %s architecture.\n' "$tool" "$expected_arch" >&2
        exit 1
      fi
      ;;
    linux)
      tool_arches="$(file -b "$tool" 2>/dev/null || true)"
      if [[ "$tool_arches" != *"$expected_arch"* ]]; then
        printf '%s is not a compatible GNU/Linux %s executable (%s).\n' "$tool" "$expected_arch" "$tool_arches" >&2
        exit 1
      fi
      ;;
    windows)
      if ! command -v file >/dev/null 2>&1; then
        return 0
      fi
      tool_arches="$(file -b "$tool" 2>/dev/null || true)"
      if [[ "$tool_arches" != *"PE32+"* || "$tool_arches" != *"$expected_arch"* ]]; then
        printf '%s is not a compatible Windows PE %s executable (%s).\n' "$tool" "$expected_arch" "$tool_arches" >&2
        exit 1
      fi
      ;;
  esac
}

for tool in "$uv_binary" "$uvx_binary" "$opencode_binary"; do
  check_binary_arch "$tool"
done

sidecar_dir="$repo_root/client/src-tauri/binaries"
sidecar_path="$sidecar_dir/botd-${target_triple}${exe_suffix}"
mkdir -p "$sidecar_dir"

GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOOS="$target_os" GOARCH="$go_arch" \
  go build -trimpath -buildvcs=false -o "$sidecar_path" "$repo_root/cmd/botd"
chmod 755 "$sidecar_path" || true
printf 'Built botd sidecar: %s\n' "$sidecar_path"

browser_mcp_path="$sidecar_dir/browser-mcp-${target_triple}${exe_suffix}"
GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOOS="$target_os" GOARCH="$go_arch" \
  go build -trimpath -buildvcs=false -o "$browser_mcp_path" "$repo_root/cmd/openagentfleet-browser-mcp"
chmod 755 "$browser_mcp_path" || true
printf 'Built Agent Computer MCP sidecar: %s\n' "$browser_mcp_path"

collab_mcp_path="$sidecar_dir/collaboration-mcp-${target_triple}${exe_suffix}"
GOTOOLCHAIN=local GOFLAGS=-mod=readonly GOOS="$target_os" GOARCH="$go_arch" \
  go build -trimpath -buildvcs=false -o "$collab_mcp_path" "$repo_root/cmd/openagentfleet-collaboration-mcp"
chmod 755 "$collab_mcp_path" || true
printf 'Built Agent collaboration MCP sidecar: %s\n' "$collab_mcp_path"

uv_path="$sidecar_dir/uv-${target_triple}${exe_suffix}"
uvx_path="$sidecar_dir/uvx-${target_triple}${exe_suffix}"
opencode_path="$sidecar_dir/opencode-${target_triple}${exe_suffix}"
cp "$uv_binary" "$uv_path"
cp "$uvx_binary" "$uvx_path"
# Homebrew exposes OpenCode via a symlink; package the executable itself, not
# a link back into the build machine's Cellar.
cp -L "$opencode_binary" "$opencode_path"
chmod 755 "$uv_path" "$uvx_path" "$opencode_path" || true
printf 'Bundled WebSearchPlus launchers: %s, %s\n' "$uv_path" "$uvx_path"
printf 'Bundled OpenCode %s: %s\n' "$opencode_version_pin" "$opencode_path"
