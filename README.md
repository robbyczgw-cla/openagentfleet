# OpenAgentFleet

> **Run AI agents on your Mac with explicit control.**

OpenAgentFleet puts Grok Build, Codex App Server and OpenCode in one local
workspace. When an agent needs a browser or desktop, it can use a separate
Linux computer that you can watch, stop or take over.

**This is an Apple Silicon macOS alpha.** It is source-build software, not a
finished product. There is no signed or notarized download yet.

[Build the macOS alpha](#build-from-source) ·
[Visit openagentfleet.xyz](https://openagentfleet.xyz) ·
[Read the security model](docs/architecture.md)

![OpenAgentFleet Computer View showing a live isolated Linux desktop with Chromium, Terminal and Files](presentation/assets/macos-openagentfleet-computer-ready.png)

## The short version

1. Choose an AI engine and create an Agent.
2. Ask it to do work in one conversation.
3. Open Computer View when it needs a browser or desktop. Watch it work, or
   take control yourself.

Conversations, memory, approvals and run history stay in the local Go
controller. Provider inference may still use the provider's remote service.

## What works in the alpha

- Grok Build, Codex App Server and bundled OpenCode lead engines.
- Model, reasoning and service-tier selection where the selected engine
  supports it.
- A real isolated Linux desktop with Chromium, Xfce, Terminal and Files.
- Live computer view, stop, take-control and approval flows.
- Local conversations, memory, transcripts and run artifacts.
- File and image attachments, drag and drop, and dictation plumbing.
- Optional MCP connectors, plugins, bounded workers, routines and remote
  phone clients.

The advanced features are optional. The default experience is one Agent and
one conversation.

## Build from source

The supported development target is Apple Silicon macOS. Install:

- Go;
- Node.js and `pnpm`;
- Rust and the macOS build tools;
- `uv` and `uvx`;
- OpenCode `1.18.10` for the bundled OpenCode path.

Provider logins remain optional: use the engine you have configured. Colima
is recommended for Computer View; Docker Desktop and other Docker-compatible
contexts are fallbacks.

```sh
git clone https://github.com/robbyczgw-cla/openagentfleet.git
cd openagentfleet

go test ./...

cd client
pnpm install
pnpm run prepare:sidecar
pnpm run tauri dev
```

To run only the shared browser client during development:

```sh
cd client
pnpm install
pnpm dev
```

Browser mode is a development/remote-client surface. The packaged macOS app
starts the local Go controller automatically.

## Computer View

The Agent Computer is a separate, non-root Linux guest. It starts lazily when
you open Computer View or an approved task needs browser/desktop access.

The recommended macOS path is a dedicated Colima profile with Docker. On its
first start OpenAgentFleet creates only its own workspace and Chromium-profile
mounts, then starts Xfce, Chromium, Terminal and Files. It does not mount the
host root or expose the Docker socket to the guest.

Docker Desktop and other Docker contexts can be selected as fallbacks. Apple
Container and Lume remain experimental until they pass the same desktop-frame,
Chromium, takeover and approval tests.

The computer is the browser/desktop boundary. Provider CLIs and harnesses
currently run as normal processes on macOS; this is not full provider-process
isolation.

## Engines and approvals

- **Grok Build** and **Codex App Server** can use the OpenAgentFleet approval
  broker for sensitive actions.
- **OpenCode** keeps OpenCode's own safe permission handling. Its dangerous
  automatic mode is disabled and it is labelled accordingly in the UI.

The user-facing concept is simply **AI engine**. Internally, the selected
engine is the lead and may delegate small, explicitly bounded tasks to
workers. Workers receive a task, model/reasoning/budget and permissions; they
do not inherit hidden credentials.

## Remote clients, MCP and plugins

The Mac remains the execution host. iOS and Android clients are remote clients
for the durable Mac controller; they do not run Docker, browsers or provider
CLIs locally. Pair them over Tailscale or another private network.

Native search is available to engines that support it. Web Search Plus and
Hound are independent, optional MCP connectors with visible per-Agent
configuration. Plugins and skills are capability-brokered and do not receive
arbitrary host access just because they are installed.

## Security boundary

OpenAgentFleet is local-first and approval-oriented:

- controller state, memory and Computer lifecycle remain on the Mac;
- only approved workspace/profile paths are mounted;
- the Computer image is non-root and has no Docker socket;
- browser and desktop actions are server-gated;
- sensitive takeovers and credential handoffs are explicit;
- approvals, transcripts and run events are durable and auditable.

This protects the Agent Computer boundary. It does not claim that a malicious
process already running as the same macOS user cannot inspect host memory or
other user-owned processes. See [SECURITY.md](SECURITY.md) for reporting and
scope.

## Documentation

- [Architecture](docs/architecture.md)
- [Agent model](docs/agent-model.md)
- [Lead and worker model](docs/lead-worker-architecture.md)
- [Fresh-user smoke test](docs/fresh-user-smoke-test.md)
- [macOS host lifecycle](docs/mac-host-install.md)
- [Remote mobile clients](docs/mobile-remote.md)
- [Search connectors](docs/search-connectors.md)
- [FAQ and troubleshooting](docs/faq.md)
- [Documentation map](docs/README.md)

## Verification

```sh
go test ./...
go vet ./...
go test -race ./internal/compute ./internal/httpapi ./internal/browsermcp -count=1
pnpm --dir client exec tsc --noEmit
pnpm --dir client exec vite build
cargo test --manifest-path client/src-tauri/Cargo.toml --locked
cargo fmt --manifest-path client/src-tauri/Cargo.toml --check
git diff --check
```

These checks do not prove provider login, a healthy Colima VM, macOS privacy
prompts, physical-device pairing or notarization. Those need live acceptance
tests.

## Status

The current slice is focused on a trustworthy local Mac runtime: one-Agent
chat, durable memory, attachments, approvals, a real Chromium/Xfce computer,
optional workers and remote-client foundations.

Signed distribution, native mobile releases, broader provider adapters,
universal worker isolation and a complete production skill/plugin lifecycle
are still ahead.

## License

OpenAgentFleet is licensed under [Apache-2.0](LICENSE). Runtime notices for
bundled or explicitly launched components are in [NOTICE.md](NOTICE.md).
