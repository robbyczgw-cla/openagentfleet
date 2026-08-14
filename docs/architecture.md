# OpenAgentFleet architecture: why Go, TypeScript, and Rust

OpenAgentFleet is intentionally polyglot. The languages are split by runtime
boundary, not by product feature:

```text
macOS app (Tauri)
  React + TypeScript UI
        |
        | authenticated local API / events
        v
botd (Go controller)
  durable state, runs, policy, approvals, memory, engines, computer lifecycle
        |
        +--> provider CLIs / ACP / OpenCode
        +--> Docker-compatible Agent Computer (Chromium, Xfce, Terminal, Files)
        +--> optional remote computer over the paired network path

Tauri Rust shell
  owns the app process and botd child; bridges native macOS capabilities
  such as on-device speech recognition into the WebView
```

## What belongs where

### Go: `botd` is the local authority

Go owns the stateful, security-sensitive, long-running part of the product:

- SQLite-backed Agents, conversations, messages, memories, runs, routines and
  device state;
- safe transcript read models for durable approval decisions, without copying
  raw provider payloads into the UI history;
- provider discovery, OAuth/session preflight, controller-owned model catalog,
  model routing and event fan-in;
- capability and permission checks, approval lifecycle, secret handoff and
  redacted audit data;
- Colima/Docker-compatible computer lifecycle, browser/desktop proxying and
  optional remote worker coordination;
- one small static sidecar that can run on a Mac or a remote Linux host.

This is the part that benefits from a single process with predictable child
process and signal handling. The UI must not become the authority for a run or
hold the only copy of an approval or memory decision.

### TypeScript: UI and browser/provider edges

React/TypeScript owns the fast-changing user surface: chat, model selection,
attachments, onboarding, Agent Builder, approvals, computer view and settings.
It also keeps us on the same ecosystem as the future iOS/Android clients and
the browser tooling around Playwright, CDP, MCP packages and provider SDKs.

The browser/computer edge is a good TypeScript boundary because Playwright and
the surrounding Chromium tooling are Node-first. It is invoked through an
authenticated controller contract; it does not become a second state store.

Browser clients may also use the browser's explicit speech-recognition API for
voice input when the runtime exposes it. Native macOS clients prefer Apple's
on-device dictation, and the controller-hosted STT endpoint remains an explicit
fallback rather than an implicit audio upload.

The bootstrap `model_catalog` is intentionally assembled by Go. React renders
it, but it does not infer whether a model is authenticated, billable, or
available from a provider name alone. This keeps the model picker honest when
an engine is installed but not signed in.

### Rust/Tauri: native macOS shell

Rust is deliberately small here. Tauri starts and owns the bundled `botd`
child, keeps the local API credential ephemeral, handles app lifecycle, and
bridges macOS-only APIs. The native dictation bridge uses Apple's speech and
audio APIs, emits partial/final text to the React composer, and refuses to
silently upload audio when on-device recognition is unavailable.

## Why not make everything TypeScript?

That would make UI work convenient, but it would put the hardest authority
and lifecycle responsibilities on a Node process: more runtime packaging,
more dependency surface, more care around child-process cleanup, and more
places for credentials and permission state to leak into application code.
It would not remove the need for a native macOS boundary or a browser worker.

## Why not make everything Go?

Go is excellent for `botd`, but it is a poor fit for React UI iteration,
Playwright/CDP integrations, MCP/provider packages, and shared mobile UI. A
Go-only product would either reimplement those ecosystems or introduce a
second web runtime anyway.

## Trade-off and current smell

The split costs us duplicated API types and a process boundary. We should
reduce that cost with a versioned API contract and generated TypeScript types;
we should not move the controller into the client just to remove the
boundary.

The current maintainability smell is `client/src/App.tsx` being too large,
not the language split. The next cleanup should extract chat/composer,
onboarding, Agent Builder, computer view, and settings into components while
keeping the single server-backed store and transport path.

The chat surface stays intentionally simple while Go remains the durable local
controller because the target also includes local isolation, approvals,
memory, routines and remote Mac computer nodes.

## Packaging identifiers

The product-facing name is `OpenAgentFleet`. A few internal package identifiers
remain intentionally compatibility-stable while the public alpha is evolving:

- the Tauri crate is still named `client`;
- the bundled Computer sidecar package is still named
  `openagentfleet-agent-computer`;
- controller environment variables retain their `OPENAGENTFLEET_*` prefix.

These names are not product dependencies or user-facing branding. They should
be renamed in one coordinated packaging change only after sidecar lookup,
installer metadata, release scripts and migration notes are updated together.
