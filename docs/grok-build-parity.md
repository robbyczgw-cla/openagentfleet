# Grok Build parity contract

This project uses the installed Grok Build runtime instead of reimplementing
its agent loop. OpenAgentFleet owns the Bot product layer: durable conversations, run
state, approvals, remote access, computer lifecycle, cross-harness routing,
and the mobile-safe API. Grok Build remains the complete coding-agent engine.

The parity target is the locally installed Grok Build 1.0.0 (`3cd0d0cbcebe`)
and the current official documentation. Re-check this matrix whenever the
installed CLI changes.

## Feature-complete definition

OpenAgentFleet is Grok-Build feature-complete when a user can do all of the following
without leaving the product, except for explicitly interactive terminal
surfaces that may be opened in the native Grok TUI:

1. Start, resume, fork, rename, export, search, and delete Grok sessions.
2. Send prompts and receive streamed assistant, thought, plan, tool, command,
   file, diff, and permission updates.
3. Use the built-in tools: file read/write/edit, directory listing, ripgrep,
   shell/terminal, web search/fetch, todo/task management, memory, MCP
   discovery/use, and language-server tools when enabled.
4. Use plan mode with a visible plan review and approve/request-changes flow.
5. Use Ask, Auto, and Always-approve permission modes with allow/deny rules;
   OpenAgentFleet must keep its own outer approval policy in force.
6. Use sandbox profiles, worktrees, checkpoints/rewind, and VCS operations.
7. Use subagents, background tasks, task output, cancellation, roles, and
   personas.
8. Use custom models, model selection, reasoning effort, system/rule
   overrides, tool allow/deny lists, and session turn limits.
9. Use project rules (`AGENTS.md` family and Claude-compatible instruction
   files), skills, slash commands, plugins, hooks, MCP servers, and
   marketplaces.
10. Inspect the effective configuration and available models/extensions.
11. Authenticate, re-authenticate, use cached auth/API-key auth, and preserve
    the provider session without copying secrets into OpenAgentFleet messages/events.
12. Use the full native Grok TUI as an escape hatch when an ACP client cannot
    represent a picker, editor, dashboard, or other terminal-only surface.

“Feature-complete” does not mean cloning xAI proprietary UI assets or copying
the Grok TUI pixel-for-pixel. It means preserving the behavior and making the
capability reachable through an original OpenAgentFleet UI, the durable Go API, or the
native TUI fallback.

## Capability map

| Grok Build capability | OpenAgentFleet surface | Current state |
| --- | --- | --- |
| Interactive fullscreen TUI | Native Grok mode | Installed runtime; safe macOS launch bridge implemented |
| Headless plain/JSON/streaming JSON | Provider runner | Read-only command surface and fallback contract available |
| ACP JSON-RPC over stdio | Grok session service | Implemented and handshake/prompt-smoke verified |
| Session create/resume/continue/fork | Sessions API + UI | Create/resume persisted; native TUI exposes continue/resume/fork |
| Streaming message/thought/tool/plan events | Run event stream | Implemented; durable non-thought work events replay via SSE |
| File, search, shell, web, todo, task, memory, MCP, LSP tools | Tool/work inspector | Grok Build native web search/fetch is an explicit Agent lead choice (`live` by default after harness authentication); ACP tool activity is visible, richer controls pending |
| Ask/Auto/Always-approve | Approval panel + policy engine | OpenAgentFleet approval gate implemented; mode controls pending |
| Plan mode and review | Plan card + approval action | Plan updates visible; explicit plan verdict UI pending |
| Sandbox and worktrees | Run configuration | Runtime supports them; OpenAgentFleet configuration controls pending |
| Background tasks and subagents | Tasks inspector | Runtime events visible; task controls pending |
| Skills, plugins, hooks, marketplaces | Extensions screen | Read-only catalog surface implemented; management UI pending |
| Models, settings, rules, agent profiles | Settings/config screen | Models/config inspection and run effort/mode controls implemented; deep editing uses native TUI |
| MCP server management | Connector screen | Read-only MCP catalog implemented; management UI pending |
| Memory and session export/import | Bot Memory settings + runtime/session screens | Bot-scoped, user-reviewable memory UI/API with bounded run snapshots; Grok runtime session export/import remains pending |
| Native-only pickers/dashboard/settings | TUI bridge | Safe macOS TUI/dashboard/settings launch implemented |

OpenAgentFleet product surfaces now also include persistent conversation creation/selection/rename, local message/conversation search, Grok session search/export/delete endpoints, and explicit native TUI controls. The remaining entries in this matrix are intentionally tracked as parity work rather than presented as finished functionality.

The first OpenAgentFleet-native surface beyond the provider shell is now the
real Computer View: persistent Chromium in an isolated Xvfb display, a
Playwright/CDP bridge, live frames, and explicit human takeover.

## Provider boundary

The preferred path is:

```text
OpenAgentFleet UI / mobile client
        -> authenticated botd API
        -> durable OpenAgentFleet run + policy
        -> grok agent stdio (ACP)
        -> Grok Build runtime and its configured tools/extensions
```

The first implementation must not force `--always-approve`. A run is only
allowed to execute when `OPENAGENTFLEET_ALLOW_HARNESS_EXECUTION=1` is set, and tool or
external-action permissions remain subject to OpenAgentFleet policy. The runtime must
also support a separate explicit native-TUI launch path for capabilities ACP
does not expose as a structured API.

## Web-search boundary

Grok Build's own web search and web fetch remain the normal path when an
Agent's lead setting is `web_search: "live"`. The setting becomes usable after
Grok Build authentication; it is not promised on a first install before the
CLI can authenticate. This follows the [Grok Build CLI/ACP
contract](https://docs.x.ai/build/cli/headless-scripting). The
`web_search: "disabled"` choice must pass the runtime's native-disable
control and must not quietly swap in another service.

[Codex App Server](https://developers.openai.com/codex/app-server/) has the
same native live-search default in the OpenAgentFleet Agent contract. OpenCode
`1.18.10` is bundled as the third lead; its model picker includes the measured
free starter route `opencode/deepseek-v4-flash-free` and configured OpenCode Go
DeepSeek V4 Flash/Pro choices. The starter route cost `0` today, but
availability may change. Fresh search through OpenCode uses only the explicitly
granted MCP path.

[Web Search Plus](https://www.websearchplus.xyz/) is a complementary, opt-in
MCP integration rather than a replacement for native tools. Its exact launch
pin, external provider-key boundary, and the independent keyless
[Hound](https://github.com/dondai44423/master-fetch) sidecar are documented in
[`docs/search-connectors.md`](search-connectors.md). WSP and Hound are
independent toggles, off by default; there is no silent WSP-Hound bridge.
Native search or ordinary MCP activity does not by itself create the product's
future source-backed Research Run artifact or citation ledger.

## Primary sources

- https://docs.x.ai/build/overview
- https://docs.x.ai/build/cli/reference
- https://docs.x.ai/build/cli/headless-scripting
- https://docs.x.ai/build/modes-and-commands
- https://docs.x.ai/build/features/permissions
- https://docs.x.ai/build/features/plan-mode
- https://docs.x.ai/build/features/skills-plugins-marketplaces
- https://docs.x.ai/build/features/mcp-servers
- https://github.com/xai-org/grok-build
- https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/15-agent-mode.md
