#!/usr/bin/env bash
set -euo pipefail

display_number="${DISPLAY:-:99}"
profile_dir=/home/agent/.chromium-profile
workspace_dir=/workspace
runtime_dir=/tmp/agent-runtime

# macOS bind mounts commonly arrive inside the Linux VM as root:root 0755,
# even when the host directory belongs to the current macOS user. Prepare the
# app-owned workspace and runtime volume, then run the actual desktop/browser/server as the
# unprivileged agent user below.
mkdir -p "${profile_dir}" "${workspace_dir}" "${runtime_dir}"
# Docker's macOS file-share may reject chmod from inside the Linux VM even
# though the host user can create files there. Do not make that a startup
# failure; the exact stale Chromium locks are removed below as root, and all
# long-lived processes still run as the unprivileged agent user.
chmod -R a+rwX "${profile_dir}" "${workspace_dir}" 2>/dev/null || true
chmod 700 "${runtime_dir}"
chown agent:agent "${runtime_dir}" 2>/dev/null || true

agent_command() {
  runuser -u agent --preserve-environment -- env \
    "HOME=/home/agent" \
    "USER=agent" \
    "LOGNAME=agent" \
    "DISPLAY=${display_number}" \
    "XDG_RUNTIME_DIR=${runtime_dir}" \
    "$@"
}

# Docker restarts the same named container without recreating its writable
# layer. If Xvfb was interrupted, its socket and lock can survive even though
# no X server is alive anymore. Remove only the display selected by this
# instance before starting a fresh virtual display; never touch another
# display number or a live process.
display_id="${display_number#:}"
if [[ "${display_id}" =~ ^[0-9]+$ ]]; then
  if [[ -e "/tmp/.X${display_id}-lock" ]] && ! kill -0 "$(cat "/tmp/.X${display_id}-lock" 2>/dev/null)" 2>/dev/null; then
    rm -f "/tmp/.X${display_id}-lock"
  fi
  if [[ -S "/tmp/.X11-unix/X${display_id}" ]] && [[ ! -f "/tmp/.X${display_id}-lock" ]]; then
    rm -f "/tmp/.X11-unix/X${display_id}"
  fi
fi

# The Chromium profile is intentionally a Docker-managed volume so browser
# sessions persist across container upgrades without crossing macOS virtiofs.
# Chromium leaves Singleton* files behind when its container is replaced; the
# named Agent Computer guarantees there is only one owner, so clear only those
# stale lock entries before starting it.
rm -f "${profile_dir}/SingletonCookie" "${profile_dir}/SingletonLock" "${profile_dir}/SingletonSocket" 2>/dev/null || true

# Docker can replace this named computer while Chromium is still winding down.
# That is not a user crash: its durable profile is deliberately reused by the
# next incarnation. Mark only Chromium's stale crash marker as clean before
# starting a new process, so it does not cover the desktop with a misleading
# "Restore pages?" prompt. Cookies, tabs and all other profile data stay intact.
preferences_file="${profile_dir}/Default/Preferences"
if [[ -f "${preferences_file}" ]]; then
  python3 - "${preferences_file}" <<'PY'
import json
import os
import sys

path = sys.argv[1]
try:
    with open(path, "r", encoding="utf-8") as source:
        preferences = json.load(source)
    profile = preferences.get("profile")
    if not isinstance(profile, dict) or profile.get("exit_type") != "Crashed":
        raise SystemExit(0)
    profile["exit_type"] = "Normal"
    temporary = path + ".openagentfleet-tmp"
    with open(temporary, "w", encoding="utf-8") as destination:
        json.dump(preferences, destination, separators=(",", ":"))
    os.replace(temporary, path)
except (OSError, ValueError, TypeError):
    # A browser profile must never stop the isolated computer from starting.
    pass
PY
fi

Xvfb "${display_number}" -screen 0 1440x900x24 -ac -nolisten tcp >/tmp/xvfb.log 2>&1 &
xvfb_pid=$!

cleanup() {
  kill "${computer_pid:-}" "${chromium_pid:-}" "${desktop_pid:-}" "${xvfb_pid:-}" 2>/dev/null || true
}
trap cleanup EXIT
trap 'cleanup; exit 143' INT TERM

for _ in $(seq 1 50); do
  if ! kill -0 "${xvfb_pid}" 2>/dev/null; then
    echo "Xvfb exited before the Agent Computer was ready" >&2
    exit 1
  fi
  if [[ -S "/tmp/.X11-unix/X${display_number#:}" ]]; then
    break
  fi
  sleep 0.1
done

# A real desktop is intentionally part of the computer, not just a hidden
# browser process. Xfce gives the agent and the human a persistent terminal,
# file-manager and application surface inside the isolated Linux runtime.
eval "$(agent_command dbus-launch --sh-syntax)"
agent_command startxfce4 >/tmp/xfce.log 2>&1 &
desktop_pid=$!

chromium_binary="${CHROMIUM_BINARY:-/usr/local/bin/chromium}"
if [[ ! -x "${chromium_binary}" ]]; then
  echo "Chromium binary is missing: ${chromium_binary}" >&2
  exit 1
fi

agent_command "${chromium_binary}" \
  --no-sandbox \
  --test-type \
  --disable-dev-shm-usage \
  --disable-gpu \
  --no-first-run \
  --no-default-browser-check \
  --password-store=basic \
  --remote-debugging-address=127.0.0.1 \
  --remote-debugging-port=9222 \
  --user-data-dir="${profile_dir}" \
  --window-size=1440,900 \
  about:blank >/tmp/chromium.log 2>&1 &
chromium_pid=$!

agent_command node /opt/agent-computer/computer-server.mjs &
computer_pid=$!
wait "${computer_pid}"
