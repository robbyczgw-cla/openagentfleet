import type { ConnectionState, MobileApproval, MobileRun, RemoteProfile } from "./api/types";

const TERMINAL_RUN_STATUS = new Set(["completed", "failed", "stopped", "blocked"]);

export function canControlDevice(profile: RemoteProfile | null): boolean {
  return profile?.device.scope_profile !== "observer";
}

export function connectionLabel(state: ConnectionState): string {
  if (state === "connected") return "Live";
  if (state === "degraded") return "Reconnecting";
  if (state === "connecting") return "Connecting";
  return "Offline";
}

export function activeRun(runs: MobileRun[] | undefined): MobileRun | undefined {
  return [...(runs || [])].reverse().find((run) => !TERMINAL_RUN_STATUS.has(run.status));
}

export function allowOptionID(approval: MobileApproval): string {
  const allow = (approval.options || []).find((option) => (option.kind || "").includes("allow") || option.optionId.includes("allow"));
  return allow?.optionId || approval.options?.[0]?.optionId || "";
}
