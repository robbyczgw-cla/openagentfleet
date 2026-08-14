#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
user_name="$(id -un)"
user_home="$(/usr/bin/dscl . -read "/Users/${user_name}" NFSHomeDirectory | /usr/bin/awk '{print $2}')"
app_root="${user_home}/Library/Application Support/OpenAgentFleet"
bin_dir="${app_root}/bin"
data_dir="${app_root}/data"
workspace_dir="${app_root}/workspace"
log_dir="${user_home}/Library/Logs/OpenAgentFleet"
launch_agents_dir="${user_home}/Library/LaunchAgents"
binary_path="${bin_dir}/botd"
plist_path="${launch_agents_dir}/com.openagentfleet.botd.plist"
template_path="${project_root}/deploy/com.openagentfleet.botd.plist.template"
go_binary="$(command -v go || true)"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "This installer targets macOS." >&2
  exit 1
fi
if [[ -z "${go_binary}" ]]; then
  echo "Go is required to build botd." >&2
  exit 1
fi

/usr/bin/install -d -m 700 "${bin_dir}" "${data_dir}" "${workspace_dir}" "${log_dir}" "${launch_agents_dir}"
(
  cd "${project_root}"
  GOTOOLCHAIN=local GOFLAGS=-mod=readonly "${go_binary}" build \
    -trimpath -buildvcs=false -o "${binary_path}" ./cmd/botd
)

openagentfleet_path="${user_home}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/Applications/Tailscale.app/Contents/MacOS:/usr/bin:/bin"
/usr/bin/sed \
  -e "s|__OPENAGENTFLEET_BIN__|${binary_path}|g" \
  -e "s|__OPENAGENTFLEET_DATA__|${data_dir}|g" \
  -e "s|__OPENAGENTFLEET_WORKSPACE__|${workspace_dir}|g" \
  -e "s|__OPENAGENTFLEET_RUNTIME__|${project_root}/runtime/agent-computer|g" \
  -e "s|__OPENAGENTFLEET_PROJECT__|${project_root}|g" \
  -e "s|__OPENAGENTFLEET_PATH__|${openagentfleet_path}|g" \
  -e "s|__OPENAGENTFLEET_LOG_DIR__|${log_dir}|g" \
  "${template_path}" > "${plist_path}"

launch_domain="gui/$(id -u)"
if /bin/launchctl print "${launch_domain}/com.openagentfleet.botd" >/dev/null 2>&1; then
  /bin/launchctl kickstart -k "${launch_domain}/com.openagentfleet.botd"
else
  /bin/launchctl bootstrap "${launch_domain}" "${plist_path}"
fi

echo "OpenAgentFleet botd installed and loaded."
echo "Data: ${data_dir}"
echo "Workspace: ${workspace_dir}"
echo "Logs: ${log_dir}"
echo "Harness and computer execution remain disabled in the LaunchAgent template."
