# OpenAgentFleet search connectors

OpenAgentFleet has two search layers: native search owned by an authenticated
lead harness, and optional MCP connectors owned by the Agent configuration.
They are deliberately not one global search service.

## First-install behavior

Grok Build and Codex App Server default to native live web search for new and
legacy lead profiles. The default is an intent that becomes usable after the
selected harness is authenticated; it is not a promise that a fresh install
can search before login. Turning native search off is explicit and must not
silently substitute Web Search Plus, Hound, Donsetch, or stale knowledge. See the
[Grok Build CLI/ACP documentation](https://docs.x.ai/build/cli/headless-scripting)
and [Codex App Server documentation](https://developers.openai.com/codex/app-server/).

OpenCode `1.18.10` is bundled as the third lead. The model picker exposes the
free starter route `opencode/deepseek-v4-flash-free` plus explicit OpenCode Go
choices `opencode-go/deepseek-v4-flash` and `opencode-go/deepseek-v4-pro` when
the local provider is configured. The starter route was measured at cost `0`
today; that is a time-specific observation, not a price or availability
guarantee. For fresh search, OpenCode uses an explicitly enabled connector
selected on the Agent.

The independent optional connector toggles are:

| Connector | ID | Default | Credentials | Runtime role |
| --- | --- | --- | --- | --- |
| Web Search Plus | `web-search-plus` | Off | Provider keys are external; OpenAgentFleet has no app key vault yet | Uniform provider routing, search, and extraction MCP |
| Hound | `hound` | Off | Keyless | Separately installed local stdio search/extraction MCP |
| Donsetch | `donsetch` | Off | Keyless; optional BYOK stays outside OpenAgentFleet | Local stdio fetch, search, and crawl MCP |

The toggles are persisted independently. Reading status is side-effect-free;
first use of an enabled connector may download its pinned package through
`uvx` or `npx`. A connector is not an Agent tool merely because its global
toggle is on: the Agent's `mcp_ids` list controls actual injection into that
Agent's lead run.

## Exact launch pins

The runtime emits these exact local stdio commands when a connector is enabled,
launcher-ready, and selected by an Agent. Packaged macOS builds use the
absolute bundled `uvx` path resolved by the launcher probe for Web Search Plus
and Hound; source/development runs may resolve `uvx` or `npx` from their
configured environment instead:

```sh
uvx --from web-search-plus-mcp==3.6.0 web-search-plus-mcp serve
```

```sh
uvx --from hound-mcp==13.1.2 hound
```

```sh
npx --yes donsetch@2.1.0 mcp
```

The pinned package references are [Web Search Plus MCP
3.6.0](https://pypi.org/project/web-search-plus-mcp/3.6.0/), [Hound MCP
13.1.2](https://pypi.org/project/hound-mcp/13.1.2/), and [Donsetch
2.1.0](https://www.npmjs.com/package/donsetch/v/2.1.0). Their upstream source
references are [web-search-plus-mcp](https://github.com/robbyczgw-cla/web-search-plus-mcp),
[master-fetch/Hound](https://github.com/dondai44423/master-fetch), and
[donsetch](https://github.com/dondai44423/donsetch).

`uvx` and `npx` may populate their own package caches on first use. This is
expected and is why status calls this state **launcher ready**, not connector
verified. It only means the exact launch mechanism is available; a clean cache
or offline first use can still fail while fetching or initializing the package.
The application does not silently install any connector at first launch.
Donsetch additionally requires a working `npx` (Node.js); that launcher is not
bundled with the macOS package today.

## Measured MCP readiness

Development QA measured successful MCP `initialize` handshakes for the pinned
Web Search Plus and Hound connectors. That measurement is not reused as a
runtime health claim: status checks local launcher availability, while the
selected lead harness performs MCP startup and `initialize` for the actual
run. The protocol lifecycle is described by the [official MCP lifecycle
specification](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle).

The connectors run as local stdio MCPs. An optional HTTP bridge, if enabled in
a future deployment, is loopback-only and is not part of the default launch
path. Web Search Plus can route to providers that require keys, but those keys
remain external to OpenAgentFleet; the current app stores connector toggles and
non-secret status only. Missing provider configuration is an
unavailable-provider condition, not a successful search claim.

## No silent connector bridges

Web Search Plus, Hound, and Donsetch remain independent MCP servers. Enabling
one does not rewrite another connector's configuration or start it. There is
no silent WSP-to-Hound or Donsetch bridge in the first-install architecture.
If a future explicit bridge is added, it requires its own visible setting,
compatibility check, and QA evidence; it must not be inferred from independent
toggles.

## Agent injection contract

`mcp_ids` is per Agent, not daemon-global. OpenAgentFleet injects only the
launcher-ready MCP specs named by that Agent's list, such as
`web-search-plus`, `hound`, or `donsetch`. Unknown IDs fail before a run instead of being
silently ignored. A lead harness may still load MCPs from its own existing
user-global configuration; strict suppression of those external registries is
not yet implemented and the UI must not describe `mcp_ids` as a closed
allowlist until it is.
This applies to the supported structured adapters; an unrelated worker does
not inherit another Agent's grants. MCP server configuration is passed through
the adapter's native boundary, consistent with [OpenCode's local MCP server
model](https://opencode.ai/docs/mcp-servers/).

The lead harness initializes the selected MCP servers for the run. This makes
fresh search available through OpenCode's free starter model plus an enabled,
per-Agent Hound, Donsetch, or Web Search Plus selection. It does not mean that source
receipts, claim-bound citations, provider receipts, or Research Run
verification are complete. A normal harness answer that used native search or
MCP search must not be labeled as a verified research result.

## Operational boundary

Use the repository's release QA checklist for first-install, toggle
persistence, launcher/download, handshake, per-Agent injection, and no-bridge
checks. The connector controller intentionally does not become a credential
vault or a universal provider router behind the user's back.
