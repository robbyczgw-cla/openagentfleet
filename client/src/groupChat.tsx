import { FormEvent, useEffect, useMemo, useState } from "react";
import "./groupChat.css";

export type GroupChatBot = {
  id: string;
  name: string;
  title: string;
};

export type GroupChatApiFetch = (
  path: string,
  init?: RequestInit,
) => Promise<Response>;

type GroupMember = {
  group_id: string;
  bot_id: string;
  name?: string;
  title?: string;
};

type Group = {
  id: string;
  title: string;
  created_at: string;
  updated_at: string;
  agent_ids?: string[];
  members?: GroupMember[];
};

type GroupMessage = {
  id: string;
  group_id: string;
  role: string;
  content: string;
  created_at: string;
  kind?: string;
  author_bot_id?: string;
  mentions?: string[];
};

type GroupRun = {
  id: string;
  group_id: string;
  bot_id: string;
  message_id: string;
  status: string;
  prompt: string;
  error?: string;
  created_at: string;
  updated_at: string;
};

type GroupChatPanelProps = {
  bots: GroupChatBot[];
  apiFetch: GroupChatApiFetch;
  onClose: () => void;
};

async function readError(response: Response): Promise<string> {
  try {
    const payload: unknown = await response.json();
    if (
      payload &&
      typeof payload === "object" &&
      "error" in payload &&
      typeof (payload as { error: unknown }).error === "string"
    ) {
      return (payload as { error: string }).error;
    }
  } catch {
    // Fall through to status text.
  }
  return `Request failed (${response.status})`;
}

function memberLabel(member: GroupMember, bots: GroupChatBot[]): string {
  if (member.name) return member.name;
  const bot = bots.find((item) => item.id === member.bot_id);
  return bot?.name || member.bot_id;
}

function messageAuthor(
  message: GroupMessage,
  members: GroupMember[],
  bots: GroupChatBot[],
): string {
  if (!message.author_bot_id) return "You";
  const member = members.find((item) => item.bot_id === message.author_bot_id);
  if (member) return memberLabel(member, bots);
  const bot = bots.find((item) => item.id === message.author_bot_id);
  return bot?.name || message.author_bot_id;
}

function runStatusClass(status: string): string {
  const normalized = status.toLowerCase();
  if (
    normalized === "queued" ||
    normalized === "running" ||
    normalized === "completed" ||
    normalized === "failed" ||
    normalized === "cancelled"
  ) {
    return `is-${normalized}`;
  }
  return "";
}

export function GroupChatPanel({ bots, apiFetch, onClose }: GroupChatPanelProps) {
  const [groups, setGroups] = useState<Group[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [messages, setMessages] = useState<GroupMessage[]>([]);
  const [runs, setRuns] = useState<GroupRun[]>([]);
  const [title, setTitle] = useState("");
  const [createAgentIDs, setCreateAgentIDs] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [mentionIDs, setMentionIDs] = useState<string[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const selected = groups.find((item) => item.id === selectedID) ?? null;
  const members = selected?.members ?? [];
  const activeRuns = useMemo(
    () =>
      runs.filter((run) => {
        const status = run.status.toLowerCase();
        return status === "queued" || status === "running";
      }),
    [runs],
  );

  async function loadGroups(preferID?: string | null) {
    const response = await apiFetch("/api/groups");
    if (!response.ok) throw new Error(await readError(response));
    const payload = (await response.json()) as { groups?: Group[] };
    const next = payload.groups ?? [];
    setGroups(next);
    setSelectedID((current) => {
      const wanted = preferID ?? current;
      if (wanted && next.some((item) => item.id === wanted)) return wanted;
      return next[0]?.id ?? null;
    });
  }

  async function loadThread(groupID: string) {
    const response = await apiFetch(`/api/groups/${groupID}/messages`);
    if (!response.ok) throw new Error(await readError(response));
    const payload = (await response.json()) as {
      messages?: GroupMessage[];
      runs?: GroupRun[];
    };
    setMessages(payload.messages ?? []);
    setRuns(payload.runs ?? []);
  }

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        await loadGroups();
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Could not load groups");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [apiFetch]);

  useEffect(() => {
    if (!selectedID) {
      setMessages([]);
      setRuns([]);
      return;
    }
    let cancelled = false;
    void (async () => {
      try {
        await loadThread(selectedID);
        if (!cancelled) setError(null);
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : "Could not load messages");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [apiFetch, selectedID]);

  useEffect(() => {
    if (!selectedID || activeRuns.length === 0) return;
    const timer = window.setInterval(() => {
      void loadThread(selectedID).catch(() => undefined);
    }, 2000);
    return () => window.clearInterval(timer);
  }, [apiFetch, selectedID, activeRuns.length]);

  useEffect(() => {
    setMentionIDs(members.map((member) => member.bot_id));
  }, [selectedID]);

  function toggleCreateAgent(id: string) {
    setCreateAgentIDs((current) =>
      current.includes(id)
        ? current.filter((item) => item !== id)
        : [...current, id],
    );
  }

  function toggleMention(id: string) {
    setMentionIDs((current) =>
      current.includes(id)
        ? current.filter((item) => item !== id)
        : [...current, id],
    );
  }

  async function createGroup(event: FormEvent) {
    event.preventDefault();
    if (createAgentIDs.length < 2) {
      setError("Pick at least two agents.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const response = await apiFetch("/api/groups", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          title: title.trim(),
          agent_ids: createAgentIDs,
        }),
      });
      if (!response.ok) throw new Error(await readError(response));
      const payload = (await response.json()) as { group?: Group };
      setTitle("");
      setCreateAgentIDs([]);
      await loadGroups(payload.group?.id ?? null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create group");
    } finally {
      setBusy(false);
    }
  }

  async function sendMessage(event: FormEvent) {
    event.preventDefault();
    if (!selectedID || !draft.trim()) return;
    const mentions =
      mentionIDs.length > 0
        ? mentionIDs
        : members.map((member) => member.bot_id);
    if (mentions.length === 0) {
      setError("Select at least one member to mention.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const response = await apiFetch(`/api/groups/${selectedID}/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          content: draft.trim(),
          mention_bot_ids: mentions,
        }),
      });
      if (!response.ok) throw new Error(await readError(response));
      setDraft("");
      await loadThread(selectedID);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not send message");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="dialog-backdrop"
      role="dialog"
      aria-modal="true"
      aria-labelledby="group-chat-title"
    >
      <section className="group-chat-dialog">
        <aside className="group-chat-sidebar">
          <div className="group-chat-heading">
            <div>
              <div className="eyebrow">Workspace</div>
              <h2 id="group-chat-title">Group chat</h2>
            </div>
            <button
              type="button"
              className="icon-button dialog-close"
              onClick={onClose}
              aria-label="Close group chat"
            >
              ×
            </button>
          </div>
          {groups.length === 0 ? (
            <p className="group-chat-empty">No groups yet.</p>
          ) : (
            <ul className="group-chat-list">
              {groups.map((group) => (
                <li key={group.id}>
                  <button
                    type="button"
                    className={`group-chat-item ${group.id === selectedID ? "active" : ""}`}
                    onClick={() => setSelectedID(group.id)}
                  >
                    <strong>{group.title || "Group chat"}</strong>
                    <span>
                      {(group.members ?? []).map((member) => memberLabel(member, bots)).join(", ") ||
                        `${group.agent_ids?.length ?? 0} agents`}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          <details className="group-chat-create">
            <summary>New group</summary>
            <form onSubmit={(event) => void createGroup(event)}>
              <label>
                Title
                <input
                  value={title}
                  onChange={(event) => setTitle(event.target.value)}
                  placeholder="Group title"
                />
              </label>
              <fieldset className="group-chat-agents">
                <legend>Agents</legend>
                {bots.length < 2 ? (
                  <p className="group-chat-empty">Need at least two agents.</p>
                ) : (
                  bots.map((bot) => (
                    <label key={bot.id}>
                      <input
                        type="checkbox"
                        checked={createAgentIDs.includes(bot.id)}
                        onChange={() => toggleCreateAgent(bot.id)}
                      />
                      {bot.name}
                      {bot.title ? ` · ${bot.title}` : ""}
                    </label>
                  ))
                )}
              </fieldset>
              <div className="group-chat-actions">
                <button
                  type="submit"
                  className="quiet-button"
                  disabled={busy || createAgentIDs.length < 2}
                >
                  Create group
                </button>
              </div>
            </form>
          </details>
        </aside>
        <div className="group-chat-thread">
          <div className="group-chat-thread-title">
            <h2>{selected?.title || "Select a group"}</h2>
          </div>
          {error && (
            <p className="group-chat-error" role="alert">
              {error}
            </p>
          )}
          {runs.length > 0 && (
            <div className="group-chat-runs" aria-live="polite">
              {runs.map((run) => {
                const bot = bots.find((item) => item.id === run.bot_id);
                const member = members.find((item) => item.bot_id === run.bot_id);
                const name = member
                  ? memberLabel(member, bots)
                  : bot?.name || run.bot_id;
                return (
                  <span
                    key={run.id}
                    className={`group-chat-run ${runStatusClass(run.status)}`}
                  >
                    {name}: {run.status}
                  </span>
                );
              })}
            </div>
          )}
          <div className="group-chat-messages">
            {selected && messages.length === 0 ? (
              <p className="group-chat-empty">No messages yet.</p>
            ) : (
              messages.map((message) => (
                <article
                  key={message.id}
                  className={`group-chat-msg ${message.role === "user" ? "is-user" : ""}`}
                >
                  <strong>
                    {messageAuthor(message, members, bots)}
                  </strong>
                  <p>{message.content}</p>
                  <time dateTime={message.created_at}>{message.created_at}</time>
                </article>
              ))
            )}
          </div>
          {selected && (
            <form
              className="group-chat-composer"
              onSubmit={(event) => void sendMessage(event)}
            >
              <fieldset className="group-chat-mentions">
                <legend>Mention</legend>
                {members.map((member) => (
                  <label key={member.bot_id}>
                    <input
                      type="checkbox"
                      checked={mentionIDs.includes(member.bot_id)}
                      onChange={() => toggleMention(member.bot_id)}
                    />
                    {memberLabel(member, bots)}
                  </label>
                ))}
              </fieldset>
              <label>
                Message
                <textarea
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  placeholder="Write a message"
                />
              </label>
              <div className="group-chat-actions">
                <button
                  type="submit"
                  className="quiet-button"
                  disabled={busy || !draft.trim()}
                >
                  Send
                </button>
              </div>
            </form>
          )}
        </div>
      </section>
    </div>
  );
}
