# OpenAgentFleet documentation

This directory is the maintained engineering record for OpenAgentFleet. Each
document distinguishes the current implementation from a planned contract so
that the app does not accidentally claim product or security properties it has
not earned yet.

## Start here

| Document | Use it for | Status |
| --- | --- | --- |
| [Architecture](architecture.md) | Why Go, TypeScript, and Rust are separate, and which layer owns what | Current architecture |
| [Grok Build parity](grok-build-parity.md) | The feature-by-feature parity contract | Living backlog |
| [Bot memory](bot-memory.md) | Explicit, reviewable memory semantics, API, and retrieval boundary | Implemented local MVP |
| [Agent model](agent-model.md) | Adopted chat-first product contract: one workspace engine, Agents, memory, Builder, and lazy Computer | Current product contract |
| [Workspace engine and optional workers](lead-worker-architecture.md) | Internal controller/engine/worker boundary behind the simple product UX | Current implementation + advanced contract |
| [Agent Computer backends](macos-agent-computer-backends.md) | Lazy local Computer lifecycle; Colima/Docker, VM, and macOS backend decisions | Implemented backend + product roadmap |
| [Windows desktop track](windows-desktop.md) | Windows/Tauri/WebView2/Docker Desktop groundwork and live-release gates | In development |
| [Remote Agent Computer worker](remote-computer-worker.md) | Optional second-host Docker/Colima worker over Tailscale | Implemented optional path |
| [Remote Mac architecture](remote-mac-architecture.md) | Network boundary, Tailscale, and mobile safety rules | Current topology + target boundary |
| [Mobile remote protocol](mobile-remote.md) | Pairing, device identity, API allowlist, and rollout phases | Implemented alpha + target contract |
| [ADR 0001: mobile alpha boundary](decisions/0001-mobile-alpha-boundary.md) | What must ship before a remote alpha, and what is intentionally deferred | Accepted |
| [Mac host install](mac-host-install.md) | LaunchAgent and local host operation | Setup guide |
| [OpenCode bundling](opencode-bundling.md) | Exact macOS sidecar pin, architecture checks, and upgrade procedure | Implemented packaging contract |
| [macOS release runbook](macos-release.md) | Developer ID signing, notarization, stapling, checksums and public prereleases | Passed for public `v0.1.0-alpha`; repeat for future releases |
| [Search connectors](search-connectors.md) | Optional Web Search Plus and Hound MCPs, pins, per-Agent grants, and runtime boundaries | Implemented connector contract |
| [Fresh-user smoke checklist](fresh-user-smoke-test.md) | Short native-app checklist for the simple first-run flow and optional Advanced settings | Current QA runbook |
| [CI and QA gates](ci-qa.md) | Local and GitHub Actions checks, plus what each gate does not prove | Current release runbook |

## Documentation conventions

- **Implemented** means it exists in this repository and has a matching test
  or build proof where practical.
- **Contract** means a deliberately specified target; it is not a claim that
  the feature is already available.
- Security boundaries include their known residual risks. In particular,
  macOS secure handoff is intentionally local-only and not a general secret
  containment system.

Historical engineering notes are intentionally kept out of the product
documentation index. The maintained docs describe OpenAgentFleet's own
contracts, implementation boundaries and measured QA.

## Release gates

Before a remote-mobile release, update the following together:

1. `docs/mobile-remote.md` for the exact API and pairing revision.
2. `docs/remote-mac-architecture.md` for the deployed Serve port and ACL
   instructions.
3. `mobile/README.md` for user setup and device limitations.
4. `NOTICE.md` if any third-party source code or material design
   system was newly incorporated.

Runbook changes are documentation changes too: if a command changes network
exposure, it must say exactly which listener it exposes and whether it modifies
Tailscale Serve state.
