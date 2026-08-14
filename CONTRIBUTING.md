# Contributing to OpenAgentFleet

Thanks for helping with OpenAgentFleet. The project is in a private alpha, so
the priority is a small, understandable Mac-first product with honest
security boundaries and reproducible checks.

## Before opening an issue

- Use the bug template for reproducible product defects.
- Use the feature template for user-facing proposals.
- Never put credentials, browser profiles, private transcripts, customer data
  or unredacted screenshots into an issue.
- Report suspected security vulnerabilities privately through
  [SECURITY.md](SECURITY.md), never in a public issue.

## Development setup

The supported development host is Apple Silicon macOS. The native Tauri path
requires Go, Node.js with `pnpm`, Rust with the macOS build tools, `uv`, `uvx`
and OpenCode `1.18.10` for the bundled sidecars:

```sh
go test ./...
cd client
pnpm install
pnpm run prepare:sidecar
pnpm run tauri dev
```

For UI-only work, use `pnpm dev` in `client`; this runs the shared browser
client and does not prove native Tauri behavior. A Docker-compatible runtime
is required only for live Agent Computer work.

## Architecture rules

- Keep durable state, approvals, policy, memory and computer lifecycle in the
  Go `botd` controller.
- Keep the React/TypeScript client server-backed; it must not become a second
  state store or approval authority.
- Keep native macOS APIs and the bundled controller lifecycle in the small
  Rust/Tauri shell.
- Never add broad host mounts, Docker-socket access, hidden credentials or
  unconditional auto-approval to the Agent Computer.
- Provider-specific permission behavior must be labelled honestly. Do not
  claim that an adapter has controller-brokered approvals unless it actually
  uses the broker.
- Optional connectors, workers, remote nodes and experimental backends must
  remain opt-in and capability-brokered.

## Checks before a pull request

Run the narrowest relevant checks, and run the full set for cross-layer work:

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

For changes involving the Agent Computer, also record whether the proof was a
fake runtime, Docker/Colima live run, packaged Tauri app, remote worker,
browser, or physical device. Do not turn a code-level test into a claim of
live runtime or signing proof.

## Change discipline

Keep pull requests focused, explain the user impact and security boundary,
and update the relevant documentation with the implementation. Do not commit
local databases, generated bundles, provider tokens, browser profiles, or
machine-specific paths. New public-facing capabilities need an opt-out or
permission story when they are not safe for every user.
