import { useEffect, useRef, useState } from "react";
import "./TasksWorkspace.css";
import {
  attemptLabel,
  attemptsLeft,
  describeFollowupError,
  MAX_TASK_ATTEMPTS,
  emptyTaskInput,
  fileSizeLabel,
  followupRequestBody,
  nextIdempotency,
  randomIdempotencyKey,
  validateFollowupIntent,
} from "./taskFollowups";
import type { FollowupIntent, FollowupKind, IdempotencyState, TaskInput } from "./taskFollowups";
import { briefRevisionLabel } from "./projects";
import type { ProjectTaskSnapshot } from "./projects";

type ApiFetch = (path: string, init?: RequestInit) => Promise<Response>;
export type TaskSummary = {
  id: string; bot_id: string; bot_name: string; conversation_id: string;
  title: string; status: string; provider: string; created_at: string;
  updated_at: string; result_preview: string; artifact_count: number; error?: string;
  parent_task_id?: string; root_task_id: string; attempt: number; followup_kind?: string;
  can_retry: boolean; can_revise: boolean;
};
type Artifact = { id: string; run_id: string; name: string; media_type: string; size: number; preview_kind: string };
type Detail = { task: TaskSummary; input: TaskInput; result: string; artifacts: Artifact[] };
type FollowupDraft = { kind: FollowupKind; brief: string; attachmentIDs: string[] };
type ApiFailure = Error & { status?: number };
const statuses: [string, string][] = [["", "All tasks"], ["queued", "Queued"], ["running", "Running"], ["waiting_approval", "Needs me"], ["completed", "Completed"], ["failed", "Failed"], ["blocked", "Blocked"], ["stopped", "Stopped"]];
const statusLabel = (status: string) => statuses.find(([value]) => value === status)?.[1] ?? status;
const dateLabel = (value: string) => new Date(value).toLocaleString();
async function checked(response: Response): Promise<Response> {
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const failure: ApiFailure = new Error(body.error || `Request failed (${response.status})`);
    failure.status = response.status;
    throw failure;
  }
  return response;
}
function errorText(cause: unknown): string {
  return cause instanceof Error ? cause.message : String(cause);
}
function followupErrorText(cause: unknown): string {
  const status = cause instanceof Error ? ((cause as ApiFailure).status ?? 0) : 0;
  return describeFollowupError(status, errorText(cause));
}

function ArtifactCard({ artifact, apiFetch }: { artifact: Artifact; apiFetch: ApiFetch }) {
  const [preview, setPreview] = useState<{ text?: string; image?: string } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const imageURL = useRef("");
  useEffect(() => () => { controller.current?.abort(); if (imageURL.current) URL.revokeObjectURL(imageURL.current); }, []);
  const base = `/api/tasks/${encodeURIComponent(artifact.run_id)}/artifacts/${encodeURIComponent(artifact.id)}`;
  async function read(download: boolean) {
    controller.current?.abort();
    const abort = new AbortController(); controller.current = abort;
    setBusy(true); setError("");
    try {
      const response = await checked(await apiFetch(`${base}/${download ? "download" : "content"}`, { signal: abort.signal }));
      const blob = await response.blob();
      if (abort.signal.aborted) return;
      if (download) {
        const url = URL.createObjectURL(blob);
        const link = document.createElement("a"); link.href = url; link.download = artifact.name; link.click();
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
      } else if (artifact.preview_kind === "image" && /^image\/(png|jpeg|gif|webp|avif|bmp)$/.test(blob.type)) {
        if (imageURL.current) URL.revokeObjectURL(imageURL.current);
        imageURL.current = URL.createObjectURL(blob); setPreview({ image: imageURL.current });
      } else {
        const text = await blob.slice(0, 100_000).text();
        if (!abort.signal.aborted) setPreview({ text: text + (blob.size > 100_000 ? "\n[Preview truncated. Download the full file.]" : "") });
      }
    } catch (cause) { if (!abort.signal.aborted) setError(errorText(cause)); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  return <article className="task-artifact">
    <strong>{artifact.name}</strong><span>{artifact.media_type} · {fileSizeLabel(artifact.size)}</span>
    <div className="task-actions">
      {artifact.preview_kind !== "download" && <button disabled={busy} onClick={() => preview ? setPreview(null) : void read(false)}>{preview ? "Hide preview" : "Preview"}</button>}
      <button disabled={busy} onClick={() => void read(true)}>{busy ? "Loading…" : "Download"}</button>
    </div>
    {error && <p role="alert">{error}</p>}
    {preview?.image && <img src={preview.image} alt={artifact.name} />}
    {preview?.text !== undefined && <pre>{preview.text}</pre>}
  </article>;
}

export function TasksWorkspace({ apiFetch, agents, onClose, onConversation, onReview, onWorkflow }: {
  apiFetch: ApiFetch; agents: { id: string; name: string }[]; onClose: () => void;
  onConversation: (id: string) => void; onReview: () => void;
  onWorkflow: (draft: { name: string; instructions: string }) => void;
}) {
  const [status, setStatus] = useState(""); const [agent, setAgent] = useState(""); const [query, setQuery] = useState("");
  const [items, setItems] = useState<TaskSummary[]>([]); const [selected, setSelected] = useState("");
  const [detail, setDetail] = useState<Detail | null>(null); const [error, setError] = useState("");
  const [detailError, setDetailError] = useState(""); const [busy, setBusy] = useState(true);
  const [stopping, setStopping] = useState(false); const [refresh, setRefresh] = useState(0);
  const [cursor, setCursor] = useState(""); const [moreBusy, setMoreBusy] = useState(false);
  const [snapshot, setSnapshot] = useState<ProjectTaskSnapshot | null>(null);
  const [followup, setFollowup] = useState<FollowupDraft | null>(null);
  const [followupError, setFollowupError] = useState(""); const [followupBusy, setFollowupBusy] = useState(false);
  // A notice belongs to one task, so switching tasks cannot carry it along.
  const [notice, setNotice] = useState<{ taskID: string; text: string } | null>(null);
  const listGeneration = useRef(0);
  const detailGeneration = useRef(0);
  // One key covers one intent. A resend after a dropped connection reuses it so
  // the agent cannot be asked twice; editing the request mints a new one.
  const idempotency = useRef<IdempotencyState | null>(null);
  const params = new URLSearchParams({ limit: "50", status, bot_id: agent, q: query }).toString();
  useEffect(() => { setSelected(""); }, [params]);
  useEffect(() => {
    const abort = new AbortController(); ++listGeneration.current; setBusy(true); setError(""); setItems([]); setCursor("");
    const timer = window.setTimeout(async () => {
      try {
        const body = await (await checked(await apiFetch(`/api/tasks?${params}`, { signal: abort.signal }))).json();
        if (!abort.signal.aborted) { setItems(body.items); setCursor(body.next_cursor ?? ""); }
      } catch (cause) { if (!abort.signal.aborted) setError(errorText(cause)); }
      finally { if (!abort.signal.aborted) setBusy(false); }
    }, 200);
    return () => { abort.abort(); window.clearTimeout(timer); };
  }, [apiFetch, params, refresh]);
  useEffect(() => {
    if (busy || items.length > 50) return;
    const abort = new AbortController();
    let pending = false;
    const timer = window.setInterval(async () => {
      if (pending) return;
      pending = true;
      try {
        const body = await (await checked(await apiFetch(`/api/tasks?${params}`, { signal: abort.signal }))).json();
        if (!abort.signal.aborted) { setItems(body.items); setCursor(body.next_cursor ?? ""); setError(""); }
      } catch (cause) {
        if (!abort.signal.aborted) setError(errorText(cause));
      } finally { pending = false; }
    }, 5000);
    return () => { abort.abort(); window.clearInterval(timer); };
  }, [apiFetch, params, busy, items.length]);
  // Everything tied to one task is dropped the moment another one is opened, so
  // a slow answer can never land on the wrong task.
  useEffect(() => {
    setSnapshot(null); setFollowup(null); setFollowupError("");
    idempotency.current = null;
  }, [selected]);
  useEffect(() => {
    if (!selected) { setDetail(null); return; }
    const abort = new AbortController(); const generation = ++detailGeneration.current;
    setDetail(null); setDetailError("");
    async function load() {
      try {
        const body: Detail = await (await checked(await apiFetch(`/api/tasks/${encodeURIComponent(selected)}`, { signal: abort.signal }))).json();
        if (abort.signal.aborted || generation !== detailGeneration.current) return;
        setDetail({ ...body, input: body.input ?? emptyTaskInput() }); setDetailError("");
      } catch (cause) { if (!abort.signal.aborted && generation === detailGeneration.current) setDetailError(errorText(cause)); }
    }
    void load();
    const interval = window.setInterval(() => void load(), 5000);
    return () => { abort.abort(); window.clearInterval(interval); };
  }, [apiFetch, selected, refresh]);
  // A task that ran without a project simply has no snapshot, which the server
  // reports as a missing record rather than an error worth showing.
  useEffect(() => {
    if (!selected) return;
    const abort = new AbortController(); const generation = detailGeneration.current;
    void (async () => {
      try {
        const response = await apiFetch(`/api/projects/task-snapshots/${encodeURIComponent(selected)}`, { signal: abort.signal });
        if (response.status === 404) return;
        const body = await (await checked(response)).json();
        if (!abort.signal.aborted && generation === detailGeneration.current) setSnapshot(body.project_snapshot ?? null);
      } catch { /* The task detail already reports anything that matters. */ }
    })();
    return () => abort.abort();
  }, [apiFetch, selected]);
  async function more() {
    const generation = listGeneration.current; setMoreBusy(true);
    try {
      const body = await (await checked(await apiFetch(`/api/tasks?${params}&cursor=${encodeURIComponent(cursor)}`))).json();
      if (generation === listGeneration.current) { setItems((current) => [...current, ...body.items]); setCursor(body.next_cursor ?? ""); }
    } catch (cause) { if (generation === listGeneration.current) setError(errorText(cause)); }
    finally { setMoreBusy(false); }
  }
  async function stop() {
    setStopping(true); setDetailError("");
    try { await checked(await apiFetch(`/api/runs/${encodeURIComponent(selected)}/stop`, { method: "POST" })); setRefresh((n) => n + 1); }
    catch (cause) { setDetailError(errorText(cause)); }
    finally { setStopping(false); }
  }
  function openFollowup(kind: FollowupKind) {
    if (!detail) return;
    idempotency.current = null;
    setFollowupError(""); setNotice(null);
    setFollowup({ kind, brief: detail.input.brief, attachmentIDs: detail.input.attachments.map((file) => file.id) });
  }
  async function sendFollowup() {
    if (!detail || !followup || followupBusy) return;
    const intent: FollowupIntent = {
      kind: followup.kind, taskID: detail.task.id, agentID: detail.task.bot_id,
      brief: followup.brief, attachmentIDs: followup.attachmentIDs,
    };
    const problem = validateFollowupIntent(intent, detail.input);
    if (problem) { setFollowupError(problem); return; }
    const key = nextIdempotency(idempotency.current, intent, randomIdempotencyKey);
    idempotency.current = key;
    setFollowupBusy(true); setFollowupError("");
    try {
      const body = await (await checked(await apiFetch(`/api/tasks/${encodeURIComponent(detail.task.id)}/${followup.kind}`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "Idempotency-Key": key.key },
        body: JSON.stringify(followupRequestBody(intent)),
      }))).json();
      const started = String(body.run?.id ?? "");
      idempotency.current = null;
      setFollowup(null);
      setRefresh((n) => n + 1);
      if (started && started !== detail.task.id) setSelected(started);
      setNotice({ taskID: started || detail.task.id, text: `${detail.task.bot_name} is working on this again. The earlier answer and files are still on the previous attempt.` });
    } catch (cause) { setFollowupError(followupErrorText(cause)); }
    finally { setFollowupBusy(false); }
  }
  const input = detail?.input ?? emptyTaskInput();
  const canSendAgain = detail ? detail.task.can_retry || detail.task.can_revise : false;
  return <section className="tasks-workspace">
    <header className="task-heading"><div><div className="eyebrow">Across your Agents</div><h2 id="workspace-panel-title">Tasks & results</h2><p>Open a task to see its request, answer and saved files.</p></div><button onClick={onClose} aria-label="Close tasks">Close</button></header>
    <div className="task-filters">
      <label>Search<input type="search" value={query} maxLength={500} onChange={(event) => setQuery(event.target.value)} placeholder="Find a task" /></label>
      <label>Agent<select value={agent} onChange={(event) => setAgent(event.target.value)}><option value="">All Agents</option>{agents.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Status<select value={status} onChange={(event) => setStatus(event.target.value)}>{statuses.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      <button onClick={() => setRefresh((n) => n + 1)} disabled={busy}>Refresh</button>
    </div>
    {error && <p role="alert" className="task-error">{error}</p>}
    <div className="task-columns">
      <div className="task-list" aria-label="Tasks" aria-busy={busy}>
        {busy ? <p role="status">Loading tasks…</p> : items.length === 0 ? <p>No tasks match these filters. Send an Agent a message to start a task.</p> : items.map((item) => <button key={item.id} className={`task-row ${selected === item.id ? "selected" : ""}`} aria-pressed={selected === item.id} onClick={() => setSelected(item.id)}>
          <span className="task-row-meta"><span>{item.bot_name}</span><span className={`task-status status-${item.status}`}>{statusLabel(item.status)}</span></span>
          <strong>{item.title || "Untitled task"}</strong><span className="task-result-preview">{item.result_preview || (item.status === "queued" ? "Waiting for this Agent's current task to finish." : "No saved result yet.")}</span>
          <span className="task-row-meta"><time dateTime={item.created_at}>{dateLabel(item.created_at)}</time>{(item.attempt ?? 1) > 1 && <span className="task-attempt">Attempt {item.attempt}</span>}{item.artifact_count > 0 && <span>{item.artifact_count} files</span>}</span>
        </button>)}
        {cursor && <button disabled={moreBusy} onClick={() => void more()}>{moreBusy ? "Loading…" : "Load older tasks"}</button>}
      </div>
      <section className="task-detail" aria-label="Task details">
        {detailError && <p role="alert" className="task-error">{detailError}</p>}
        {!selected ? <p>Select a task to inspect its result.</p> : !detail ? !detailError && <p role="status">Loading task…</p> : <>
          <span className={`task-status status-${detail.task.status}`}>{statusLabel(detail.task.status)}</span>
          <h3>{detail.task.title || "Untitled task"}</h3><p>{detail.task.bot_name} · {detail.task.provider} · {dateLabel(detail.task.created_at)}</p>
          <p className="task-attempt-line">{attemptLabel(detail.task.attempt ?? 1, detail.task.followup_kind ?? "")}</p>
          {snapshot && <p className="task-project">Worked from the {snapshot.project_name} project · {briefRevisionLabel(snapshot.brief_revision)}. That wording was copied when this task joined the queue, and later project edits did not change it.</p>}
          {detail.task.parent_task_id && <div className="task-actions"><button onClick={() => setSelected(detail.task.parent_task_id ?? "")}>Open the earlier attempt</button></div>}
          {notice && notice.taskID === selected && <p className="task-notice" role="status">{notice.text}</p>}
          {detail.task.error && <p className="task-error">{detail.task.error}</p>}
          <div className="task-actions"><button onClick={() => onConversation(detail.task.conversation_id)}>Open conversation</button>
            {detail.task.status === "waiting_approval" && <button onClick={onReview}>Review approval</button>}
            {["queued", "running", "waiting_approval"].includes(detail.task.status) && <button disabled={stopping} onClick={() => void stop()}>{stopping ? "Stopping…" : "Stop task"}</button>}
            {detail.task.can_retry && <button onClick={() => openFollowup("retry")}>Run this again</button>}
            {detail.task.can_revise && <button onClick={() => openFollowup("revise")}>Change the request and run</button>}
            {detail.task.status === "completed" && <button onClick={() => onWorkflow({ name: detail.task.title.slice(0, 100), instructions: detail.task.title })}>Save as workflow</button>}
          </div>
          {!canSendAgain && ["failed", "stopped", "completed"].includes(detail.task.status) && attemptsLeft(detail.task.attempt ?? 1) === 0 &&
            <p className="task-help">This request has been run as many times as we allow. Start a new task to keep going.</p>}
          {followup && <form className="task-followup" onSubmit={(event) => { event.preventDefault(); void sendFollowup(); }}>
            <h4>{followup.kind === "retry" ? "Run this again" : "Change the request and run"}</h4>
            <p className="task-help">{followup.kind === "retry"
              ? "The same request goes back to the same Agent, word for word."
              : "Reword the request. It goes back to the same Agent."}</p>
            <p className="task-help">Runs with {detail.task.bot_name}. Handing this to a different Agent is not available yet.</p>
            <p className="task-help">The earlier answer and files stay where they are. This starts attempt {(detail.task.attempt ?? 1) + 1} of {MAX_TASK_ATTEMPTS}.</p>
            <label className="task-followup-brief">
              <span>Request</span>
              <textarea rows={6} value={followup.brief} readOnly={followup.kind === "retry"} disabled={followupBusy}
                onChange={(event) => setFollowup({ ...followup, brief: event.target.value })} />
            </label>
            {input.attachments.length > 0 && <fieldset className="task-followup-files" disabled={followupBusy}>
              <legend>Files from the original request</legend>
              {input.attachments.map((file) => <label key={file.id}>
                <input type="checkbox" checked={followup.attachmentIDs.includes(file.id)}
                  onChange={(event) => setFollowup({
                    ...followup,
                    attachmentIDs: event.target.checked
                      ? input.attachments.filter((candidate) => candidate.id === file.id || followup.attachmentIDs.includes(candidate.id)).map((candidate) => candidate.id)
                      : followup.attachmentIDs.filter((id) => id !== file.id),
                  })} />
                <span>{file.name} · {fileSizeLabel(file.size)}</span>
              </label>)}
            </fieldset>}
            {followupError && <p role="alert" className="task-error">{followupError}</p>}
            <div className="task-actions">
              <button type="submit" disabled={followupBusy}>{followupBusy ? "Starting…" : followup.kind === "retry" ? "Run it again" : "Send the new request"}</button>
              <button type="button" disabled={followupBusy} onClick={() => { setFollowup(null); setFollowupError(""); }}>Cancel</button>
            </div>
          </form>}
          <h4>Original request</h4>
          <pre className="task-answer">{input.brief || "The original wording for this task was not kept."}</pre>
          {input.attachments.length > 0 && <ul className="task-input-files">
            {input.attachments.map((file) => <li key={file.id}>{file.name} · {fileSizeLabel(file.size)}</li>)}
          </ul>}
          <h4>Result</h4><pre className="task-answer">{detail.result || "This task has no saved answer yet. Older tasks may predate result history."}</pre>
          <h4>Files ({detail.artifacts.length})</h4>
          {detail.artifacts.length ? <div className="task-artifacts">{detail.artifacts.map((artifact) => <ArtifactCard key={artifact.id} artifact={artifact} apiFetch={apiFetch} />)}</div> : <p>Ask the Agent to save files in outputs/ and link them in its answer.</p>}
        </>}
      </section>
    </div>
  </section>;
}
