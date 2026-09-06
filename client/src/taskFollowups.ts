// Retry and revise state that does not touch React or the DOM, so it can be
// tested directly with node --test.

import { fallbackErrorText } from "./apiErrors.ts";

export type FollowupKind = "retry" | "revise";

export type TaskAttachment = {
  id: string;
  name: string;
  media_type: string;
  size: number;
};

export type TaskInput = {
  brief: string;
  agent_id: string;
  attachments: TaskAttachment[];
};

export type FollowupIntent = {
  kind: FollowupKind;
  taskID: string;
  agentID: string;
  brief: string;
  attachmentIDs: string[];
};

export type FollowupRequest = {
  agent_id: string;
  brief?: string;
  attachment_ids: string[];
};

export type IdempotencyState = { key: string; fingerprint: string };

// Mirrors store.MaxTaskAttempts and the attachment cap in the tasks handler.
export const MAX_TASK_ATTEMPTS = 10;
export const MAX_FOLLOWUP_ATTACHMENTS = 10;

export function emptyTaskInput(): TaskInput {
  return { brief: "", agent_id: "", attachments: [] };
}

// A retry sends the original brief untouched. Only a revision carries edited
// wording, so only a revision puts a brief in the body.
export function followupRequestBody(intent: FollowupIntent): FollowupRequest {
  const body: FollowupRequest = { agent_id: intent.agentID, attachment_ids: [...intent.attachmentIDs] };
  if (intent.kind === "revise") body.brief = intent.brief.trim();
  return body;
}

// Two submissions describe the same intent when every part the server records
// is the same. Attachment order is kept because the server hashes the list.
export function followupFingerprint(intent: FollowupIntent): string {
  return JSON.stringify([
    intent.kind,
    intent.taskID,
    intent.agentID,
    intent.kind === "revise" ? intent.brief.trim() : "",
    intent.attachmentIDs,
  ]);
}

// Reuses the key while the intent is unchanged, so a resend after a dropped
// connection lands on the run the first attempt may already have created. Any
// edit produces a new key and therefore a new run.
export function nextIdempotency(
  current: IdempotencyState | null,
  intent: FollowupIntent,
  mint: () => string,
): IdempotencyState {
  const fingerprint = followupFingerprint(intent);
  if (current && current.fingerprint === fingerprint) return current;
  return { key: mint(), fingerprint };
}

export function validateFollowupIntent(intent: FollowupIntent, input: TaskInput): string {
  if (!intent.agentID) return "This task has no agent to run it again.";
  if (intent.agentID !== input.agent_id) {
    return "A follow-up runs with the same agent as the original task.";
  }
  if (intent.kind === "revise" && !intent.brief.trim()) {
    return "Say what this agent should do differently.";
  }
  if (intent.attachmentIDs.length > MAX_FOLLOWUP_ATTACHMENTS) {
    return `Send at most ${MAX_FOLLOWUP_ATTACHMENTS} files.`;
  }
  const offered = new Set(input.attachments.map((attachment) => attachment.id));
  if (intent.attachmentIDs.some((id) => !offered.has(id))) {
    return "Only files from the original request can be sent again.";
  }
  if (new Set(intent.attachmentIDs).size !== intent.attachmentIDs.length) {
    return "Each file can only be sent once.";
  }
  return "";
}

export function attemptLabel(attempt: number, kind: string): string {
  const position = `Attempt ${Math.max(1, attempt)} of ${MAX_TASK_ATTEMPTS}`;
  if (kind === "retry") return `${position} · same request, run again`;
  if (kind === "revise") return `${position} · reworded request`;
  return `${position} · original request`;
}

export function attemptsLeft(attempt: number): number {
  return Math.max(0, MAX_TASK_ATTEMPTS - Math.max(1, attempt));
}

const FOLLOWUP_ERROR_TEXT: [RegExp, string][] = [
  [
    /idempotency key conflicts/i,
    "This request was already sent with different wording or files. Reopen the follow-up and send it again.",
  ],
  [
    /attempt limit reached/i,
    `This task has already been run ${MAX_TASK_ATTEMPTS} times. Start a new task instead.`,
  ],
  [
    /status does not allow this followup/i,
    "This task moved on while the panel was open. Close the follow-up and check its current state.",
  ],
  [/agent_id must match the original task/i, "A follow-up runs with the same agent as the original task."],
  [/retry uses the original brief/i, "Running it again keeps the original request. Use Revise to change the wording."],
  [/original attachment content is unavailable/i, "A file from the original request is no longer on disk."],
  [/attachment does not belong to the original task/i, "Only files from the original request can be sent again."],
  [/task not found/i, "This task no longer exists."],
];

export function describeFollowupError(status: number, message: string): string {
  for (const [pattern, text] of FOLLOWUP_ERROR_TEXT) {
    if (pattern.test(message)) return text;
  }
  return fallbackErrorText(status, message);
}

export function fileSizeLabel(size: number): string {
  if (size < 1024) return `${Math.max(0, size)} B`;
  return `${Math.max(1, Math.ceil(size / 1024))} KB`;
}

export function randomIdempotencyKey(): string {
  const source = globalThis.crypto;
  if (source && typeof source.randomUUID === "function") return source.randomUUID();
  return `k-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}
