# Workspace engine and optional worker architecture

**Product rule:** one workspace engine powers all Agents. The user talks to an
Agent, not to a lead or a worker.

The terms "lead" and "worker" are internal execution roles. They may appear
in diagnostics and Advanced settings, but must not be required vocabulary in
the normal chat, onboarding, or Agent Builder.

## Normal run

```text
User message
    |
    v
Named Agent and its ongoing chat
    |  role + approved memory + enabled tools
    v
botd local controller
    |  engine preflight + permissions + run/event storage
    v
Workspace engine (internal: lead harness)
    |  direct tools and, when approved, the Agent Computer
    v
Answer in the same Agent chat
```

The Agent is the durable user-facing identity. The workspace engine is chosen
once and applies to subsequent runs for every Agent. `botd` is not a hidden
AI model: it is the Mac-local controller that owns authentication, policy,
timeouts, approvals, memory retrieval, events, artifacts, and computer
lifecycle.

Current engine adapters are Grok Build, Codex App Server, and bundled
OpenCode. The controller must fail clearly when the selected engine is missing
or not signed in. It must never silently substitute another provider.

## Agent Computer is a capability, not a worker

When an Agent needs browser, desktop, terminal, or file-manager work, the
selected workspace engine uses the local **Agent Computer** after the relevant
permission is granted. This is the visible Chromium/Linux desktop on the Mac;
it is not a cloud-only screenshot and not an extra Bot.

The computer is lazy: opening the app does not start a VM/container. First
computer use detects or explicitly sets up Colima/Docker, then starts the
persistent local desktop. During a run, the visible Agent owns the computer.
The user can take control for sign-in, OTP, CAPTCHA, payment, or any sensitive
step. Android and iOS later connect to the same Mac controller over the remote
protocol; they do not run an engine or container runtime locally.

The current backend is a Docker-compatible Linux guest with Chromium, Xfce,
Terminal, and Files. It may run through Colima, Docker Desktop, or another
accepted Docker-compatible runtime; see [Agent Computer backends](macos-agent-computer-backends.md).

## Advanced worker pool

The worker pool is optional, disabled by default, and configured globally.
It exists for a difficult task where the workspace engine can benefit from a
bounded specialist result, such as a repository inventory or a second review.

```text
Workspace engine
    |  optional bounded task slice
    v
Worker from the global advanced pool
    |  untrusted result/evidence only
    v
Workspace engine synthesizes the final answer
```

A worker is never a second visible chat participant. It cannot take over the
Agent Computer, access all Agent memory or MCPs, inherit host credentials, or
delegate recursively by default. It receives the smallest approved task and
capability set, then returns a bounded result to the workspace engine. The
visible Agent remains accountable for the final answer and every user-visible
action.

Internal worker profiles may specify an adapter, model, reasoning effort,
service tier, permission mode, turn cap, timeout, and resource budget. Those
are controller enforcement details under **Settings → Advanced**, not ordinary
per-Agent configuration. A simple per-Agent permission can allow or disallow
background help without exposing that matrix.

## Authority and data boundaries

- **Agent memory:** owned by OpenAgentFleet and shared across that Agent's
  optional chats and future engine changes. The controller passes only an
  approved retrieval snapshot to a run.
- **Search:** native search belongs to the selected engine. Hound, Donsetch,
  and Web Search Plus are explicit Agent MCP grants, never invisible fallbacks.
- **MCPs/plugins:** resolved and permission-checked by the controller before
  they are made available. Workers do not automatically inherit them.
- **Computer:** an explicit capability with separate human takeover. A worker
  has no live-desktop access unless a future isolated-worker feature grants it
  a different computer instance.
- **Run binding:** when Agent Control is enabled, the controller-owned
  `openagentfleet-browser-mcp` sidecar receives a random capability plus the
  durable run ID through child-process environment only. Botd accepts its
  agent requests only while that exact run is active, and revokes the
  capability when the run ends. The capability is never persisted, put in a
  prompt, or inherited by bounded workers.
- **Mobile:** a paired remote client of the Mac controller, not a local
  harness/worker host.

## Advanced implementation boundary

The execution model contains per-Agent lead and worker profiles because the
controller needs explicit bounds at the adapter boundary. The product surface
does not require users to configure those fields: one workspace engine owns
normal Agent runs, while the worker pool is a hidden, optional Advanced
feature. Any visible execution override must state its scope and never change
providers or permissions silently.

The presence of a worker profile or a controller contract is not, by itself,
proof that every provider combination has complete end-to-end delegation. Each
worker adapter must be preflighted and exercised under its declared limits
before the product presents it as available.
