# Fleet Host MVP

A Fleet Host is an always-on OpenAgentFleet controller (`botd`) that stays the
authority for Agents, chats, pairing, and the local Agent Computer. A laptop
or phone can pair as a **client**. The computer worker is a separate optional
path and is not the host.

This is an MVP: the host still binds loopback, and Tailscale Serve remains the
private network edge. Funnel is out of scope.

## Role

- `GET /api/host/status` reports `{host_id, api_version, auth_version, role}`.
- `role` is `authority`. `auth_version` stays at Bearer (`1`) until a
  coordinated bump.
- Pairing platforms are `ios`, `android`, and `desktop`.
- Dropping a client SSE stream does not cancel an in-flight run. Interrupted
  runs fail closed only when **this host** restarts.

## Linux host

From the repository root:

```sh
bash scripts/install-linux-fleet-host.sh
```

The script builds `botd` into `~/.local/share/openagentfleet/bin`, writes a
user systemd unit, and starts it on `127.0.0.1:4317`. It does not bind
`0.0.0.0`, does not point Tailscale Serve at `:4317`, and does not enable
Funnel.

Harness and computer execution stay disabled in the unit until you review the
workspace and approval policy.

## Private tailnet access

Install and authenticate Tailscale separately, then run:

```sh
bash scripts/configure-tailnet-serve.sh
```

Serve must proxy **only** the mobile listener on `127.0.0.1:4318`. Do not Serve
the local desktop API on `:4317`.

## Desktop as a client

The desktop app can still spawn a local `botd` for a laptop-owned workspace.
Treating that laptop as a **remote client of a Linux or Mac host** is the
intended Fleet Host shape: pair with platform `desktop` against the host’s
mobile listener, keep the host as authority, and do not collapse the Agent
Computer worker into the host process.
