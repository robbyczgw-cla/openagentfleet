# CI and QA boundary

OpenAgentFleet CI validates code-level contracts only. It does not claim that a
provider is logged in, that a container desktop is visible, or that a macOS
permission prompt was accepted.

Third-party GitHub Actions are pinned to reviewed commit SHAs in the workflow;
the trailing release comments are only human-readable version labels.

## Automated on every push and pull request

| Check | What it proves | What it deliberately does not prove |
| --- | --- | --- |
| `go test ./...` | Go unit and package integration tests pass. | A real harness, Docker daemon, or graphical desktop works. |
| `go test -race ./internal/compute ./internal/httpapi ./internal/browsermcp` | The Agent Computer, browser-MCP and HTTP API test suites have no race detected in their exercised paths. | Race freedom across every package or a live container session. |
| HTTP/store lifecycle tests | Attachment upload/claim/run creation is atomic, stale pending uploads are bounded, immediate run cancellation is accepted, a late provider answer cannot be persisted after stop, and one-chat visibility remains per-Agent. | A provider completing a real task or recovery from every external process failure. |
| Agent Computer contract smoke | Disabled mode, loopback-only published ports, bounded browser/desktop actions, redirect refusal, and local-only Colima installation remain enforced in fake/in-process tests. | Starting Colima, Docker, Chromium, a remote worker, or a real browser. |
| `pnpm --dir client exec tsc --noEmit` | The TypeScript client typechecks. | Rendering, native Tauri APIs, microphone permission, or drag-and-drop on macOS. |
| `pnpm --dir client exec vite build` | The web client production bundle builds. | The packaged native app or a signed/notarized release. |
| `pnpm --dir mobile exec tsc --noEmit` | The Expo remote client typechecks against the current pairing/API contract. | A connected iPhone/Android device, Tailscale reachability, or a store-signed build. |
| `npm --prefix runtime/agent-computer ci --omit=dev --ignore-scripts --no-audit --no-fund` | The isolated Computer image's npm lockfile is complete and reproducible. | The assembled image, Chromium startup, or a graphical session. |
| Agent Computer Docker image build | The pinned Node base, Chromium/Xfce packages, locked Playwright dependency and non-root entrypoint assemble into a Linux amd64 image. | The macOS Colima arm64 image, live Chromium frame, or host permissions. |
| `cargo test --manifest-path client/src-tauri/Cargo.toml --locked` | The Tauri Rust crate tests pass with placeholder sidecars. | Launching actual sidecars, OAuth, or the native macOS window. |
| Linux sidecar script syntax | `scripts/build-tauri-sidecar.sh` and the Linux release scripts parse. | A published `.deb` / `.rpm` / `.AppImage` or a live Docker Computer. |

The Computer smoke test is intentionally secret-free and does not contact a
Docker daemon. It uses existing fake Docker configuration and `httptest`
servers; CI must not gain provider credentials merely to cover this boundary.

## Required manual evidence before a macOS release

Use the [fresh-user native smoke checklist](fresh-user-smoke-test.md) with a
fresh disposable data directory and the packaged Tauri app. At minimum record:

1. onboarding and selected-engine state;
2. provider sign-in state without exposing credentials;
3. lazy Agent Computer start through the selected runtime;
4. Chromium, desktop frame, Take Control, and recovery/error state;
5. attachment picker and Finder drag-and-drop;
6. microphone permission and transcription behavior when configured;
7. the exact build/commit, macOS version, architecture, and evidence path.

Remote workers, mobile clients, provider OAuth, real web-search connectors,
and signed/notarized distribution require their own live evidence. They are
not represented as passing by this CI workflow.

The macOS development path also has a local Colima proof:

```sh
docker --context colima-openagentfleet build \
  --progress=plain \
  --tag openagentfleet-agent-computer:lock-check \
  runtime/agent-computer
```

That build was run successfully on the development Mac; it is evidence for the
arm64-compatible local runtime, not a replacement for the Linux CI job.
