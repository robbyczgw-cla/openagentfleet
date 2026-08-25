# Agent runtime foundation

Status: implemented foundation. Native and Docker backends wrap existing local
execution. Agent-to-agent delegation and a separate remote backend are
extension points, not shipped behavior.

This is the controller-side runtime model inside `botd`. It does not copy
Grok Bot's renderer, IPC, transcript formats, or process split.

## Ownership

```text
Agent owns     identity, memory ref, conversation ref, skills, permissions,
               engine binding, computer binding
Engine owns    provider I/O, request formatting, stream decoding, auth probe,
               declared capabilities
Tool layer     canonical definitions, validation, capability checks,
               protocol adapters (MCP and future engine formats)
Computer       process exec, filesystem, lifecycle, health
Coordinator    turn lifecycle, per-agent queue, event routing,
               future delegation
```

The HTTP API and UI keep using `run.*` and `provider.output`. Engines emit
`agent.*` names as well so new code does not branch on provider stream types.

## Agent vs engine

An Agent is a durable teammate (`bots` row + metadata JSON). The engine is
whichever installed CLI currently powers a turn (`grok_build`,
`codex_app_server`, `opencode`, `pi`, plus worker adapters).

Changing an Agent from Codex to Claude does not create a new Agent. Memory,
conversation, skills, permissions, and computer binding stay on the Agent.
`internal/engine.Adapter` is the replaceable contract:

- `GetCapabilities()` is explicit
- `GetAuthState()` is metadata-only (no tokens)
- `RunTurn` yields normalized `agent.*` events
- provider-specific protocol stays inside the adapter

Existing harness runners are wrapped, not replaced.

## Agent vs computer

The Agent references a logical computer (`AgentMetadata.Computer`). The
logical computer references a `computer.Backend`. Default binding:

- id `workspace`
- backend `docker` (the shipped local Agent Computer)

Moving that computer from Docker to a future native or remote backend must
not create a new Agent. Domain code does not call `os/exec` or Docker.

Today one workspace computer is still the product default. The binding exists
so a later per-agent computer does not require a new identity.

## Tool registry

`internal/tools` is the only catalog. MCP is a transport adapter over that
catalog. Existing browser, computer, and collaboration MCP tools are
registered under dotted names with the original underscore aliases:

```text
browser.navigate  =  browser_navigate
agent.delegate    =  delegate_to_agent
```

Execute stays unbound for those tools: botd remains the HTTP/policy
authority. The registry still validates input and required capabilities
before any executor runs.

## Engine adapters

```text
Agent  →  EngineAdapter  →  grok | claude | codex | opencode | pi | cursor
```

`CapabilitiesFor` states MCP/computer/tools support per engine. Pi does not
advertise MCP or Computer MCP. Unknown engines get streaming only.

## Computer backends

```text
ComputerBackend
  ├── NativeBackend   host argv exec (tests + future native_host)
  ├── DockerBackend   wraps compute.Docker (production)
  └── remote          reserved kind, not implemented here
```

`DockerBackend` is the production path, including the existing remote
transport already inside `compute.Docker`. This package does not add a
second remote stack.

## Turn scheduling

The coordinator queue is keyed by Agent ID:

- two turns for the same Agent run FIFO, one at a time
- different Agents may run at the same time
- a failed or canceled turn does not poison later turns for that Agent

`httpapi` launches turns through this queue when an Agent ID is present.
Tests that omit the queue keep the previous per-run goroutine behavior.

## Fleet events

Normalized names live in `internal/domain/fleet_events.go`. Engines map
provider streams before the rest of OAF sees them:

```text
Claude stream ─┐
Codex stream ──┼─→ EngineAdapter → agent.* + existing run.*/provider.output
Grok stream ───┘
```

`agent.delegation.created|started|completed|failed` are emitted for
`HandoffModeDelegate`. Computer lifecycle names remain reserved.

## Extension points

Remote computers: add a `KindRemote` backend that speaks the existing worker
HTTP contract. Do not teach the Agent domain about URLs.

Delegation: the coordinator plans the hop and queues a turn on the target
Agent. The source engine does not own the target. Policy (allowlist, depth,
active-peer cap) stays in `orchestration.ValidateAgentTask`. The existing
handoff row is the durable job; `agent.delegation.*` events are the fleet
names for that hop.

## Diagram

```text
                    OAF client
                        │
                    Fleet Host (botd)
                        │
                 Fleet Coordinator
                        │
          ┌─────────────┼─────────────┐
          │             │             │
       Agent A         Agent B         Agent C
          │             │             │
       Engine         Engine        Engine
       Adapter        Adapter       Adapter
          │             │             │
       Codex          Claude         Grok
          │             │             │
          └─────────────┼─────────────┘
                        │
                   OAF Tools
                        │
                Capability/Policy
                        │
                 Agent Computer
                        │
               ComputerBackend
                 ┌──────┼──────┐
                 │      │      │
               Native Docker Remote
```
