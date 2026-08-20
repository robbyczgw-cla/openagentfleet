import type { ReactNode } from "react";
import "./collaboration.css";

export type HandoffStatus =
  | "queued"
  | "running"
  | "waiting"
  | "completed"
  | "failed"
  | "cancelled";

export type CollaborationHandoff = {
  id: string;
  source_bot_id: string;
  source_conversation_id: string;
  source_message_id: string;
  target_bot_id: string;
  target_conversation_id: string;
  target_message_id: string;
  target_run_id?: string;
  content: string;
  created_at: string;
  status?: string;
  mode?: string;
  depth?: number;
  result?: string;
};

export type CollaborationMessage = {
  id: string;
  role: string;
  content: string;
  created_at: string;
  kind?: string;
  author_bot_id?: string;
  mentions?: string[];
  handoff_id?: string;
  status?: string;
  mode?: string;
  depth?: number;
  target_conversation_id?: string;
  result?: string;
};

function isPresent<T>(value: T | undefined): value is T {
  return value !== undefined;
}

function patchDefined<T extends object>(base: T, patch: Partial<T>): T {
  const next = { ...base };
  for (const key of Object.keys(patch) as (keyof T)[]) {
    const value = patch[key];
    if (isPresent(value)) next[key] = value as T[keyof T];
  }
  return next;
}

export function handoffStatusLabel(status?: string): string {
  switch (status) {
    case "queued":
    case "running":
      return "Asked";
    case "waiting":
      return "Waiting";
    case "completed":
      return "Done";
    case "failed":
      return "Failed";
    case "cancelled":
      return "Cancelled";
    default:
      return "Asked";
  }
}

export function handoffStatusTone(status?: string): string {
  switch (status) {
    case "waiting":
      return "is-waiting";
    case "completed":
      return "is-done";
    case "failed":
      return "is-failed";
    case "cancelled":
      return "is-cancelled";
    default:
      return "";
  }
}

function fieldsFromHandoff(handoff: CollaborationHandoff) {
  return {
    status: handoff.status,
    mode: handoff.mode,
    depth: handoff.depth,
    target_conversation_id: handoff.target_conversation_id,
    result: handoff.result,
  };
}

function mergeHandoffIntoMessages(
  messages: CollaborationMessage[],
  handoff: CollaborationHandoff,
): CollaborationMessage[] {
  const patch = fieldsFromHandoff(handoff);
  return messages.map((item) => {
    if (
      item.handoff_id !== handoff.id &&
      item.id !== handoff.source_message_id &&
      item.id !== handoff.target_message_id
    ) {
      return item;
    }
    return patchDefined(item, patch);
  });
}

function mergeHandoffRecords(
  handoffs: CollaborationHandoff[],
  incoming: CollaborationHandoff,
): CollaborationHandoff[] {
  const exists = handoffs.some((item) => item.id === incoming.id);
  if (!exists) return [...handoffs, incoming];
  return handoffs.map((item) =>
    item.id === incoming.id ? patchDefined(item, incoming) : item,
  );
}

function createdHandoffMessage(
  conversationID: string,
  handoff: CollaborationHandoff,
): CollaborationMessage | null {
  const shared = {
    content: handoff.content,
    created_at: handoff.created_at,
    kind: "handoff",
    handoff_id: handoff.id,
    status: handoff.status ?? "queued",
    mode: handoff.mode,
    depth: handoff.depth,
    target_conversation_id: handoff.target_conversation_id,
    result: handoff.result,
  };
  if (conversationID === handoff.target_conversation_id) {
    return {
      id: handoff.target_message_id,
      role: "user",
      author_bot_id: handoff.source_bot_id,
      mentions: [handoff.target_bot_id],
      ...shared,
    };
  }
  if (conversationID === handoff.source_conversation_id) {
    return {
      id: handoff.source_message_id,
      role: "user",
      mentions: [handoff.target_bot_id],
      ...shared,
    };
  }
  return null;
}

export function applyHandoffStreamEvent<
  T extends {
    conversation: { id: string };
    messages?: CollaborationMessage[] | null;
    handoffs?: CollaborationHandoff[] | null;
  },
>(current: T, handoff: CollaborationHandoff, eventType: string): T {
  let messages = [...(current.messages ?? [])];
  if (eventType === "handoff.created") {
    const incoming = createdHandoffMessage(current.conversation.id, handoff);
    if (incoming && !messages.some((item) => item.id === incoming.id)) {
      messages.push(incoming);
    }
  }
  messages = mergeHandoffIntoMessages(messages, {
    ...handoff,
    status:
      handoff.status ??
      (eventType === "handoff.completed" ? "completed" : handoff.status),
  });

  const next = { ...current, messages: messages as T["messages"] };
  if (current.handoffs) {
    next.handoffs = mergeHandoffRecords(current.handoffs, handoff) as T["handoffs"];
  }
  return next;
}

export function HandoffMessageChrome(props: {
  inbound: boolean;
  teammateName: string;
  status?: string;
  targetConversationId?: string;
  onOpenConversation?: (conversationId: string) => void;
}): ReactNode {
  const { inbound, teammateName, status, targetConversationId, onOpenConversation } =
    props;
  const canOpen = Boolean(targetConversationId && onOpenConversation);
  return (
    <span className="collab-meta">
      <span>
        {inbound
          ? `Teammate ${teammateName} asked`
          : `Asked ${teammateName}`}
      </span>
      <span className={`collab-chip ${handoffStatusTone(status)}`}>
        {handoffStatusLabel(status)}
      </span>
      {canOpen ? (
        <button
          className="collab-open"
          type="button"
          onClick={() => onOpenConversation!(targetConversationId!)}
        >
          Open teammate chat
        </button>
      ) : targetConversationId ? (
        <span className="collab-target-id">Open teammate chat · {targetConversationId}</span>
      ) : null}
    </span>
  );
}

export function HandoffResultUpdate(props: {
  teammateName: string;
  content?: string;
  result?: string;
  status?: string;
}): ReactNode {
  const body = (props.result || props.content || "").trim();
  return (
    <>
      <div className="message-meta">
        <span>Agent update</span>
        <span className={`collab-chip ${handoffStatusTone(props.status ?? "completed")}`}>
          {handoffStatusLabel(props.status ?? "completed")}
        </span>
      </div>
      <div className="collab-result">
        <span className="collab-result-label">
          {props.teammateName} {handoffStatusLabel(props.status ?? "completed").toLowerCase()}
        </span>
        {body || "Teammate finished."}
      </div>
    </>
  );
}
