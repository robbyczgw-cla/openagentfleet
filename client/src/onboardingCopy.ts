/** User-visible first-run copy. Keep internals (harness, worker, MCP) out of these strings. */

export const productTagline =
  "Open-source AI coworkers that can use a real computer, learn your workflows, and collaborate — on infrastructure you control.";

export const emptyStateTitle = "Start chatting.";
export const emptyStateBody =
  "Your Agent can research, build, organize, or automate work on infrastructure you control.";

export const enginePickerEyebrow = "Your AI engines";
export const enginePickerTitle = "Choose an AI engine.";
export const enginePickerIntro = (hostNoun: string) =>
  `OpenAgentFleet uses the AI tools already available on this ${hostNoun}. Pick one for the workspace; you can change it later in Settings. Grok Build is the default. Pi is optional.`;
export const enginePickerAriaLabel = "Workspace AI engine";

export const engineFactLocalControl = "Local control";
export const engineFactLocalControlDetail = (hostNoun: string) =>
  `Agents, memory and approvals stay on this ${hostNoun}.`;
export const engineFactOneEngine = "One AI engine";
export const engineFactOneEngineDetail =
  "Every Agent uses this engine and model unless you change it later in Settings.";
export const engineFactOpenChoices = "Open choices";
export const engineFactOpenChoicesDetail =
  "Computer access, extra search, and extra Agents stay off until you need them.";

export const modelConfigEyebrow = "Optional model";
export const modelConfigTitle = "Choose the model that does the work.";
export const modelConfigNote =
  "Start with the recommended model. You can change it later in Settings.";

export const engineDescriptions = {
  grok_build:
    "Default. Local Grok Build, with a real computer for browser and desktop work when you need it.",
  codex_app_server:
    "Rich threads, approvals, and ChatGPT sign-in through Codex App Server.",
  opencode:
    "Bundled OpenCode using your local provider setup. Availability and billing depend on that provider.",
  pi: "Opt-in. Install the pi CLI and sign in with pi /login. This engine has no Agent Computer.",
} as const;

export const permissionsEyebrow = "Nothing extra required";
export const permissionsTitle = "Chat first. Add tools when you need them.";
export const permissionsIntro =
  "Create an Agent and start chatting. Computer access, extra search, and extra teammates are offered later — only when you use them.";
export const computerCardTitle = "Agent Computer";
export const computerCardDetail =
  "An isolated Linux desktop with Chromium, Terminal, and Files. Setup is offered the first time an Agent needs a computer — not during this step.";
export const computerCardStatus = "Set up when needed";
export const computerNote =
  "Finishing setup does not install or start a computer. That happens the first time you ask for browser or desktop work, or you open Computer View.";
export const computerNotePi =
  " The Pi engine cannot use the Agent Computer.";
export const controlCardTitle = "Your computer, your workspace";
export const controlCardDetail =
  "Chats, memory, attachments, and approvals stay in your local OpenAgentFleet workspace.";
export const micCardTitle = "Microphone & speech";
export const micCardDetailMac =
  "On Mac, dictation stays on-device when Apple supports the current locale. Browser clients use browser speech recognition when available, or the configured fallback.";

export const searchEyebrow = "Optional search";
export const searchTitle = "Add search later if it helps.";
export const searchIntroDefault =
  "You can chat without extra search. Built-in live search is available for some engines. Optional Web Search Plus, Hound, and Donsetch can be turned on here or later in Settings.";
export const searchIntroOpenCode =
  "OpenCode uses its configured model tools. Optional Web Search Plus, Hound, and Donsetch can be turned on here or later in Settings.";
export const searchIntroPi =
  "Pi has no built-in search. Optional Web Search Plus, Hound, and Donsetch stay off unless you turn them on here or later in Settings.";

export const readyEyebrow = "Ready";
export const readyTitle = "Create your first Agent and start chatting.";
export const readyBody =
  "Your workspace engine is set. Next, create an Agent and send a message. When an Agent needs a computer, the app will offer setup. When you need collaboration, create another Agent.";
export const readyEngineLabel = "AI engine";
export const readyComputerValue = "Offered when first needed";
export const finishButton = "Start chatting";
export const finishBusy = "Saving setup…";
export const skipSetup = "Skip setup";
export const noUsableEngine =
  "No usable AI engine is selected. Install or connect an engine before finishing setup.";
