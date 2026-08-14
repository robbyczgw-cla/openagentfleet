# Fresh-user native smoke checklist

This is the short macOS first-run test for OpenAgentFleet. Use a fresh,
disposable data directory and the packaged Tauri app. A Vite browser window is
not equivalent because the native app owns the local API credential.

## Native computer test prerequisite

The Agent Computer bind-mounts the data directory's `agent-workspace` and
Chromium profile into the selected runtime. On first use with Colima, the app
creates those directories and adds exactly those paths to the dedicated
`openagentfleet` profile automatically. Do not prepare a manual home or `/tmp`
mount. A disposable data directory is still recommended for QA, and the test
must record that the generated Colima config contains the two expected
writable paths only.

## The user path

1. **Choose an engine and model.** Grok Build, Codex App Server, or the local
   OpenCode fallback shows its real installed/login state. Select one, choose
   the model and reasoning depth in the same step, then continue.
2. **Optional permissions.** Confirm that microphone and Agent Computer access
   are lazy. No runtime, VM, OAuth, or connector setup is required here.
3. **Optional search.** Native engine search is the default route. Web Search
   Plus and Hound are separate local MCP options and remain off unless chosen.
4. **Start using OpenAgentFleet.** Finish setup and land in the single seeded
   Agent chat. The Agent Builder must not open automatically.

## Acceptance checks

| ID | Action | Pass condition |
| --- | --- | --- |
| FRESH-01 | Start with a fresh profile and inspect onboarding. | Exactly four steps appear: engine/model/reasoning, optional permissions, optional search, start. No Colima/Docker gate appears. |
| FRESH-02 | Select each available engine in turn. | The selected card has a visible and accessible check state. The model list changes with the harness, unavailable/auth-gated models are visibly disabled, and the reasoning options stay valid for that harness. |
| FRESH-03 | Toggle Hound and Web Search Plus on, then off. | Each confirmed toggle persists without a raw unexplained `401`; failure is inline and actionable. Search options remain optional. |
| FRESH-04 | Finish setup. | Onboarding closes, exactly one Agent and one default conversation are visible, and no Agent Builder opens. |
| FRESH-05 | Open Settings. | The global engine is labelled **Default engine**, not worker/lead. Computer runtime, tools, memory, automation, and advanced execution remain available in settings. |
| FRESH-06 | Send a harmless message. | The run uses the selected workspace engine, model and reasoning depth; the chat remains usable without configuring a worker. |
| FRESH-07 | Open Agent Computer. | App startup created no container. **Start Agent Computer** prepares the dedicated Colima mounts automatically when needed, starts the local Colima/Docker computer on demand, and exposes Chromium, Terminal, Files, frames, takeover, and the explicit **Agent control** switch. |
| FRESH-08 | Open New agent. | The normal form asks for identity and role. Advanced engine/permission/helper controls are collapsed and optional. |

## Functional follow-up

- Attach a file and an image by picker and drag-and-drop; verify the preview and
  message binding.
- Use the chat microphone only after choosing it; on the Mac app verify
  on-device dictation permission, and in a browser verify browser speech
  recognition or the configured STT fallback is clearly identified.
- Enable multiple conversations in Settings only if needed. It is off by
  default and does not replace Agent memory.
- Enable a worker pool only from Advanced settings. Workers are helpers, not
  additional Agents or conversations, and do not inherit hidden access.
- Run `go test ./...`, `pnpm --dir client build`, and `git diff --check` before
  recording a release result.

## Result template

```text
Build / commit:
macOS / architecture:
Fresh data directory: yes/no
Colima host mounts prepared automatically: yes/no/not applicable
Native Tauri app: yes/no
FRESH-01: pass/fail
FRESH-02: pass/fail
FRESH-03: pass/fail
FRESH-04: pass/fail
FRESH-05: pass/fail
FRESH-06: pass/fail
FRESH-07: not run / pass / fail
FRESH-08: pass/fail
Attachments / STT: not run / pass / fail
Evidence path or issue links:
```
