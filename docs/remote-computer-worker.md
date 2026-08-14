# Remote Agent Computer worker

## Short version

OpenAgentFleet uses a local **Colima + Docker** Agent Computer by default. You
can optionally move only that computer to another Mac or Linux machine and
reach it over your private Tailscale network.

This is an advanced setup. It is not needed for the normal first run. Leave
the **Remote worker URL** empty to keep the local computer.

~~~text
Controller Mac                         Worker Mac / Linux
----------------                       -----------------
OpenAgentFleet app                      agent-computer-worker
botd                                   Docker-compatible runtime
chat, memory, runs, approvals           openagentfleet-agent-computer:dev
Grok/Codex/OpenCode                     Chromium + desktop + workspace
        |                                      |
        +------ HTTPS over Tailscale ---------+
~~~

The controller still owns the agent and its conversation. The worker owns the
browser and desktop only.

## What is implemented

The remote path is implemented by these current code paths:

- [`cmd/agent-computer-worker`](../cmd/agent-computer-worker) runs the worker
  process on the second machine.
- [`internal/compute/worker.go`](../internal/compute/worker.go) exposes the
  authenticated worker HTTP contract.
- [`internal/compute/docker.go`](../internal/compute/docker.go) switches the
  controller between local Docker/Colima and a remote worker.
- [`cmd/botd/main.go`](../cmd/botd/main.go) reads the remote URL and token and
  routes computer requests without using a remote Docker socket.

The worker does not run a second OpenAgentFleet controller. It has no chat
database, memory store, harness OAuth, MCP administration, or model session.

## What runs on each machine

### Controller

The controller is the Mac running the OpenAgentFleet app and `botd`. It owns:

- agents, the default conversation, memory, runs, events, and approvals;
- the selected engine/harness and its model login;
- MCP/plugin configuration and other optional settings;
- the remote worker URL and the worker bearer token in the launch environment;
- requests for computer status, screenshots, browser actions, and desktop
  actions.

The agent's LLM work still runs through the controller-side harness. Moving the
computer does not move model inference or harness credentials to the worker.

### Worker

The second machine runs `agent-computer-worker`. It owns:

- the Docker-compatible runtime;
- the `openagentfleet-agent-computer:dev` image built from
  [`runtime/agent-computer`](../runtime/agent-computer);
- Chromium, the Xfce desktop, Terminal, Files, and the worker workspace;
- the authenticated status, frame, lifecycle, and action endpoints.

The worker exposes no Docker socket to the controller. The controller makes
HTTPS requests; the worker performs Docker operations locally.

## First-run behaviour

Nothing changes for a normal installation:

1. The remote URL is empty.
2. The Agent Computer runtime is **Colima + Docker**.
3. Colima starts lazily when the Agent Computer is requested.
4. The first-run flow does not ask for a second machine, Tailscale, or a
   worker token.

To use a remote worker later, open Settings and expand **Advanced computer
routing**. This keeps the normal chat and first-run experience simple.

## Setup from a cloned repository

Use the same repository revision on both machines.

~~~sh
git clone <your-openagentfleet-repository-url> openagentfleet
cd openagentfleet
go test ./...
~~~

### 1. Prepare the worker machine

The worker needs Go, Tailscale, and a working Docker-compatible runtime.

On a Mac, the recommended runtime is Colima:

~~~sh
brew install colima docker
~~~

The worker can start the dedicated `openagentfleet` Colima profile when the
controller first requests a computer. To select it explicitly, use
`OPENAGENTFLEET_COMPUTER_WORKER_RUNTIME=colima`. On Linux, install Docker and
make sure `docker info` works for the user that will run the worker; use the
default `auto` runtime.

Create one high-entropy worker token and transfer it to the worker and
controller through a password manager or another private channel. Do not put
it in a URL:

~~~sh
WORKER_TOKEN="$(openssl rand -hex 32)"
~~~

The token must be at least 32 characters. Keep it private and do not commit it
to the repository.

### 2. Start the worker

Run this from the cloned repository on the second machine. The listener stays
on loopback; Tailscale Serve is the private network edge.

~~~sh
export OPENAGENTFLEET_COMPUTER_WORKER_TOKEN="$WORKER_TOKEN"

go run ./cmd/agent-computer-worker \
  --addr 127.0.0.1:9323 \
  --runtime auto \
  --data-dir "$HOME/.openagentfleet-computer"
~~~

For a Mac worker using the dedicated Colima profile:

~~~sh
OPENAGENTFLEET_COMPUTER_WORKER_RUNTIME=colima \
OPENAGENTFLEET_COMPUTER_WORKER_TOKEN="$WORKER_TOKEN" \
go run ./cmd/agent-computer-worker \
  --addr 127.0.0.1:9323 \
  --runtime colima \
  --data-dir "$HOME/.openagentfleet-computer"
~~~

The worker command also supports these current options:

~~~text
OPENAGENTFLEET_COMPUTER_WORKER_ADDR
OPENAGENTFLEET_COMPUTER_WORKER_TOKEN
OPENAGENTFLEET_COMPUTER_WORKER_DATA_DIR
OPENAGENTFLEET_COMPUTER_WORKER_BUILD_CONTEXT
OPENAGENTFLEET_COMPUTER_WORKER_RUNTIME
OPENAGENTFLEET_COMPUTER_WORKER_PORT
OPENAGENTFLEET_COMPUTER_WORKER_CONTAINER
~~~

The default worker API is `127.0.0.1:9323`. Keep it on loopback and do not
publish port `9323` directly to the internet.

### 3. Put the worker behind Tailscale

On the worker machine:

~~~sh
tailscale up
tailscale status
tailscale serve --bg --https=443 http://127.0.0.1:9323
tailscale serve status --json
~~~

Use the worker's Tailnet HTTPS hostname as the controller URL, for example:

~~~text
https://worker-name.your-tailnet.ts.net
~~~

Do not add `/status` to the saved URL. OpenAgentFleet appends the worker API
paths itself. Do not enable Tailscale Funnel for this service.

If the worker already has another Tailscale Serve configuration, inspect it
before changing it. The repository helper
[`scripts/configure-tailnet-serve.sh`](../scripts/configure-tailnet-serve.sh)
is for the **mobile API on port 4318**, not for a computer worker on port 9323.

### 4. Point the controller at the worker

The simple UI path is:

1. Open Settings.
2. Find **Advanced computer routing**.
3. Enter the worker's base HTTPS URL in **Remote worker URL**.
4. Start or restart the controller with the worker token available.

For a cloned-repository controller, the complete environment looks like this:

~~~sh
export OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION=1
export OPENAGENTFLEET_COMPUTER_REMOTE_URL="https://worker-name.your-tailnet.ts.net"
export OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN="$WORKER_TOKEN"

go run ./cmd/botd
~~~

`OPENAGENTFLEET_COMPUTER_REMOTE_URL` overrides the saved `computer.remote_url` value.
The controller accepts `http` only for loopback test URLs. A remote machine
must use the HTTPS Tailscale address. URLs containing credentials, query
parameters, or fragments are rejected.

The controller sends the token as:

~~~text
Authorization: Bearer <worker-token>
~~~

The token is not saved in preferences. `OPENAGENTFLEET_REMOTE_TOKEN` is a different,
daemon-wide local API token; it is not the computer-worker credential and is
not required for this setup.

## Worker API surface

The controller uses the worker's authenticated endpoints for:

| Endpoint | Purpose |
| --- | --- |
| `GET /status` | Check worker and Agent Computer status |
| `POST /ensure` | Start the worker's local runtime and computer |
| `POST /stop` | Stop the worker's computer |
| `GET /health` | Check the browser view service |
| `GET /frame` | Read the browser frame |
| `GET /desktop-frame` | Read the desktop frame |
| `GET /target?surface=browser` | Read the current browser target binding |
| `POST /action` | Perform a validated browser action |
| `POST /desktop/action` | Perform a validated desktop action |

There is no remote `docker exec` endpoint. The controller never receives the
worker's Docker socket.

## Security and secret entry

- Tailscale supplies the private network path; the worker bearer token supplies
  application authentication.
- Use a separate random token per worker. Rotate it by stopping the worker,
  starting it with a new token, and updating the controller environment.
- Never put a token in `remote_url`, a query string, a checked-in file, or a
  screenshot.
- The worker has no model keys, harness OAuth, MCP credentials, or chat data.
- Secure password/OTP handoff is **local-only** in the current implementation.
  A remote worker can show and control its browser/desktop through the normal
  action contract, but the trusted native secret handoff must happen on the
  worker host. Keep the Agent Computer local when you need the Mac-native
  password/2FA flow.
- Do not expose raw VNC, noVNC, Chrome DevTools, or the Docker socket.

## Current limitations

The remote worker is intentionally small:

- There is one optional `computer.remote_url`, not a worker pool, scheduler,
  failover system, or automatic worker discovery.
- The controller and worker workspaces are separate. Files downloaded or
  created on the worker are not automatically available to controller-side
  harnesses. Use an explicit Git/artifact transfer when you need to move files.
- The worker does not run the agent, memory, routines, MCP/plugin management, or
  notifications.
- The worker process has no bundled LaunchAgent/systemd installer yet. Keep it
  running with your preferred local process supervisor if it should survive
  logout or reboot.
- The phone remote API is a separate feature. It connects to the controller's
  mobile listener on `127.0.0.1:4318`; it is not a computer-worker connection.
- Apple Container remains experimental and is not a supported worker runtime.
- The controller must be running for the agent to use the remote computer. The
  worker can keep its container alive, but it does not run conversations by
  itself.

## Troubleshooting

### `Remote Agent Computer unavailable`

On the worker, check the process and local endpoint:

~~~sh
curl -fsS \
  -H "Authorization: Bearer $WORKER_TOKEN" \
  http://127.0.0.1:9323/status
~~~

Then check the Tailnet path from the controller:

~~~sh
tailscale status
tailscale ping worker-name
curl -fsS \
  -H "Authorization: Bearer $WORKER_TOKEN" \
  https://worker-name.your-tailnet.ts.net/status
~~~

The saved URL must be the base URL, and the worker must be serving port 9323
through Tailscale Serve.

### `401 Unauthorized`

The controller and worker tokens do not match, contain a newline, or the
worker token is shorter than 32 characters. Compare them through your secret
manager and restart both processes. Do not print the token into logs.

### `remote Agent Computer is disabled`

`botd` fails closed when a remote URL is configured without a valid
`OPENAGENTFLEET_COMPUTER_REMOTE_TOKEN`, or when the URL contains credentials, a query,
or a fragment. Set the token in the controller's launch environment and restart
`botd`, or clear the Remote worker URL to return to local Colima.

### `404 Not Found`

You are probably pointing the controller at the mobile Serve endpoint on port
4318, or at a reverse proxy that does not forward the worker paths. A computer
worker must expose `/status`, `/ensure`, `/stop`, `/frame`, and the action paths
from `cmd/agent-computer-worker`.

### Runtime or image errors on the worker

Check the runtime on the worker, not the controller:

~~~sh
docker context ls
docker info
~~~

On a Mac, install Colima and the Docker CLI if needed, then select
`--runtime colima`. The image is built on the worker from
`runtime/agent-computer`; the controller's local Docker installation is not
used when a remote URL is active.

### No browser or desktop frame

Call `/ensure` through the controller first. Then check `/status` and `/health`
with the worker token. If browser access works but the desktop frame does not,
the worker's Chromium/Xfce image is not ready; inspect the worker's Docker
container logs and keep the worker runtime selected consistently.

### Password or 2FA cannot be entered

That is expected for the current remote path: native secure handoff is local to
the trusted worker host. Use the local Agent Computer for sensitive entry, or
interact with the worker's desktop through an explicitly approved, non-secret
workflow.

## Advanced settings stay optional

The normal product path remains:

~~~text
Remote worker URL: empty
Agent Computer: local Colima + Docker
First run: no remote setup
~~~

Remote routing is an opt-in setting for users who have a second machine. It
should stay out of onboarding, normal chat, and the basic agent builder. Clear
the URL and restart the controller whenever you want to return to the simple
local setup.
