# OpenAgentFleet client

OpenAgentFleet is the Tauri 2 + React/TypeScript Mac client for the local `botd`
runtime. It is intentionally a remote-safe client: it talks to the Go API and
does not launch Docker or provider CLIs directly. The same API boundary is the
route for later iOS and Android clients.

## Development

From this directory:

```sh
pnpm install
pnpm dev
```

Set `VITE_BOTD_URL` for a remote Mac daemon and `VITE_BOTD_TOKEN` for the
optional bearer token. The UI currently includes the Bot conversation, durable
runs, live SSE activity, provider selection, session status, computer status,
and approval controls. The remaining Grok Build parity surfaces are tracked in
`../docs/grok-build-parity.md`.

## Recommended IDE Setup

- [VS Code](https://code.visualstudio.com/) + [Tauri](https://marketplace.visualstudio.com/items?itemName=tauri-apps.tauri-vscode) + [rust-analyzer](https://marketplace.visualstudio.com/items?itemName=rust-lang.rust-analyzer)
