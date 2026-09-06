import { useCallback, useEffect, useId, useMemo, useRef, useState, useSyncExternalStore } from "react";
import "./ProjectsWorkspace.css";
import {
  assignableProjects,
  briefRevisionLabel,
  draftFromProject,
  describeProjectError,
  emptyProjectDraft,
  isProjectVersionConflict,
  projectDraftChanged,
  projectStatusLabel,
  removedMembers,
  toggleMember,
  validateProjectDraft,
} from "./projects";
import type { Project, ProjectAssociation, ProjectBriefRevision, ProjectDraft } from "./projects";
import { notifyProjectsChanged, projectsRevision, subscribeProjects } from "./projectEvents";

// The parent renders the dialog: backdrop, role, focus trap and Escape all live
// in App.tsx. This component is only the contents, so it adds no outer chrome
// and never moves focus on its own.

type ApiFetch = (path: string, init?: RequestInit) => Promise<Response>;
type Agent = { id: string; name: string };
type ApiFailure = Error & { status?: number };

export type ProjectsWorkspaceProps = {
  apiFetch: ApiFetch;
  agents: Agent[];
  onClose: () => void;
};

export type ProjectSelectorProps = {
  apiFetch: ApiFetch;
  subjectType: "conversation" | "routine";
  subjectID: string;
  agentID: string;
};

async function checked(response: Response): Promise<Response> {
  if (response.ok) return response;
  const body = await response.json().catch(() => ({}) as { error?: string });
  const failure: ApiFailure = new Error(body.error || `Request failed (${response.status})`);
  failure.status = response.status;
  throw failure;
}

function failureText(cause: unknown): string {
  if (cause instanceof Error) {
    return describeProjectError((cause as ApiFailure).status ?? 0, cause.message);
  }
  return describeProjectError(0, String(cause));
}

function isConflict(cause: unknown): boolean {
  return (
    cause instanceof Error && isProjectVersionConflict((cause as ApiFailure).status ?? 0, cause.message)
  );
}

function isAborted(cause: unknown): boolean {
  return cause instanceof DOMException && cause.name === "AbortError";
}

function timestampLabel(value: string): string {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

type ProjectDetail = { project: Project; brief_revisions: ProjectBriefRevision[] };

export function ProjectsWorkspace({ apiFetch, agents, onClose }: ProjectsWorkspaceProps) {
  const fieldID = useId();
  const [includeArchived, setIncludeArchived] = useState(false);
  const [projects, setProjects] = useState<Project[]>([]);
  const [listBusy, setListBusy] = useState(true);
  const [listError, setListError] = useState("");
  // The list follows the same signal the selectors do, so one write reloads
  // every surface exactly once.
  const listRefresh = useSyncExternalStore(subscribeProjects, projectsRevision, projectsRevision);

  const [selectedID, setSelectedID] = useState("");
  const [detail, setDetail] = useState<ProjectDetail | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [detailError, setDetailError] = useState("");
  const [detailRefresh, setDetailRefresh] = useState(0);

  const [draft, setDraft] = useState<ProjectDraft>(emptyProjectDraft);
  const [saveError, setSaveError] = useState("");
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [confirmArchive, setConfirmArchive] = useState(false);

  const [creating, setCreating] = useState(false);
  const [createDraft, setCreateDraft] = useState<ProjectDraft>(emptyProjectDraft);
  const [createError, setCreateError] = useState("");
  const [createBusy, setCreateBusy] = useState(false);

  const [notice, setNotice] = useState("");
  // A conflict keeps the reviewer's wording while the newest saved copy loads
  // underneath it, so nothing typed is thrown away by the reload.
  const keepDraft = useRef(false);
  const detailGeneration = useRef(0);

  useEffect(() => {
    const abort = new AbortController();
    setListBusy(true);
    setListError("");
    void (async () => {
      try {
        const body = await (
          await checked(
            await apiFetch(`/api/projects?include_archived=${includeArchived ? "true" : "false"}`, {
              signal: abort.signal,
            }),
          )
        ).json();
        if (abort.signal.aborted) return;
        setProjects(Array.isArray(body.projects) ? body.projects : []);
      } catch (cause) {
        if (!isAborted(cause) && !abort.signal.aborted) setListError(failureText(cause));
      } finally {
        if (!abort.signal.aborted) setListBusy(false);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, includeArchived, listRefresh]);

  useEffect(() => {
    if (!selectedID) {
      setDetail(null);
      setDetailError("");
      return;
    }
    const abort = new AbortController();
    const generation = ++detailGeneration.current;
    setDetailBusy(true);
    setDetailError("");
    void (async () => {
      try {
        const body: ProjectDetail = await (
          await checked(
            await apiFetch(`/api/projects/${encodeURIComponent(selectedID)}`, { signal: abort.signal }),
          )
        ).json();
        // A slower answer for a project the reviewer already left must not
        // replace what is on screen now.
        if (abort.signal.aborted || generation !== detailGeneration.current) return;
        setDetail({ project: body.project, brief_revisions: body.brief_revisions ?? [] });
        if (keepDraft.current) keepDraft.current = false;
        else setDraft(draftFromProject(body.project));
      } catch (cause) {
        if (!isAborted(cause) && generation === detailGeneration.current) setDetailError(failureText(cause));
      } finally {
        if (generation === detailGeneration.current) setDetailBusy(false);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, selectedID, detailRefresh]);

  const selectProject = useCallback((id: string) => {
    keepDraft.current = false;
    setSelectedID(id);
    setCreating(false);
    setConflict(false);
    setSaveError("");
    setHistoryOpen(false);
    setConfirmArchive(false);
    setNotice("");
  }, []);

  const memberOptions = useMemo(() => {
    const known = new Map<string, string>(agents.map((agent) => [agent.id, agent.name]));
    for (const member of detail?.project.members ?? []) {
      if (!known.has(member.agent_id)) known.set(member.agent_id, member.name || member.agent_id);
    }
    return [...known].map(([id, name]) => ({ id, name }));
  }, [agents, detail]);

  const project = detail?.project ?? null;
  const revisions = detail?.brief_revisions ?? [];
  const archived = project?.status === "archived";
  const dirty = project ? projectDraftChanged(draft, project) : false;
  const losing = project ? removedMembers(draft, project) : [];

  async function save() {
    if (!project || saving) return;
    const problem = validateProjectDraft(draft);
    if (problem) {
      setSaveError(problem);
      return;
    }
    setSaving(true);
    setSaveError("");
    setNotice("");
    try {
      const body = await (
        await checked(
          await apiFetch(`/api/projects/${encodeURIComponent(project.id)}`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              name: draft.name.trim(),
              brief: draft.brief,
              agent_ids: draft.agentIDs,
              expected_version: project.version,
            }),
          }),
        )
      ).json();
      const saved: Project = body.project;
      setDetail((current) => (current ? { ...current, project: saved } : current));
      setDraft(draftFromProject(saved));
      setConflict(false);
      setNotice(
        saved.brief_revision > project.brief_revision
          ? `Saved. New work on this project uses ${briefRevisionLabel(saved.brief_revision).toLowerCase()}.`
          : "Saved.",
      );
      notifyProjectsChanged();
      setDetailRefresh((count) => count + 1);
    } catch (cause) {
      if (isConflict(cause)) setConflict(true);
      setSaveError(failureText(cause));
    } finally {
      setSaving(false);
    }
  }

  function loadLatest(keepEdits: boolean) {
    keepDraft.current = keepEdits;
    setConflict(false);
    setSaveError("");
    setNotice(keepEdits ? "Loaded the latest copy. Your wording is still here." : "Loaded the latest copy.");
    setDetailRefresh((count) => count + 1);
  }

  async function archive() {
    if (!project || saving) return;
    setSaving(true);
    setSaveError("");
    try {
      const body = await (
        await checked(
          await apiFetch(`/api/projects/${encodeURIComponent(project.id)}/archive`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ expected_version: project.version }),
          }),
        )
      ).json();
      const saved: Project = body.project;
      setDetail((current) => (current ? { ...current, project: saved } : current));
      setDraft(draftFromProject(saved));
      setConfirmArchive(false);
      setNotice(`Archived ${saved.name}. Its agents will not start new work on it.`);
      notifyProjectsChanged();
    } catch (cause) {
      setConfirmArchive(false);
      if (isConflict(cause)) setConflict(true);
      setSaveError(failureText(cause));
    } finally {
      setSaving(false);
    }
  }

  async function create() {
    if (createBusy) return;
    const problem = validateProjectDraft(createDraft);
    if (problem) {
      setCreateError(problem);
      return;
    }
    setCreateBusy(true);
    setCreateError("");
    try {
      const body = await (
        await checked(
          await apiFetch("/api/projects", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              name: createDraft.name.trim(),
              brief: createDraft.brief,
              agent_ids: createDraft.agentIDs,
            }),
          }),
        )
      ).json();
      const saved: Project = body.project;
      setCreateDraft(emptyProjectDraft());
      notifyProjectsChanged();
      selectProject(saved.id);
      setNotice(`Created ${saved.name}.`);
    } catch (cause) {
      setCreateError(failureText(cause));
    } finally {
      setCreateBusy(false);
    }
  }

  function renderMemberPicker(
    value: ProjectDraft,
    onChange: (next: ProjectDraft) => void,
    prefix: string,
    disabled: boolean,
    options: { id: string; name: string }[],
  ) {
    return (
      <fieldset className="pw-members" disabled={disabled}>
        <legend>Agents on this project</legend>
        {options.length === 0 ? (
          <p className="pw-help">Add an agent to your fleet first, then come back.</p>
        ) : (
          <div className="pw-member-grid">
            {options.map((option) => (
              <label key={option.id} htmlFor={`${prefix}-member-${option.id}`}>
                <input
                  id={`${prefix}-member-${option.id}`}
                  type="checkbox"
                  checked={value.agentIDs.includes(option.id)}
                  onChange={(event) =>
                    onChange({ ...value, agentIDs: toggleMember(value.agentIDs, option.id, event.target.checked) })
                  }
                />
                <span>{option.name}</span>
              </label>
            ))}
          </div>
        )}
      </fieldset>
    );
  }

  return (
    <section className="projects-workspace">
      <header className="pw-heading">
        <div>
          <div className="eyebrow">Shared context</div>
          <h2 id="workspace-panel-title">Projects</h2>
          <p>
            A project holds a brief that its agents work from. Each task copies the brief at the moment it
            joins the queue, so later edits only reach work queued after them.
          </p>
        </div>
        <button type="button" onClick={onClose} aria-label="Close projects">
          Close
        </button>
      </header>

      {notice && (
        <p className="pw-notice" role="status">
          {notice}
        </p>
      )}
      {listError && (
        <p className="pw-error" role="alert">
          {listError}
        </p>
      )}

      <div className="pw-columns">
        <div className="pw-list-column">
          <div className="pw-list-head">
            <button
              type="button"
              className="pw-primary"
              onClick={() => {
                setCreating(true);
                setSelectedID("");
                setCreateError("");
                setNotice("");
              }}
            >
              New project
            </button>
            <label htmlFor={`${fieldID}-archived`} className="pw-inline-check">
              <input
                id={`${fieldID}-archived`}
                type="checkbox"
                checked={includeArchived}
                onChange={(event) => setIncludeArchived(event.target.checked)}
              />
              <span>Show archived</span>
            </label>
          </div>
          <div className="pw-list" aria-label="Projects" aria-busy={listBusy}>
            {listBusy && projects.length === 0 ? (
              <p role="status">Loading projects…</p>
            ) : projects.length === 0 ? (
              <p>No projects yet. Create one to give a group of agents a shared brief.</p>
            ) : (
              projects.map((item) => (
                <button
                  type="button"
                  key={item.id}
                  className={`pw-item${item.id === selectedID ? " is-on" : ""}`}
                  aria-pressed={item.id === selectedID}
                  onClick={() => selectProject(item.id)}
                >
                  <span className="pw-item-name">{item.name}</span>
                  <span className={`pw-badge is-${item.status}`}>{projectStatusLabel(item)}</span>
                  <span className="pw-item-meta">
                    {item.members.length === 1 ? "1 agent" : `${item.members.length} agents`}
                    {" · "}
                    {briefRevisionLabel(item.brief_revision)}
                  </span>
                </button>
              ))
            )}
          </div>
        </div>

        <div className="pw-detail-column">
          {creating ? (
            <form
              className="pw-form"
              onSubmit={(event) => {
                event.preventDefault();
                void create();
              }}
            >
              <h3>New project</h3>
              <div className="pw-field">
                <label htmlFor={`${fieldID}-new-name`}>Name</label>
                <input
                  id={`${fieldID}-new-name`}
                  value={createDraft.name}
                  maxLength={160}
                  onChange={(event) => setCreateDraft({ ...createDraft, name: event.target.value })}
                />
              </div>
              <div className="pw-field">
                <label htmlFor={`${fieldID}-new-brief`}>Brief</label>
                <textarea
                  id={`${fieldID}-new-brief`}
                  rows={7}
                  value={createDraft.brief}
                  onChange={(event) => setCreateDraft({ ...createDraft, brief: event.target.value })}
                  placeholder="What this project is for, who it serves, and anything its agents should always keep in mind."
                />
              </div>
              {renderMemberPicker(createDraft, setCreateDraft, `${fieldID}-new`, createBusy, agents)}
              {createError && (
                <p className="pw-error" role="alert">
                  {createError}
                </p>
              )}
              <div className="pw-actions">
                <button type="submit" className="pw-primary" disabled={createBusy}>
                  {createBusy ? "Creating…" : "Create project"}
                </button>
                <button type="button" onClick={() => setCreating(false)} disabled={createBusy}>
                  Cancel
                </button>
              </div>
            </form>
          ) : !selectedID ? (
            <p className="pw-empty">Pick a project to read its brief, change who is on it, or see its history.</p>
          ) : detailError && !project ? (
            <p className="pw-error" role="alert">
              {detailError}
            </p>
          ) : !project ? (
            <p role="status">Loading project…</p>
          ) : (
            <article className="pw-detail" aria-busy={detailBusy}>
              <div className="pw-detail-head">
                <h3>{project.name}</h3>
                <span className={`pw-badge is-${project.status}`}>{projectStatusLabel(project)}</span>
              </div>
              <p className="pw-help">
                {briefRevisionLabel(project.brief_revision)} · updated {timestampLabel(project.updated_at)}
                {archived ? ` · archived ${timestampLabel(project.archived_at ?? "")}` : ""}
              </p>
              {archived && (
                <p className="pw-help">
                  Archived projects keep their history. Their agents cannot start new work from this brief.
                </p>
              )}
              {detailError && (
                <p className="pw-error" role="alert">
                  {detailError}
                </p>
              )}

              <form
                className="pw-form"
                onSubmit={(event) => {
                  event.preventDefault();
                  void save();
                }}
              >
                <div className="pw-field">
                  <label htmlFor={`${fieldID}-name`}>Name</label>
                  <input
                    id={`${fieldID}-name`}
                    value={draft.name}
                    maxLength={160}
                    disabled={archived}
                    onChange={(event) => setDraft({ ...draft, name: event.target.value })}
                  />
                </div>
                <div className="pw-field">
                  <label htmlFor={`${fieldID}-brief`}>Brief</label>
                  <textarea
                    id={`${fieldID}-brief`}
                    rows={9}
                    value={draft.brief}
                    disabled={archived}
                    onChange={(event) => setDraft({ ...draft, brief: event.target.value })}
                  />
                  <p className="pw-help">
                    Saving a changed brief starts a new version. Tasks already queued keep the version they
                    were queued with, even if they only start running later.
                  </p>
                </div>
                {renderMemberPicker(draft, setDraft, fieldID, archived || saving, memberOptions)}
                {losing.length > 0 && (
                  <p className="pw-help">
                    Saving removes {losing.map((member) => member.name || member.agent_id).join(", ")} from this
                    project. Removed agents cannot start new work from it.
                  </p>
                )}
                {saveError && (
                  <p className="pw-error" role="alert">
                    {saveError}
                  </p>
                )}
                {conflict && (
                  <div className="pw-actions">
                    <button type="button" onClick={() => loadLatest(true)}>
                      Load latest, keep my wording
                    </button>
                    <button type="button" onClick={() => loadLatest(false)}>
                      Discard my changes
                    </button>
                  </div>
                )}
                <div className="pw-actions">
                  <button type="submit" className="pw-primary" disabled={archived || saving || !dirty}>
                    {saving ? "Saving…" : "Save changes"}
                  </button>
                  <button
                    type="button"
                    disabled={!dirty || saving}
                    onClick={() => {
                      setDraft(draftFromProject(project));
                      setSaveError("");
                      setConflict(false);
                    }}
                  >
                    Undo my changes
                  </button>
                  <button type="button" onClick={() => setHistoryOpen((open) => !open)}>
                    {historyOpen ? "Hide brief history" : "Brief history"}
                  </button>
                  {!archived &&
                    (confirmArchive ? (
                      <>
                        <button type="button" className="pw-danger" disabled={saving} onClick={() => void archive()}>
                          {saving ? "Archiving…" : "Confirm archive"}
                        </button>
                        <button type="button" disabled={saving} onClick={() => setConfirmArchive(false)}>
                          Keep it open
                        </button>
                      </>
                    ) : (
                      <button type="button" disabled={saving} onClick={() => setConfirmArchive(true)}>
                        Archive project
                      </button>
                    ))}
                </div>
                {confirmArchive && (
                  <p className="pw-help">
                    Archiving stops new tasks and routines from using this brief. Finished results stay where
                    they are.
                  </p>
                )}
              </form>

              {historyOpen && (
                <div className="pw-history">
                  <h4>Brief history</h4>
                  {revisions.length === 0 ? (
                    <p className="pw-empty">Nothing saved yet.</p>
                  ) : (
                    <ol className="pw-revisions">
                      {[...revisions]
                        .sort((left, right) => right.revision - left.revision)
                        .map((revision) => (
                          <li key={revision.revision}>
                            <div className="pw-revision-head">
                              <strong>{briefRevisionLabel(revision.revision)}</strong>
                              <time dateTime={revision.created_at}>{timestampLabel(revision.created_at)}</time>
                              {revision.revision === project.brief_revision && (
                                <span className="pw-badge is-active">In use</span>
                              )}
                            </div>
                            <pre>{revision.brief || "(empty brief)"}</pre>
                          </li>
                        ))}
                    </ol>
                  )}
                </div>
              )}

              <div className="pw-history">
                <h4>Agents on this project</h4>
                {project.members.length === 0 ? (
                  <p className="pw-empty">No agents yet.</p>
                ) : (
                  <ul className="pw-member-list">
                    {project.members.map((member) => (
                      <li key={member.agent_id}>
                        <strong>{member.name || member.agent_id}</strong>
                        {member.title && <span>{member.title}</span>}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </article>
          )}
        </div>
      </div>
    </section>
  );
}

// ProjectSelector puts one conversation or one routine on a project. It offers
// only projects the owning agent belongs to, because those are the only ones
// the server will accept.
export function ProjectSelector({ apiFetch, subjectType, subjectID, agentID }: ProjectSelectorProps) {
  const selectID = useId();
  const [projects, setProjects] = useState<Project[]>([]);
  const [association, setAssociation] = useState<ProjectAssociation | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const writeGeneration = useRef(0);
  // A project created, renamed or archived in the workspace has to show up
  // here while this selector stays mounted.
  const revision = useSyncExternalStore(subscribeProjects, projectsRevision, projectsRevision);

  useEffect(() => {
    if (!subjectID) return;
    const abort = new AbortController();
    setLoading(true);
    setError("");
    setNotice("");
    void (async () => {
      try {
        const [listResponse, currentResponse] = await Promise.all([
          apiFetch("/api/projects", { signal: abort.signal }),
          apiFetch(
            `/api/project-associations/${subjectType}/${encodeURIComponent(subjectID)}`,
            { signal: abort.signal },
          ),
        ]);
        const listBody = await (await checked(listResponse)).json();
        // A missing row is the normal "not on a project" answer, not a failure.
        const currentBody = currentResponse.status === 404 ? null : await (await checked(currentResponse)).json();
        if (abort.signal.aborted) return;
        setProjects(Array.isArray(listBody.projects) ? listBody.projects : []);
        setAssociation(currentBody?.association ?? null);
      } catch (cause) {
        if (!isAborted(cause) && !abort.signal.aborted) setError(failureText(cause));
      } finally {
        if (!abort.signal.aborted) setLoading(false);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, subjectType, subjectID, revision]);

  const options = useMemo(() => assignableProjects(projects, agentID), [projects, agentID]);
  const current = association?.project_id ?? "";
  // A project the agent has left, or one that was archived, is no longer in the
  // list but is still what this work is attached to.
  const detached = current && !options.some((option) => option.id === current);

  async function assign(projectID: string) {
    const generation = ++writeGeneration.current;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      if (!projectID) {
        await checked(
          await apiFetch(`/api/project-associations/${subjectType}/${encodeURIComponent(subjectID)}`, {
            method: "DELETE",
          }),
        );
        if (generation !== writeGeneration.current) return;
        setAssociation(null);
        setNotice("Removed from the project.");
        return;
      }
      const body = await (
        await checked(
          await apiFetch(`/api/project-associations/${subjectType}/${encodeURIComponent(subjectID)}`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ project_id: projectID }),
          }),
        )
      ).json();
      if (generation !== writeGeneration.current) return;
      setAssociation(body.association ?? null);
      setNotice(`New work here uses the ${body.association?.project_name ?? "project"} brief.`);
    } catch (cause) {
      if (generation === writeGeneration.current) setError(failureText(cause));
    } finally {
      if (generation === writeGeneration.current) setBusy(false);
    }
  }

  if (!subjectID) return null;

  return (
    <div className="project-selector">
      <label htmlFor={selectID}>Project</label>
      <select
        id={selectID}
        value={current}
        disabled={loading || busy}
        onChange={(event) => void assign(event.target.value)}
      >
        <option value="">{loading ? "Loading…" : "No project"}</option>
        {detached && association && (
          <option value={association.project_id}>{association.project_name} (unavailable)</option>
        )}
        {options.map((project) => (
          <option key={project.id} value={project.id}>
            {project.name}
          </option>
        ))}
      </select>
      {busy && (
        <span className="project-selector-note" role="status">
          Saving…
        </span>
      )}
      {notice && !busy && (
        <span className="project-selector-note" role="status">
          {notice}
        </span>
      )}
      {error && (
        <span className="project-selector-note is-error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
