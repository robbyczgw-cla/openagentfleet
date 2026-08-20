export type PresenceState =
  | "idle"
  | "working"
  | "using_computer"
  | "waiting_for_approval"
  | "waiting_for_takeover"
  | "collaborating"
  | "failed";

export type AgentPresence = {
  state: PresenceState | string;
  label: string;
  detail?: string;
};

export type PresenceRun = {
  bot_id?: string;
  status: string;
  error?: string;
};

export type PresenceHandoff = {
  source_bot_id: string;
  target_bot_id: string;
  status?: string;
};

export type PresenceComputer = {
  running: boolean;
  takeover: boolean;
  agent_control?: boolean;
};

export function deriveAgentPresence(input: {
  botID: string;
  run?: PresenceRun | null;
  computer?: PresenceComputer | null;
  handoffs?: PresenceHandoff[];
}): AgentPresence {
  const run = input.run;
  const computer = input.computer;
  const active = Boolean(run && runActive(run.status));
  if (run?.status === "waiting_for_approval") {
    return {
      state: "waiting_for_approval",
      label: "Needs approval",
      detail: "Waiting for an explicit Allow or Deny",
    };
  }
  if (active && computer?.takeover) {
    return {
      state: "waiting_for_takeover",
      label: "Needs takeover",
      detail: "Human control of the Agent Computer",
    };
  }
  if (active && computer?.running && (computer.agent_control || computer.takeover)) {
    return {
      state: "using_computer",
      label: "Using computer",
      detail: "Browser or desktop work on the isolated computer",
    };
  }
  if (collaborating(input.botID, input.handoffs ?? [])) {
    return {
      state: "collaborating",
      label: "Collaborating",
      detail: active ? "Working with a teammate" : "Asked another Agent",
    };
  }
  if (run && (run.status === "queued" || run.status === "running")) {
    return {
      state: "working",
      label: run.status === "queued" ? "Queued" : "Working",
      detail: `Run ${run.status}`,
    };
  }
  if (run && (run.status === "failed" || run.status === "blocked")) {
    return {
      state: "failed",
      label: "Failed",
      detail: run.error || `Last run ${run.status}`,
    };
  }
  return { state: "idle", label: "Idle" };
}

function runActive(status: string) {
  return status === "queued" || status === "running" || status === "waiting_for_approval";
}

function collaborating(botID: string, handoffs: PresenceHandoff[]) {
  return handoffs.some((handoff) => {
    const status = handoff.status ?? "";
    if (status !== "queued" && status !== "running" && status !== "waiting") {
      return false;
    }
    return handoff.source_bot_id === botID || handoff.target_bot_id === botID;
  });
}

export function engineShortLabel(harness?: string) {
  switch (harness) {
    case "grok":
    case "grok_build":
      return "Grok";
    case "codex_app_server":
      return "Codex";
    case "opencode":
      return "OpenCode";
    case "pi":
      return "Pi";
    default:
      return "";
  }
}
