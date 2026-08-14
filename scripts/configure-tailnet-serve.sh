#!/usr/bin/env bash
set -euo pipefail

# Configure the private OpenAgentFleet mobile API behind Tailscale Serve.
# This script never enables Funnel and deliberately refuses ambiguous Serve state.

readonly DEFAULT_REMOTE_UPSTREAM="http://127.0.0.1:4318"

resolve_tailscale_bin() {
  local candidate="${TAILSCALE_BIN:-}"

  if [[ -z "${candidate}" ]] && command -v tailscale >/dev/null 2>&1; then
    candidate="$(command -v tailscale)"
  fi
  if [[ -z "${candidate}" ]] && [[ -x "/Applications/Tailscale.app/Contents/MacOS/Tailscale" ]]; then
    candidate="/Applications/Tailscale.app/Contents/MacOS/Tailscale"
  fi

  if [[ -z "${candidate}" ]] || [[ ! -x "${candidate}" ]]; then
    echo "Tailscale was not found on PATH or at /Applications/Tailscale.app/Contents/MacOS/Tailscale." >&2
    return 1
  fi

  printf '%s\n' "${candidate}"
}

validate_upstream() {
  local upstream="$1"
  local port

  if [[ ! "${upstream}" =~ ^http://127\.0\.0\.1:([0-9]{1,5})$ ]]; then
    echo "OFB_REMOTE_UPSTREAM must be an http://127.0.0.1:<port> loopback URL." >&2
    return 1
  fi

  port="${BASH_REMATCH[1]}"
  if (( 10#${port} < 1 || 10#${port} > 65535 )); then
    echo "OFB_REMOTE_UPSTREAM has an invalid port." >&2
    return 1
  fi
}

tailscale_status_is_ready() {
  local status_json="$1"
  local compact

  compact="$(printf '%s' "${status_json}" | tr -d '[:space:]')"
  if [[ ! "${compact}" =~ \"BackendState\":\"Running\" ]]; then
    echo "Tailscale is not authenticated or its local daemon is unavailable. Run 'tailscale up' and sign in first." >&2
    return 1
  fi
  if [[ ! "${compact}" =~ \"Self\":\{[^}]*\"DNSName\":\"[^\"]+\" ]]; then
    echo "Tailscale is running, but no Tailnet HTTPS DNS name is available. Verify MagicDNS and this machine's Tailnet registration." >&2
    return 1
  fi
}

serve_status_is_empty() {
  local serve_json="$1"
  local compact

  compact="$(printf '%s' "${serve_json}" | tr -d '[:space:]')"
  [[ "${compact}" == '{}' ]] || {
    [[ "${compact}" == *'"Web":{}'* ]] && [[ "${compact}" == *'"TCP":{}'* ]]
  }
}

serve_state_for_upstream() {
  local serve_json="$1"
  local upstream="$2"
  local compact

  compact="$(printf '%s' "${serve_json}" | tr -d '[:space:]')"
  if [[ "${compact}" == *'"AllowFunnel":true'* ]]; then
    printf '%s\n' "funnel-enabled"
  elif [[ "${serve_json}" == *"\"${upstream}\""* ]]; then
    printf '%s\n' "already-configured"
  elif serve_status_is_empty "${serve_json}"; then
    printf '%s\n' "empty"
  else
    printf '%s\n' "different-or-unknown"
  fi
}

main() {
  local tailscale_bin
  local upstream="${OFB_REMOTE_UPSTREAM:-${DEFAULT_REMOTE_UPSTREAM}}"
  local status_json
  local serve_json
  local serve_state

  validate_upstream "${upstream}"
  tailscale_bin="$(resolve_tailscale_bin)"

  if ! status_json="$("${tailscale_bin}" status --json 2>/dev/null)"; then
    echo "Could not inspect Tailscale status. No Serve configuration was changed." >&2
    return 1
  fi
  tailscale_status_is_ready "${status_json}"

  if ! serve_json="$("${tailscale_bin}" serve status --json 2>/dev/null)"; then
    echo "Could not inspect the existing Tailscale Serve state. No Serve configuration was changed." >&2
    return 1
  fi
  serve_state="$(serve_state_for_upstream "${serve_json}" "${upstream}")"

  case "${serve_state}" in
    funnel-enabled)
      echo "Tailscale Funnel is already enabled in the current Serve configuration. Refusing to change it; disable Funnel explicitly before configuring OpenAgentFleet." >&2
      return 1
      ;;
    already-configured)
      echo "OpenAgentFleet Tailscale Serve already targets ${upstream}; no change was made."
      return 0
      ;;
    different-or-unknown)
      if [[ "${OFB_REPLACE_TAILSCALE_SERVE:-}" != "1" ]]; then
        echo "An existing Tailscale Serve configuration is present or could not be classified. Refusing to overwrite it. Set OFB_REPLACE_TAILSCALE_SERVE=1 only after reviewing it." >&2
        return 1
      fi
      echo "Replacing the existing Tailscale Serve configuration with OpenAgentFleet's private remote API target."
      ;;
    empty)
      echo "No existing Tailscale Serve configuration was found; configuring OpenAgentFleet's private remote API target."
      ;;
    *)
      echo "Unexpected internal Serve state. No Serve configuration was changed." >&2
      return 1
      ;;
  esac

  if [[ "${OFB_TAILSCALE_SERVE_DRY_RUN:-}" == "1" ]]; then
    echo "Dry run: would run Tailscale Serve HTTPS on port 443 to ${upstream}."
    return 0
  fi

  "${tailscale_bin}" serve --bg --https=443 "${upstream}"
  echo "OpenAgentFleet remote API is now privately served over Tailnet HTTPS on port 443. Funnel was not enabled."
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
