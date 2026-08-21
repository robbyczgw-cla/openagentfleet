export type ReviewKind = "approval" | "run";

export type ReviewOption = {
  optionId: string;
  name: string;
  kind: string;
};

export type ReviewItem = {
  kind: ReviewKind;
  bot_id: string;
  bot_name: string;
  conversation_id: string;
  run_id: string;
  created_at: string;
  id?: string;
  action?: string;
  status?: string;
  summary?: string;
  options?: ReviewOption[];
};

export function parseReviewQueue(value: unknown): ReviewItem[] {
  if (!value || typeof value !== "object") return [];
  const items = (value as { items?: unknown }).items;
  if (!Array.isArray(items)) return [];
  const parsed: ReviewItem[] = [];
  for (const raw of items) {
    const item = parseReviewItem(raw);
    if (item) parsed.push(item);
  }
  return sortReviewItems(parsed);
}

export function sortReviewItems(items: ReviewItem[]): ReviewItem[] {
  return [...items].sort((left, right) => {
    if (left.kind !== right.kind) {
      return left.kind === "approval" ? -1 : 1;
    }
    return reviewTimestamp(right.created_at) - reviewTimestamp(left.created_at);
  });
}

export function firstAllowOptionID(item: ReviewItem): string | undefined {
  const options = item.options ?? [];
  const allow = options.find((option) => {
    const kind = option.kind.toLowerCase();
    return kind.includes("allow") && !kind.includes("deny");
  });
  return allow?.optionId || options[0]?.optionId;
}

export function reviewApprovalStub(item: ReviewItem): {
  id: string;
  run_id: string;
  provider: string;
  action: string;
  payload: string;
  status: string;
  created_at: string;
} | null {
  if (item.kind !== "approval" || !item.id) return null;
  return {
    id: item.id,
    run_id: item.run_id,
    provider: "",
    action: item.action ?? "",
    payload: "",
    status: item.status ?? "pending",
    created_at: item.created_at,
  };
}

function parseReviewItem(value: unknown): ReviewItem | null {
  if (!value || typeof value !== "object") return null;
  const raw = value as Record<string, unknown>;
  const kind = raw.kind === "approval" || raw.kind === "run" ? raw.kind : null;
  const botID = readString(raw.bot_id);
  const runID = readString(raw.run_id);
  const conversationID = readString(raw.conversation_id);
  if (!kind || !botID || !runID || !conversationID) return null;
  if (kind === "approval" && !readString(raw.id)) return null;
  return {
    kind,
    bot_id: botID,
    bot_name: readString(raw.bot_name) || "Agent",
    conversation_id: conversationID,
    run_id: runID,
    created_at: readString(raw.created_at),
    id: readString(raw.id) || undefined,
    action: readString(raw.action) || undefined,
    status: readString(raw.status) || undefined,
    summary: readString(raw.summary) || undefined,
    options: parseReviewOptions(raw.options),
  };
}

function parseReviewOptions(value: unknown): ReviewOption[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const options: ReviewOption[] = [];
  for (const raw of value) {
    if (!raw || typeof raw !== "object") continue;
    const item = raw as Record<string, unknown>;
    const optionId = readString(item.optionId);
    const name = readString(item.name);
    if (!optionId || !name) continue;
    options.push({
      optionId,
      name,
      kind: readString(item.kind),
    });
  }
  return options.length > 0 ? options : undefined;
}

function readString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function reviewTimestamp(value: string): number {
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? 0 : timestamp;
}
