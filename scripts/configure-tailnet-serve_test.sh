#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=configure-tailnet-serve.sh
# shellcheck disable=SC1091
source "${script_dir}/configure-tailnet-serve.sh"

assert_equals() {
  local expected="$1"
  local actual="$2"
  local label="$3"

  if [[ "${expected}" != "${actual}" ]]; then
    echo "FAIL: ${label}: expected '${expected}', got '${actual}'" >&2
    exit 1
  fi
}

assert_success() {
  local label="$1"
  shift
  if ! "$@" >/dev/null 2>&1; then
    echo "FAIL: ${label}" >&2
    exit 1
  fi
}

assert_failure() {
  local label="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    echo "FAIL: ${label}" >&2
    exit 1
  fi
}

main() {
  local ready_status='{"BackendState":"Running","Self":{"DNSName":"mac.tailnet.ts.net"}}'
  local empty_serve='{"TCP":{},"Web":{}}'
  local bare_empty_serve='{}'
  local configured_serve='{"TCP":{},"Web":{"mac.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:4318"}}}}}'
  local other_serve='{"TCP":{},"Web":{"mac.tailnet.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:4317"}}}}}'
  local funnel_serve='{"TCP":{},"Web":{},"AllowFunnel":true}'

  assert_success "accepts default private loopback upstream" validate_upstream "http://127.0.0.1:4318"
  assert_failure "rejects non-loopback upstream" validate_upstream "https://example.com"
  assert_failure "rejects invalid upstream port" validate_upstream "http://127.0.0.1:99999"
  assert_success "accepts running Tailscale with Tailnet DNS" tailscale_status_is_ready "${ready_status}"
  assert_failure "rejects missing Tailnet DNS" tailscale_status_is_ready '{"BackendState":"Running","Self":{}}'
  assert_failure "rejects unavailable Tailscale" tailscale_status_is_ready '{"BackendState":"Stopped","Self":{}}'

  assert_equals "empty" "$(serve_state_for_upstream "${empty_serve}" "http://127.0.0.1:4318")" "empty Serve state"
  assert_equals "empty" "$(serve_state_for_upstream "${bare_empty_serve}" "http://127.0.0.1:4318")" "bare empty Serve state"
  assert_equals "already-configured" "$(serve_state_for_upstream "${configured_serve}" "http://127.0.0.1:4318")" "matching Serve state"
  assert_equals "different-or-unknown" "$(serve_state_for_upstream "${other_serve}" "http://127.0.0.1:4318")" "different Serve state"
  assert_equals "funnel-enabled" "$(serve_state_for_upstream "${funnel_serve}" "http://127.0.0.1:4318")" "Funnel state"

  echo "configure-tailnet-serve tests passed"
}

main "$@"
