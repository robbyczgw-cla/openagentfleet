import { useEffect, useRef, useState } from "react";
import "./TasksWorkspace.css";

type ApiFetch = (path: string, init?: RequestInit) => Promise<Response>;
export type TaskSummary = {
  id: string; bot_id: string; bot_name: string; conversation_id: string;
  title: string; status: string; provider: string; created_at: string;
  updated_at: string; result_preview: string; artifact_count: number; error?: string;
};
type Artifact = { id: string; run_id: string; name: string; media_type: string; size: number; preview_kind: string };
type Detail = { task: TaskSummary; result: string; artifacts: Artifact[] };
const statuses: [string, string][] = [["", "All tasks"], ["queued", "Queued"], ["running", "Running"], ["waiting_approval", "Needs me"], ["completed", "Completed"], ["failed", "Failed"], ["blocked", "Blocked"], ["stopped", "Stopped"]];
const statusLabel = (status: string) => statuses.find(([value]) => value === status)?.[1] ?? status;
const dateLabel = (value: string) => new Date(value).toLocaleString();
async function checked(response: Response): Promise<Response> {
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `Request failed (${response.status})`);
  }
  return response;
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
    } catch (cause) { if (!abort.signal.aborted) setError(String(cause instanceof Error ? cause.message : cause)); }
    finally { if (!abort.signal.aborted) setBusy(false); }
  }
  return <article className="task-artifact">
    <strong>{artifact.name}</strong><span>{artifact.media_type} · {Math.max(1, Math.ceil(artifact.size / 1024))} KB</span>
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
  const listGeneration = useRef(0);
  const params = new URLSearchParams({ limit: "50", status, bot_id: agent, q: query }).toString();
  useEffect(() => { setSelected(""); }, [params]);
  useEffect(() => {
    const abort = new AbortController(); ++listGeneration.current; setBusy(true); setError(""); setItems([]); setCursor("");
    const timer = window.setTimeout(async () => {
      try {
        const body = await (await checked(await apiFetch(`/api/tasks?${params}`, { signal: abort.signal }))).json();
        if (!abort.signal.aborted) { setItems(body.items); setCursor(body.next_cursor ?? ""); }
      } catch (cause) { if (!abort.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause)); }
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
        if (!abort.signal.aborted) setError(cause instanceof Error ? cause.message : String(cause));
      } finally { pending = false; }
    }, 5000);
    return () => { abort.abort(); window.clearInterval(timer); };
  }, [apiFetch, params, busy, items.length]);
  useEffect(() => {
    if (!selected) { setDetail(null); return; }
    const abort = new AbortController(); setDetail(null); setDetailError("");
    async function load() {
      try {
        const body: Detail = await (await checked(await apiFetch(`/api/tasks/${encodeURIComponent(selected)}`, { signal: abort.signal }))).json();
        if (!abort.signal.aborted) { setDetail(body); setDetailError(""); }
      } catch (cause) { if (!abort.signal.aborted) setDetailError(cause instanceof Error ? cause.message : String(cause)); }
    }
    void load();
    const interval = window.setInterval(() => void load(), 5000);
    return () => { abort.abort(); window.clearInterval(interval); };
  }, [apiFetch, selected, refresh]);
  async function more() {
    const generation = listGeneration.current; setMoreBusy(true);
    try {
      const body = await (await checked(await apiFetch(`/api/tasks?${params}&cursor=${encodeURIComponent(cursor)}`))).json();
      if (generation === listGeneration.current) { setItems((current) => [...current, ...body.items]); setCursor(body.next_cursor ?? ""); }
    } catch (cause) { if (generation === listGeneration.current) setError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setMoreBusy(false); }
  }
  async function stop() {
    setStopping(true); setDetailError("");
    try { await checked(await apiFetch(`/api/runs/${encodeURIComponent(selected)}/stop`, { method: "POST" })); setRefresh((n) => n + 1); }
    catch (cause) { setDetailError(cause instanceof Error ? cause.message : String(cause)); }
    finally { setStopping(false); }
  }
  return <section className="tasks-workspace">
    <header className="task-heading"><div><div className="eyebrow">Across your Agents</div><h2 id="workspace-panel-title">Tasks & results</h2><p>Open a task to see its brief, answer and saved files.</p></div><button onClick={onClose} aria-label="Close tasks">Close</button></header>
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
          <span className="task-row-meta"><time dateTime={item.created_at}>{dateLabel(item.created_at)}</time>{item.artifact_count > 0 && <span>{item.artifact_count} files</span>}</span>
        </button>)}
        {cursor && <button disabled={moreBusy} onClick={() => void more()}>{moreBusy ? "Loading…" : "Load older tasks"}</button>}
      </div>
      <section className="task-detail" aria-label="Task details">
        {detailError && <p role="alert" className="task-error">{detailError}</p>}
        {!selected ? <p>Select a task to inspect its result.</p> : !detail ? !detailError && <p role="status">Loading task…</p> : <>
          <span className={`task-status status-${detail.task.status}`}>{statusLabel(detail.task.status)}</span>
          <h3>{detail.task.title || "Untitled task"}</h3><p>{detail.task.bot_name} · {detail.task.provider} · {dateLabel(detail.task.created_at)}</p>
          {detail.task.error && <p className="task-error">{detail.task.error}</p>}
          <div className="task-actions"><button onClick={() => onConversation(detail.task.conversation_id)}>Open conversation</button>
            {detail.task.status === "waiting_approval" && <button onClick={onReview}>Review approval</button>}
            {["queued", "running", "waiting_approval"].includes(detail.task.status) && <button disabled={stopping} onClick={() => void stop()}>{stopping ? "Stopping…" : "Stop task"}</button>}
            {detail.task.status === "completed" && <button onClick={() => onWorkflow({ name: detail.task.title.slice(0, 100), instructions: detail.task.title })}>Save as workflow</button>}
          </div>
          <h4>Result</h4><pre className="task-answer">{detail.result || "This task has no saved answer yet. Older tasks may predate result history."}</pre>
          <h4>Files ({detail.artifacts.length})</h4>
          {detail.artifacts.length ? <div className="task-artifacts">{detail.artifacts.map((artifact) => <ArtifactCard key={artifact.id} artifact={artifact} apiFetch={apiFetch} />)}</div> : <p>Ask the Agent to save files in outputs/ and link them in its answer.</p>}
        </>}
      </section>
    </div>
  </section>;
}
