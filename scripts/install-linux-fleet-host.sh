#!/usr/bin/env bash
set -euo pipefail

# Install an always-on loopback botd as a Linux user systemd unit.
# This is the Fleet Host authority. It never binds 0.0.0.0, never enables
# Funnel, and does not configure Tailscale Serve (that remains :4318 only).

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This installer targets Linux." >&2
  exit 1
fi

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_binary="$(command -v go || true)"
if [[ -z "${go_binary}" ]]; then
  echo "Go is required to build botd." >&2
  exit 1
fi

app_root="${XDG_DATA_HOME:-${HOME}/.local/share}/openagentfleet"
bin_dir="${app_root}/bin"
data_dir="${app_root}/data"
workspace_dir="${app_root}/workspace"
log_dir="${app_root}/logs"
unit_dir="${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user"
binary_path="${bin_dir}/botd"
unit_path="${unit_dir}/openagentfleet-botd.service"
runtime_dir="${project_root}/runtime/agent-computer"

install -d -m 700 "${bin_dir}" "${data_dir}" "${workspace_dir}" "${log_dir}" "${unit_dir}"
(
  cd "${project_root}"
  GOTOOLCHAIN=local GOFLAGS=-mod=readonly "${go_binary}" build \
    -trimpath -buildvcs=false -o "${binary_path}" ./cmd/botd
)

cat > "${unit_path}" <<UNIT
[Unit]
Description=OpenAgentFleet Fleet Host (loopback botd)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${binary_path} -addr 127.0.0.1:4317 -mobile-addr 127.0.0.1:4318 -data-dir ${data_dir} -workspace ${workspace_dir} -build-context ${runtime_dir}
WorkingDirectory=${project_root}
Environment=OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION=0
Environment=OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION=0
Restart=on-failure
RestartSec=3
StandardOutput=append:${log_dir}/botd.log
StandardError=append:${log_dir}/botd.error.log

[Install]
WantedBy=default.target
UNIT

systemctl --user daemon-reload
systemctl --user enable --now openagentfleet-botd.service

echo "OpenAgentFleet Fleet Host installed and started."
echo "API: 127.0.0.1:4317 (do not Serve this port)"
echo "Mobile API: 127.0.0.1:4318 (Tailscale Serve target)"
echo "Data: ${data_dir}"
echo "Workspace: ${workspace_dir}"
echo "Logs: ${log_dir}"
echo "Harness and computer execution remain disabled in the systemd unit."
echo "Do not enable Tailscale Funnel. Do not treat a computer worker as the host."
