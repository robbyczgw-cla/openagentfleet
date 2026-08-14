# Agent Computer image

This image is the first local runtime for the persistent user-scoped Agent Computer.

It is isolated and non-root. It runs a persistent Xfce desktop with Chromium,
a terminal and file-manager inside Xvfb; Chromium DevTools Protocol is attached
through Playwright. The Go daemon mounts `/workspace` and
`/home/agent/.chromium-profile` from the host workspace.

The current view contract is:

- `GET /health` — readiness, URL, title, viewport, and tab count;
- `GET /frame` — one PNG viewport frame;
- `GET /tabs` — current persistent Chromium tabs;
- `POST /action` — navigate, click, type, key press, scroll, reload, back, or forward.

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
profile survives container recreation.

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
