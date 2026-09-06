// Project state that does not touch React or the DOM, so it can be tested
// directly with node --test.

import { fallbackErrorText } from "./apiErrors.ts";

export type ProjectMember = { agent_id: string; name: string; title: string };

export type Project = {
  id: string;
  name: string;
  brief: string;
  status: "active" | "archived";
  version: number;
  brief_revision: number;
  members: ProjectMember[];
  created_at: string;
  updated_at: string;
  archived_at?: string;
};

export type ProjectBriefRevision = {
  project_id: string;
  revision: number;
  brief: string;
  created_at: string;
};

export type ProjectAssociation = {
  project_id: string;
  project_name: string;
  subject_type: "conversation" | "routine";
  subject_id: string;
  created_at: string;
  updated_at: string;
};

export type ProjectTaskSnapshot = {
  run_id: string;
  project_id: string;
  project_name: string;
  brief_revision: number;
  brief?: string;
  created_at: string;
};

export type ProjectDraft = { name: string; brief: string; agentIDs: string[] };

// Mirrors internal/domain/projects.go. Names and briefs are counted in bytes
// there, so the checks below count bytes too.
export const PROJECT_NAME_MAX_BYTES = 160;
export const PROJECT_BRIEF_MAX_BYTES = 64 * 1024;
export const PROJECT_MEMBERS_MAX = 128;

const encoder = new TextEncoder();

export function byteLength(value: string): number {
  return encoder.encode(value).length;
}

export function emptyProjectDraft(): ProjectDraft {
  return { name: "", brief: "", agentIDs: [] };
}

export function draftFromProject(project: Project): ProjectDraft {
  return {
    name: project.name,
    brief: project.brief,
    agentIDs: project.members.map((member) => member.agent_id),
  };
}

// The server rejects a bad draft too. Checking here keeps the reason next to
// the field instead of turning a typo into a failed request.
export function validateProjectDraft(draft: ProjectDraft): string {
  const name = draft.name.trim();
  if (!name) return "Give this project a name.";
  if (byteLength(name) > PROJECT_NAME_MAX_BYTES) {
    return `Shorten the name to ${PROJECT_NAME_MAX_BYTES} characters or fewer.`;
  }
  if (byteLength(draft.brief) > PROJECT_BRIEF_MAX_BYTES) {
    return "The brief is too long. Trim it and try again.";
  }
  if (draft.agentIDs.length === 0) return "Pick at least one agent for this project.";
  if (draft.agentIDs.length > PROJECT_MEMBERS_MAX) {
    return `A project can hold at most ${PROJECT_MEMBERS_MAX} agents.`;
  }
  if (new Set(draft.agentIDs).size !== draft.agentIDs.length) {
    return "An agent can only be on the project once.";
  }
  return "";
}

export function projectDraftChanged(draft: ProjectDraft, project: Project): boolean {
  const saved = draftFromProject(project);
  if (draft.name.trim() !== saved.name.trim()) return true;
  if (draft.brief !== saved.brief) return true;
  const before = [...saved.agentIDs].sort();
  const after = [...draft.agentIDs].sort();
  return before.length !== after.length || before.some((id, index) => id !== after[index]);
}

export function removedMembers(draft: ProjectDraft, project: Project): ProjectMember[] {
  const keep = new Set(draft.agentIDs);
  return project.members.filter((member) => !keep.has(member.agent_id));
}

// Only an active project whose members include this agent can take new work
// from it, so those are the only ones worth offering.
export function assignableProjects(projects: Project[], agentID: string): Project[] {
  return projects.filter(
    (project) =>
      project.status === "active" &&
      (!agentID || project.members.some((member) => member.agent_id === agentID)),
  );
}

export function toggleMember(agentIDs: string[], agentID: string, on: boolean): string[] {
  const without = agentIDs.filter((id) => id !== agentID);
  return on ? [...without, agentID] : without;
}

const PROJECT_ERROR_TEXT: [RegExp, string][] = [
  [
    /project version conflict/i,
    "Someone else changed this project while you were editing. Load the latest copy, then apply your changes again.",
  ],
  [/project is archived/i, "This project is archived, so it can no longer be changed or take new work."],
  [
    /agent is not a project member/i,
    "That agent is not on this project. Add the agent to the project first.",
  ],
  [/project agent not found/i, "That agent no longer exists."],
  [
    // The same 503 the GitHub workspace can see: the local service has no
    // controller credential, which is not something a person sets in settings.
    /configure a controller token/i,
    "Controller authentication is unavailable. Restart the desktop app; a development server needs a controller token.",
  ],
  [/project not found/i, "This project no longer exists. It may have been removed."],
  [/project association subject not found/i, "This conversation or routine no longer exists."],
  [
    /project association not found/i,
    "This conversation or routine is not on a project yet.",
  ],
];

// Turns a server message into something a person can act on, and keeps the
// original text when there is nothing better to say.
export function describeProjectError(status: number, message: string): string {
  for (const [pattern, text] of PROJECT_ERROR_TEXT) {
    if (pattern.test(message)) return text;
  }
  return fallbackErrorText(status, message);
}

export function isProjectVersionConflict(status: number, message: string): boolean {
  return status === 409 && /project version conflict/i.test(message);
}

export function projectStatusLabel(project: Project): string {
  return project.status === "archived" ? "Archived" : "Active";
}

export function briefRevisionLabel(revision: number): string {
  return `Brief version ${revision}`;
}

// The snapshot is copied when the task is queued, which can be well before it
// runs, and it never follows later edits.
export function snapshotLabel(snapshot: ProjectTaskSnapshot): string {
  return `${snapshot.project_name} · ${briefRevisionLabel(snapshot.brief_revision)}`;
}
