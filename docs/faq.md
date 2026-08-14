# OpenAgentFleet FAQ

This page answers the questions that usually come up after the short README.
For exact setup commands and engineering contracts, use the linked guides in
the [documentation map](README.md).

## Product basics

### Is OpenAgentFleet a cloud service?

The workspace is local-first. The Go controller, SQLite state, conversations,
memory, approvals and Agent Computer lifecycle run on your Mac. The selected
AI provider may still send prompts to its own remote inference service through
its CLI or harness. OpenAgentFleet does not turn that provider traffic into a
local model.

### Do I need a Grok or Codex account?

No single provider is required by the product. Grok Build and Codex App Server
are optional lead engines. Bundled OpenCode is the local-provider fallback,
but the models and accounts available to OpenCode still depend on the local
provider configuration you choose.

### What is an Agent?

An Agent is the identity you chat with: its name, role, system context,
memory, enabled connectors and Computer policy. The default is one Agent and
one conversation. Additional conversations and more advanced engine settings
are available without changing that simple starting point.

### What is a worker?

A worker is an optional helper below the selected AI engine. The engine can
delegate a small, explicit task with its own model, reasoning level, budget and
permissions. A worker is not a second Agent and does not receive hidden
credentials or unrestricted host access.

## Agent Computer

### What does the Agent Computer contain?

It is a separate, non-root Linux desktop with Chromium, Xfce, Terminal and
Files. Browser and desktop actions happen there, and the app shows its live
screen. You can stop the run or take control when the agent needs a human for
a sign-in, CAPTCHA, payment or another sensitive step.

### Is the whole agent isolated from macOS?

Not yet. The Agent Computer is the browser and desktop boundary. Provider CLIs
and harnesses currently run as normal processes on macOS. A process already
running as the same macOS user is outside the Computer boundary. See the
[security model](architecture.md) for the full scope and residual risks.

### Which container runtime should I use on a Mac?

Colima plus Docker is the recommended open-source route. OpenAgentFleet uses a
dedicated profile and only its own workspace/profile mounts. Docker Desktop
and other Docker-compatible contexts are fallbacks. Apple Container and Lume
are experimental until they pass the same Chromium, desktop-frame, takeover
and approval tests.

### Why does the Computer start slowly?

The Computer is lazy: it is not started just because the app opened. The first
Computer start may need to start Colima, create the image and launch Xfce and
Chromium. Later starts reuse the dedicated runtime and profile. The app should
show a live desktop frame before it labels the Computer ready.

### The Computer says it is ready, but I see a blank or stale frame. What do I do?

Stop the Computer in the app and start it again. If it persists, check the
dedicated Docker context and container without exposing the Docker socket:

```sh
docker context show
docker --context colima-openagentfleet ps
docker --context colima-openagentfleet logs openagentfleet-agent-computer
```

The expected container includes Xfce, Chromium and the Computer view service.
If Colima or Docker is missing, use the install command shown by the app and
retry rather than switching the global Docker context manually. Report the
runtime, the visible error and the relevant run ID; do not include provider
tokens or passwords.

## Approvals, passwords and privacy

### What does “approve” mean?

For Grok Build and Codex App Server, sensitive actions can be routed through
the local controller approval broker. You see the request before it runs and
can allow or reject it. OpenCode currently keeps its own safe permission
handling rather than using the same broker; the UI labels that boundary.

### Can I give the agent my password or one-time code?

Use the native secure handoff while you have control of the Computer. Do not
paste passwords, passkeys, CAPTCHA answers, payment details or one-time codes
into the chat composer. The secure handoff is kept out of chat state, Teach
recordings and model context.

### Where are chats and memory stored?

The controller stores conversations, agent memory, transcripts, approvals and
run artifacts locally. Provider inference and any connector you explicitly
enable have their own network and retention policies. Review those policies
before enabling a provider or MCP connector.

## Search, files and phone access

### Is web search always on?

Native search is available to engines that support it. Web Search Plus and
Hound are separate optional MCP connectors, off until you enable and configure
them for an Agent. Connector credentials are configured locally and are not
placed in chat messages.

### Can I attach files and images?

Yes. Use the composer attachment control or drag files and images into the
chat. The exact file-size and provider limits still apply. Only attach data
you intend to make available to the selected Agent and provider.

### Can I use OpenAgentFleet from an iPhone or Android phone?

The Mac remains the execution host. Mobile clients are remote clients for the
Mac controller; they do not run Docker, Chromium or provider CLIs on the
phone. Pairing is designed for a private Tailscale or equivalent network and
is optional for the macOS alpha.

## Distribution and support

### Can I download a signed Mac app?

Not yet. The current alpha is source-build software for Apple Silicon macOS.
Signing, notarization and a public release asset are separate release gates;
the homepage and README will change when they are genuinely complete.

### How do I report a security problem?

Do not open a public issue for an unpatched vulnerability. Follow
[SECURITY.md](../SECURITY.md) for the private reporting route, scope and
expected information. For normal bugs, use the repository issue templates.

### Where should I look next?

- [Build and host lifecycle](mac-host-install.md)
- [Computer backend details](macos-agent-computer-backends.md)
- [Agent and memory model](agent-model.md)
- [Lead and worker architecture](lead-worker-architecture.md)
- [Fresh-user smoke test](fresh-user-smoke-test.md)
- [Security and architecture](architecture.md)
