import { useEffect, useId, useRef, useState } from "react";
import "./MemoryProposals.css";
import {
  MEMORY_CATEGORIES,
  MEMORY_PRIORITY_MAX,
  MEMORY_PRIORITY_MIN,
  agentName,
  categoryLabel,
  describeProposalError,
  draftFromProposal,
  priorityLabel,
  proposalDraftChanged,
  proposalPatch,
  validateProposalDraft,
} from "./memoryProposals";
import type { MemoryCategory, MemoryProposal, ProposalDraft } from "./memoryProposals";

// Nothing here is saved to an agent's memory until a person accepts it. The
// component is embedded in the Review dialog, so it renders no chrome of its
// own and stays out of the way when there is nothing waiting.

type ApiFetch = (path: string, init?: RequestInit) => Promise<Response>;
type Agent = { id: string; name: string };
type ApiFailure = Error & { status?: number };

export type MemoryProposalsProps = {
  apiFetch: ApiFetch;
  agents: Agent[];
};

async function checked(response: Response): Promise<Response> {
  if (response.ok) return response;
  const body = await response.json().catch(() => ({}) as { error?: string });
  const failure: ApiFailure = new Error(body.error || `Request failed (${response.status})`);
  failure.status = response.status;
  throw failure;
}

function failureStatus(cause: unknown): number {
  return cause instanceof Error ? ((cause as ApiFailure).status ?? 0) : 0;
}

function failureText(cause: unknown): string {
  if (cause instanceof Error) {
    return describeProposalError((cause as ApiFailure).status ?? 0, cause.message);
  }
  return describeProposalError(0, String(cause));
}

function isAborted(cause: unknown): boolean {
  return cause instanceof DOMException && cause.name === "AbortError";
}

function timestampLabel(value: string): string {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

export function MemoryProposals({ apiFetch, agents }: MemoryProposalsProps) {
  const fieldID = useId();
  const [proposals, setProposals] = useState<MemoryProposal[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [agentFilter, setAgentFilter] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [listError, setListError] = useState("");
  const [notice, setNotice] = useState("");
  const [openID, setOpenID] = useState("");
  const [drafts, setDrafts] = useState<Record<string, ProposalDraft>>({});
  const [busyID, setBusyID] = useState("");
  // Review needs the controller credential. Without it the whole section is
  // simply not offered, instead of parking an error in the Review dialog.
  const [unavailable, setUnavailable] = useState(false);
  const [cardError, setCardError] = useState<Record<string, string>>({});
  const listGeneration = useRef(0);

  useEffect(() => {
    const abort = new AbortController();
    const generation = ++listGeneration.current;
    const query = new URLSearchParams({ status: "pending" });
    if (agentFilter) query.set("bot_id", agentFilter);
    void (async () => {
      try {
        const body = await (
          await checked(await apiFetch(`/api/memory-proposals?${query.toString()}`, { signal: abort.signal }))
        ).json();
        // An answer for a filter the reviewer already changed is dropped.
        if (abort.signal.aborted || generation !== listGeneration.current) return;
        setProposals(Array.isArray(body.proposals) ? body.proposals : []);
        setListError("");
      } catch (cause) {
        if (isAborted(cause) || generation !== listGeneration.current) return;
        if ([401, 403, 503].includes(failureStatus(cause))) setUnavailable(true);
        else setListError(failureText(cause));
      } finally {
        if (generation === listGeneration.current) setLoaded(true);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, agentFilter, refresh]);

  function draftFor(proposal: MemoryProposal): ProposalDraft {
    return drafts[proposal.id] ?? draftFromProposal(proposal);
  }

  function setDraft(id: string, next: ProposalDraft) {
    setDrafts((current) => ({ ...current, [id]: next }));
  }

  function clearDraft(id: string) {
    setDrafts((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
  }

  function setError(id: string, message: string) {
    setCardError((current) => ({ ...current, [id]: message }));
  }

  function drop(id: string) {
    setProposals((current) => current.filter((item) => item.id !== id));
    clearDraft(id);
    setCardError((current) => {
      const next = { ...current };
      delete next[id];
      return next;
    });
    if (openID === id) setOpenID("");
  }

  async function saveEdits(proposal: MemoryProposal) {
    if (busyID) return;
    const draft = draftFor(proposal);
    const problem = validateProposalDraft(draft);
    if (problem) {
      setError(proposal.id, problem);
      return;
    }
    const patch = proposalPatch(draft, proposal);
    if (!patch) {
      setError(proposal.id, "");
      return;
    }
    setBusyID(proposal.id);
    setError(proposal.id, "");
    setNotice("");
    try {
      const saved: MemoryProposal = await (
        await checked(
          await apiFetch(`/api/memory-proposals/${encodeURIComponent(proposal.id)}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(patch),
          }),
        )
      ).json();
      setProposals((current) => current.map((item) => (item.id === saved.id ? saved : item)));
      clearDraft(proposal.id);
      setNotice("Saved your wording. It still needs your approval before the agent remembers it.");
    } catch (cause) {
      setError(proposal.id, failureText(cause));
    } finally {
      setBusyID("");
    }
  }

  async function accept(proposal: MemoryProposal) {
    if (busyID) return;
    const draft = draftFor(proposal);
    const problem = validateProposalDraft(draft);
    if (problem) {
      setError(proposal.id, problem);
      return;
    }
    // Edits ride along with the approval, so nothing typed here is lost if the
    // reviewer forgets to save first.
    const patch = proposalPatch(draft, proposal);
    setBusyID(proposal.id);
    setError(proposal.id, "");
    setNotice("");
    try {
      const body = await (
        await checked(
          await apiFetch(`/api/memory-proposals/${encodeURIComponent(proposal.id)}/accept`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(patch ?? {}),
          }),
        )
      ).json();
      const saved: MemoryProposal = body.proposal ?? proposal;
      drop(proposal.id);
      setNotice(`${agentName(agents, saved.bot_id)} will remember this from now on.`);
    } catch (cause) {
      setError(proposal.id, failureText(cause));
    } finally {
      setBusyID("");
    }
  }

  async function reject(proposal: MemoryProposal) {
    if (busyID) return;
    setBusyID(proposal.id);
    setError(proposal.id, "");
    setNotice("");
    try {
      await checked(
        await apiFetch(`/api/memory-proposals/${encodeURIComponent(proposal.id)}/reject`, { method: "POST" }),
      );
      drop(proposal.id);
      setNotice(`Dismissed. ${agentName(agents, proposal.bot_id)} will not remember it.`);
    } catch (cause) {
      setError(proposal.id, failureText(cause));
    } finally {
      setBusyID("");
    }
  }

  const hasFilter = agentFilter !== "";
  if (unavailable || !loaded) return null;
  if (proposals.length === 0 && !hasFilter && !listError) return null;

  return (
    <section className="memory-proposals" aria-labelledby={`${fieldID}-title`}>
      <div className="mp-head">
        <h3 id={`${fieldID}-title`}>
          Suggested memories <span className="mp-count">{proposals.length}</span>
        </h3>
        <label htmlFor={`${fieldID}-agent`}>
          <span>Agent</span>
          <select
            id={`${fieldID}-agent`}
            value={agentFilter}
            onChange={(event) => setAgentFilter(event.target.value)}
          >
            <option value="">All Agents</option>
            {agents.map((agent) => (
              <option key={agent.id} value={agent.id}>
                {agent.name}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="quiet-button" onClick={() => setRefresh((count) => count + 1)}>
          Refresh
        </button>
      </div>
      <p className="mp-intro">
        Agents ask before they remember anything. Read each one, change the wording if you want, then accept or
        dismiss it. Nothing here reaches an agent until you accept it.
      </p>
      {notice && (
        <p className="mp-notice" role="status">
          {notice}
        </p>
      )}
      {listError && (
        <p className="mp-error" role="alert">
          {listError}
        </p>
      )}
      {proposals.length === 0 ? (
        <p className="mp-empty">Nothing waiting for this Agent.</p>
      ) : (
        <ul className="mp-list">
          {proposals.map((proposal) => {
            const draft = draftFor(proposal);
            const open = openID === proposal.id;
            const busy = busyID === proposal.id;
            const changed = proposalDraftChanged(draft, proposal);
            return (
              <li key={proposal.id} className="mp-card">
                <div className="mp-card-head">
                  <strong>{agentName(agents, proposal.bot_id)}</strong>
                  <span className="mp-tag">{categoryLabel(proposal.category)}</span>
                  <span className="mp-tag">{priorityLabel(proposal.priority)}</span>
                  <time dateTime={proposal.created_at}>{timestampLabel(proposal.created_at)}</time>
                </div>
                {open ? (
                  <div className="mp-edit">
                    <div className="mp-field">
                      <label htmlFor={`${fieldID}-content-${proposal.id}`}>What the agent would remember</label>
                      <textarea
                        id={`${fieldID}-content-${proposal.id}`}
                        rows={4}
                        value={draft.content}
                        disabled={busy}
                        onChange={(event) => setDraft(proposal.id, { ...draft, content: event.target.value })}
                      />
                    </div>
                    <div className="mp-field-row">
                      <div className="mp-field">
                        <label htmlFor={`${fieldID}-category-${proposal.id}`}>Kind</label>
                        <select
                          id={`${fieldID}-category-${proposal.id}`}
                          value={draft.category}
                          disabled={busy}
                          onChange={(event) =>
                            setDraft(proposal.id, { ...draft, category: event.target.value as MemoryCategory })
                          }
                        >
                          {MEMORY_CATEGORIES.map(([value, label]) => (
                            <option key={value} value={value}>
                              {label}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="mp-field">
                        <label htmlFor={`${fieldID}-priority-${proposal.id}`}>Importance</label>
                        <select
                          id={`${fieldID}-priority-${proposal.id}`}
                          value={String(draft.priority)}
                          disabled={busy}
                          onChange={(event) =>
                            setDraft(proposal.id, { ...draft, priority: Number(event.target.value) })
                          }
                        >
                          {[MEMORY_PRIORITY_MIN, 2, 3, 4, MEMORY_PRIORITY_MAX].map((value) => (
                            <option key={value} value={String(value)}>
                              {priorityLabel(value)}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="mp-field">
                        <label htmlFor={`${fieldID}-expiry-${proposal.id}`}>Forget after</label>
                        <input
                          id={`${fieldID}-expiry-${proposal.id}`}
                          type="date"
                          value={draft.expiry}
                          disabled={busy}
                          onChange={(event) => setDraft(proposal.id, { ...draft, expiry: event.target.value })}
                        />
                      </div>
                    </div>
                  </div>
                ) : (
                  <p className="mp-content">{proposal.content}</p>
                )}
                <p className="mp-source">
                  Came from task <code>{proposal.source_run_id}</code>, message{" "}
                  <code>{proposal.source_message_id}</code>
                  {proposal.expires_at ? ` · forgotten after ${timestampLabel(proposal.expires_at)}` : ""}
                </p>
                {cardError[proposal.id] && (
                  <p className="mp-error" role="alert">
                    {cardError[proposal.id]}
                  </p>
                )}
                <div className="mp-actions">
                  <button
                    type="button"
                    className="quiet-button"
                    disabled={busy}
                    onClick={() => setOpenID(open ? "" : proposal.id)}
                  >
                    {open ? "Done editing" : "Edit wording"}
                  </button>
                  {open && (
                    <button
                      type="button"
                      className="quiet-button"
                      disabled={busy || !changed}
                      onClick={() => void saveEdits(proposal)}
                    >
                      Save edits
                    </button>
                  )}
                  <button
                    type="button"
                    className="quiet-button mp-accept"
                    disabled={busy}
                    onClick={() => void accept(proposal)}
                  >
                    {busy ? "Working…" : changed ? "Accept with my edits" : "Accept"}
                  </button>
                  <button type="button" className="quiet-button" disabled={busy} onClick={() => void reject(proposal)}>
                    Dismiss
                  </button>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
