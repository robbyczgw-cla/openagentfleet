# macOS host lifecycle

OpenAgentFleet keeps the Mac as the execution host. The mobile clients connect to the
Mac; they do not run Docker, Grok Build, Pi, Claude Code, or Codex locally.

## Install the durable daemon

From the repository root on the Mac:

```sh
bash scripts/install-mac-host.sh
```

The installer builds `botd`, writes an ordinary per-user LaunchAgent under
`~/Library/LaunchAgents`, and starts it on `127.0.0.1:4317`. The generated
LaunchAgent keeps computer and harness execution disabled. Enable those gates
only after reviewing the workspace and approval policy.

## Expose it privately to the tailnet

Install and authenticate Tailscale separately, then run:

```sh
bash scripts/configure-tailnet-serve.sh
```

This configures Tailscale Serve to proxy HTTPS tailnet traffic to the local
daemon. It does not bind `botd` to `0.0.0.0`, and it does not enable Funnel.

On macOS the script accepts the normal `tailscale` CLI and the Tailscale.app binary at `/Applications/Tailscale.app/Contents/MacOS/Tailscale`. A missing shell `PATH` entry is not evidence that Tailscale is absent. Set `TAILSCALE_BIN` only when the app is installed elsewhere.

Inspect the resulting route with:

```sh
tailscale serve status --json
```

For a mobile pairing flow, set `OPENAGENTFLEET_REMOTE_TOKEN` in the LaunchAgent’s
environment and store the same value in iOS Keychain or Android Keystore. Do
not put the token in a URL or commit it.
