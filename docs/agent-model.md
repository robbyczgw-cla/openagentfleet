# Agent product model

**Product decision:** OpenAgentFleet is a chat-first Bot product. A user should
not need to learn the distinction between a lead, a harness, a worker, a
container runtime, or a model profile before sending the first message.

This document is the product contract. Internal execution structs may use
provider-specific lead/worker terminology, but that vocabulary is deliberately
hidden from the normal product surface.

## The simple mental model

```text
One workspace engine
        |
Many Agents, each with one ongoing chat by default
        |
One local Agent Computer, only when an Agent needs it
```

- An **Agent** is the Bot the user names, chats with, and edits.
- An **engine** is the workspace-wide AI runtime chosen once in Settings. It
  is backed by an installed provider such as Grok Build, Codex App Server,
  bundled OpenCode, or Pi. Internally, this is the selected *lead harness*.
  The default remains Grok Build (`grok-4.6`); Pi is opt-in.
- The **Agent Computer** is the visible Chromium/Linux desktop with Files and
  Terminal. It is started only when it is needed, and only for engines that
  actually receive Computer MCP. A Pi engine does not.
- A **worker** is an optional, hidden advanced helper. It is not another Bot,
  another chat, or another computer the user must configure.

The normal experience is simply: write to an Agent; the selected workspace
engine does the work; when browser or desktop work is needed, the Agent opens
its visible local computer and asks for takeover when appropriate.

## Workspace engine

Every workspace has one active engine. The engine selection belongs in a small
"Your engines" onboarding/settings surface, which detects local tools and
their login state:

- Grok Build (the default, model `grok-4.6`);
- Codex App Server;
- bundled OpenCode, including an explicitly configured provider/model;
- Pi, when the `pi` CLI is installed and signed in with `pi /login`.

Pi is a selectable workspace engine, not a silent substitute. A Pi lead starts
`pi --mode rpc --no-session` with an exact `--tools` allowlist. `--no-session`
keeps Agent memory OpenAgentFleet-owned rather than writing a Pi session.
Auth lives in Pi's own `~/.pi` store; OpenAgentFleet does not run OAuth for
Pi. Pi receives no MCP injection: no Hound, no Web Search Plus, and no
Computer MCP. A Pi lead therefore has no Agent Computer. Lead `ask`, when
used, confirms through a bundled Pi extension and RPC `extension_ui`, not a
native OpenAgentFleet permission popup.

The onboarding result is a working engine or a clearly marked draft setup. It
does not configure a container runtime, a worker pool, search connectors, or
a separate per-Agent model matrix. A provider that is installed but not signed
in must say so plainly and offer its provider-owned sign-in action; it must not
be displayed as connected merely because the binary exists.

Changing the workspace engine affects subsequent runs for every Agent. It does
not create a new Agent, erase an Agent's memory, or silently move credentials.
An explicit preflight prevents an unauthenticated or unavailable engine from
being substituted by a different provider.

The runtime may retain per-Agent execution fields for explicit Advanced
experiments, but normal Agent creation and editing never requires them. The
workspace engine is the default for every Agent, and the implementation may
call that adapter boundary a "lead" internally without exposing it to users.

## Agents and chats

- One Agent is created with one durable, ongoing chat.
- The sidebar lists Agents, not an independent collection of conversations.
- Multiple chats are an off-by-default preference. Enabling them adds threads
  for the selected Agent without splitting its identity or memory.
- An Agent owns its name, role, description, optional avatar, memory,
  notifications, approved skills/plugins/MCPs, routines, and computer policy.
- Agent memory belongs to OpenAgentFleet and survives engine changes. Each run
  receives only a controller-approved memory snapshot.
- Role and description are bounded system context, separate from user messages
  and reviewable memory. An avatar is presentation metadata and is never sent
  to a model without an explicit feature that says so.

An Agent is therefore a durable teammate, not a wrapper around a particular
provider session.

## Agent Builder

The normal Builder creates an Agent and its first chat atomically. It contains
only the information that defines the teammate:

- name, role, description, and optional avatar;
- approved tools, plugins, and MCP connectors;
- notification preference;
- whether the Agent may request the Agent Computer.

Engine, model, reasoning, service tier, permission internals, worker profiles,
and computer backend selection are not normal Builder fields. Provider-specific
settings belong to **Workspace Settings → Engines**; high-risk capabilities
remain behind explicit permission prompts. Advanced settings may expose a
model override only when the selected engine can enforce it, and must show the
scope and reset path.

The optional Fleet Guide is an ordinary Agent template. It has no hidden
controller role or extra authority.

## Lazy local Agent Computer

Opening OpenAgentFleet must not create a VM, start a container, or install a
container runtime. The Agent Computer setup starts only when the user asks an
Agent to use a computer or explicitly opens Computer View.

On first use, the app:

1. explains that the computer is an isolated Linux desktop on this Mac;
2. detects a compatible runtime;
3. recommends Colima as the open-source macOS route and accepts Docker Desktop
   or another compatible runtime as a fallback;
4. offers an explicit install/copy-command/retry path if no runtime is ready;
5. starts the persistent Chromium, Files, and Terminal environment only after
   confirmation.

The visible Agent owns that computer during a run. The user can take control
for passwords, OTPs, CAPTCHA, payment, or any other sensitive step. A worker
never shares that live desktop by default.

## Optional advanced worker pool

Workers are an implementation optimization for unusually large or parallel
tasks. They stay off by default and are configured once in **Workspace
Settings → Advanced → Worker pool**, not in onboarding or the ordinary Agent
Builder.

When enabled, the active workspace engine may delegate a narrowly bounded
subtask to an eligible worker. The worker returns evidence to the engine; the
visible Agent remains responsible for the plan, tools, computer, approvals,
and final response. Workers receive only the task slice and the capabilities
the controller grants. They do not inherit the live computer, full memory,
all MCPs, host credentials, or a right to recursively delegate.

The pool can be disabled globally at any time. Per-Agent use is a simple
permission such as "Allow background help for this Agent", not a second
provider configuration screen.

The current end-to-end slice is deliberately narrow: an eligible Grok or
OpenCode engine records its draft, fans out bounded worker calls in parallel,
records worker lifecycle events, and asks the same engine to synthesize the
final answer. A worker receives no Computer MCP, run capability, full Agent
memory, or implicit connector grant. Pi remains an eligible worker when its
stored permission is `read_only` or `workspace`; that worker path is
unchanged: `pi --mode rpc --no-session` with an exact `--tools` allowlist and
never `bash`. Claude, Codex CLI and Cursor remain declared adapters whose
enforceable execution path is future work; their profiles are rejected at
preflight rather than silently substituted.

## Search, permissions, and current boundary

Native web search is an engine capability on Grok Build and Codex App Server.
Optional Web Search Plus and Hound remain explicit Agent MCP grants; they are
never silently installed or used as a fallback for disabled native search.
They are not injected into a Pi engine at all. Search connector configuration
belongs in Agent tools/Settings, not first-run onboarding.

`botd` remains the local policy boundary: it authenticates local clients,
preflights the selected engine, retrieves approved memory, brokers permissions,
records runs/events/artifacts, and controls the Agent Computer. Mobile clients
remain remote clients of the Mac authority; they do not run engines or local
container runtimes themselves.

The UI/API may expose richer per-Agent execution controls only inside an
explicit Advanced section. They are optional implementation controls, never
the required way to create or use a Bot.

See [Lead and worker architecture](lead-worker-architecture.md) for the
internal execution boundary and [Agent Computer backends](macos-agent-computer-backends.md)
for the local-runtime decision.
