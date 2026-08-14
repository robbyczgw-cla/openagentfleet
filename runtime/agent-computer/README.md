# Agent Computer image

This image is the first local runtime for the persistent user-scoped Agent Computer.

The desktop, Chromium and computer server run as the unprivileged `agent` user
inside the isolated runtime. A short-lived root entrypoint only prepares the
two app-owned mounts before dropping privileges. It runs a persistent Xfce
desktop with Chromium, a terminal and file-manager inside Xvfb; Chromium DevTools
Protocol is attached through Playwright. The Go daemon bind-mounts `/workspace`
from the host and puts `/home/agent/.chromium-profile` in a durable
Docker-managed volume inside the local runtime. This keeps Chromium's POSIX
profile locks away from the macOS virtiofs boundary.

The current view contract is:

- `GET /health` — readiness, URL, title, viewport, and tab count;
- `GET /frame` — one PNG viewport frame;
- `GET /tabs` — current persistent Chromium tabs;
- `POST /action` — navigate, click, type, key press, scroll, reload, back, or forward.

## Image and resource defaults

The controller builds this image from Ubuntu 24.04 by default. Ubuntu 26.04 and
Debian 13 are also supported base-image choices. The standard Agent Computer
resource contract is 4 CPU, 4 GiB RAM, 25 GiB disk and 1 GiB guest swap. The
controller accepts optional overrides of 1–16 CPU, 2–64 GiB RAM, 10–500 GiB
disk and 0–16 GiB guest swap; changes apply when the computer starts again.

Runtime behavior is provider-specific. Colima uses CPU, RAM and disk as VM
resources and configures the requested swap inside the Linux guest. Docker
Desktop and OrbStack use CPU, RAM and swap as container limits while managing
their VM resources and VM disk separately. An existing larger Colima disk is
never shrunk to satisfy a smaller setting.

Before Colima provisioning, `botd` checks host free space for the Colima
storage and the workspace/runtime volume. Insufficient space blocks startup
with a retryable free-space error before provisioning begins. Swap is only an
emergency buffer, not a replacement for RAM.

The app receives full Xfce desktop frames from the container and sends manual
click/type/key actions through `botd`. The server applies the same explicit
takeover gate as it does for the browser surface. Raw VNC/noVNC is deliberately
not published because a client-side read-only flag cannot securely enforce
human takeover.

Only the token-authenticated controlled view-service port is published to
`127.0.0.1` on the Mac; CDP stays inside the container. botd holds a fresh,
per-container capability token and adds the explicit human takeover gate before
forwarding actions. Its owner-only local state file lives outside the
bind-mounted workspace, so a botd restart can reattach to the same container;
the token is rotated when that container is recreated or stopped. The browser
profile volume survives container recreation and image upgrades.

## Native secure handoff

For an active, user-controlled takeover, the macOS shell can collect a password
or OTP in an `NSSecureTextField`. It sends the value exactly once to `botd` over
an owner-only Unix-domain socket using `OFBH/1`; the browser-accessible HTTP API
never accepts secret bytes. The in-memory handoff manager then authorizes a
single controlled `type` action for the focused browser `input` or `textarea`
captured when the handoff was requested. Desktop secret entry is intentionally
not supported yet.

The computer service rechecks that computer and target immediately before it
types. A changed document, tab, focused field, or unavailable target fails
closed and requires a fresh entry. The handoff does not create a chat message,
Teach trace, or model event. CAPTCHA, payment, and desktop credential fields are
intentionally not supported by this path. Raw VNC/noVNC is also not published.

The mechanism is best-effort secret minimization, not a promise that bytes never
exist transiently in memory. It also does not defend against a malicious process
already running as the same macOS user.

The image is built and started by `botd` only when computer execution is explicitly enabled:

```text
OPENAGENTFLEET_ALLOW_COMPUTER_EXECUTION=1 botd
```

The image is not a security boundary by itself. The host daemon must control mounts, credentials, network policy, and approval gates.
