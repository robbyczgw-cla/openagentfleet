// Memory proposal state that does not touch React or the DOM, so it can be
// tested directly with node --test.

import { fallbackErrorText } from "./apiErrors.ts";

export type MemoryCategory = "fact" | "preference" | "instruction" | "project";

export type MemoryProposal = {
  id: string;
  bot_id: string;
  source_run_id: string;
  source_message_id: string;
  category: MemoryCategory;
  status: "pending" | "accepted" | "rejected";
  content: string;
  priority: number;
  expires_at?: string;
  memory_id?: string;
  created_at: string;
  updated_at: string;
};

export type ProposalDraft = {
  category: MemoryCategory;
  content: string;
  priority: number;
  expiry: string; // yyyy-mm-dd, empty means it never expires
};

export type ProposalPatch = {
  category?: MemoryCategory;
  content?: string;
  priority?: number;
  expires_at?: string | null;
};

// Mirrors internal/domain/memory.go.
export const MEMORY_CONTENT_MAX_BYTES = 4096;
export const MEMORY_PRIORITY_MIN = 1;
export const MEMORY_PRIORITY_MAX = 5;

export const MEMORY_CATEGORIES: [MemoryCategory, string][] = [
  ["fact", "Fact"],
  ["preference", "Preference"],
  ["instruction", "Instruction"],
  ["project", "Project"],
];

const encoder = new TextEncoder();

export function categoryLabel(category: string): string {
  return MEMORY_CATEGORIES.find(([value]) => value === category)?.[1] ?? category;
}

// The server stores an RFC3339 timestamp; a date input speaks yyyy-mm-dd.
export function expiryToInput(value: string | undefined): string {
  if (!value) return "";
  const match = /^(\d{4}-\d{2}-\d{2})/.exec(value.trim());
  return match ? match[1] : "";
}

export function expiryFromInput(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) return "";
  return `${trimmed}T00:00:00Z`;
}

export function draftFromProposal(proposal: MemoryProposal): ProposalDraft {
  return {
    category: proposal.category,
    content: proposal.content,
    priority: proposal.priority,
    expiry: expiryToInput(proposal.expires_at),
  };
}

export function validateProposalDraft(draft: ProposalDraft): string {
  if (!draft.content.trim()) return "Write what this agent should remember.";
  if (encoder.encode(draft.content).length > MEMORY_CONTENT_MAX_BYTES) {
    return "This note is too long. Trim it and try again.";
  }
  if (!Number.isInteger(draft.priority) || draft.priority < MEMORY_PRIORITY_MIN || draft.priority > MEMORY_PRIORITY_MAX) {
    return `Pick an importance between ${MEMORY_PRIORITY_MIN} and ${MEMORY_PRIORITY_MAX}.`;
  }
  if (draft.expiry && !/^\d{4}-\d{2}-\d{2}$/.test(draft.expiry.trim())) {
    return "Enter the expiry as a date, or leave it empty to keep it forever.";
  }
  return "";
}

// Sends only what the reviewer changed. An omitted expiry keeps the current
// one; an explicit null clears it, which is how the server tells them apart.
export function proposalPatch(draft: ProposalDraft, proposal: MemoryProposal): ProposalPatch | null {
  const patch: ProposalPatch = {};
  if (draft.category !== proposal.category) patch.category = draft.category;
  if (draft.content !== proposal.content) patch.content = draft.content;
  if (draft.priority !== proposal.priority) patch.priority = draft.priority;
  const expiry = expiryFromInput(draft.expiry);
  const current = proposal.expires_at ?? "";
  if (expiryToInput(expiry) !== expiryToInput(current)) {
    patch.expires_at = expiry ? expiry : null;
  }
  return Object.keys(patch).length === 0 ? null : patch;
}

export function proposalDraftChanged(draft: ProposalDraft, proposal: MemoryProposal): boolean {
  return proposalPatch(draft, proposal) !== null;
}

export function priorityLabel(priority: number): string {
  if (priority >= 5) return "Highest";
  if (priority === 4) return "High";
  if (priority === 3) return "Normal";
  if (priority === 2) return "Low";
  return "Lowest";
}

const PROPOSAL_ERROR_TEXT: [RegExp, string][] = [
  [/memory proposal not found/i, "This suggestion is no longer here. Someone may have handled it already."],
  [
    /already resolved|proposal resolved/i,
    "This suggestion was already accepted or dismissed.",
  ],
  [/duplicate/i, "This agent already remembers something with the same wording."],
  [/limit/i, "This agent has too many suggestions waiting. Handle a few first."],
  [/appears to contain a secret/i, "This wording looks like a password or key. Remove it before saving."],
  [/unsupported control characters/i, "Remove the unusual characters from this note."],
  [/priority must be between/i, `Pick an importance between ${MEMORY_PRIORITY_MIN} and ${MEMORY_PRIORITY_MAX}.`],
  [/expiry must be an RFC3339 timestamp/i, "Enter the expiry as a date, or leave it empty to keep it forever."],
];

export function describeProposalError(status: number, message: string): string {
  for (const [pattern, text] of PROPOSAL_ERROR_TEXT) {
    if (pattern.test(message)) return text;
  }
  return fallbackErrorText(status, message);
}

export function agentName(agents: { id: string; name: string }[], botID: string): string {
  return agents.find((agent) => agent.id === botID)?.name ?? botID;
}
