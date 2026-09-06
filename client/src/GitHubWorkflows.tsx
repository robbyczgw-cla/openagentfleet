import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import "./GitHubWorkflows.css";
import {
  canReview,
  taskOutcome,
  taskStatusText,
  commandPreview,
  defaultPullRequestBody,
  defaultPullRequestTitle,
  describeWorkflowError,
  diffSummary,
  emptyStartDraft,
  matchStartedWorkflow,
  parseTestCommand,
  publishDisabledReason,
  reviewBlockedReason,
  reviewReady,
  startRequestBody,
  validateStartDraft,
  validateTestCommand,
  workflowStateDetail,
  workflowStateKey,
  workflowStateLabel,
  workflowStatusLabel,
} from "./githubWorkflowModel";
import type { GitHubWorkflow, StartDraft } from "./githubWorkflowModel";

// The parent renders the dialog: backdrop, role, focus trap and Escape all live
// in App.tsx. This component is only the contents, so it adds no outer chrome
// and never moves focus on its own.
//
// OpenAgentFleet pushes nothing on its own. The agent works in a separate
// checkout and is instructed not to push; the review only reads that checkout
// and runs the command the reviewer typed; the push and the draft pull request
// happen on one explicit, confirmed click. The agent's own engine still owns
// its shell, so this screen describes what OpenAgentFleet does, not what the
// engine is prevented from doing.

type ApiFetch = (path: string, init?: RequestInit) => Promise<Response>;
type Agent = { id: string; name: string };
type ApiFailure = Error & { status?: number; testOutput?: string };
type Connection = {
  enabled: boolean;
  repositories: string[];
  agent_ids: string[];
  installed: boolean;
  authenticated: boolean;
  login: string;
};

export type GitHubWorkflowsProps = {
  apiFetch: ApiFetch;
  agents: Agent[];
  onClose: () => void;
  onConversation: (conversationID: string) => void;
};

const DIFF_DISPLAY_LIMIT = 200_000;

async function checked(response: Response): Promise<Response> {
  if (response.ok) return response;
  const body = await response.json().catch(() => ({}) as { error?: string; test_output?: string });
  const failure: ApiFailure = new Error(body.error || `Request failed (${response.status})`);
  failure.status = response.status;
  if (typeof body.test_output === "string") failure.testOutput = body.test_output;
  throw failure;
}

function failureText(cause: unknown): string {
  if (cause instanceof Error) return describeWorkflowError((cause as ApiFailure).status ?? 0, cause.message);
  return describeWorkflowError(0, String(cause));
}

function isAborted(cause: unknown): boolean {
  return cause instanceof DOMException && cause.name === "AbortError";
}

function timestampLabel(value: string): string {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

function clip(value: string): { text: string; clipped: boolean } {
  if (value.length <= DIFF_DISPLAY_LIMIT) return { text: value, clipped: false };
  return { text: value.slice(0, DIFF_DISPLAY_LIMIT), clipped: true };
}

export function GitHubWorkflows({ apiFetch, agents, onClose, onConversation }: GitHubWorkflowsProps) {
  const fieldID = useId();
  const [connection, setConnection] = useState<Connection | null>(null);
  const [items, setItems] = useState<GitHubWorkflow[]>([]);
  const [listBusy, setListBusy] = useState(true);
  const [listError, setListError] = useState("");
  const [listRefresh, setListRefresh] = useState(0);

  const [selectedID, setSelectedID] = useState("");
  const [workflow, setWorkflow] = useState<GitHubWorkflow | null>(null);
  const [detailError, setDetailError] = useState("");
  const [detailRefresh, setDetailRefresh] = useState(0);
  const [detailBusy, setDetailBusy] = useState(false);
  const [taskStatus, setTaskStatus] = useState("");

  const [starting, setStarting] = useState(false);
  const [startOpen, setStartOpen] = useState(false);
  const [startDraft, setStartDraft] = useState<StartDraft>(() => emptyStartDraft());
  const [startError, setStartError] = useState("");

  const [executable, setExecutable] = useState("");
  const [argumentLines, setArgumentLines] = useState("");
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState("");
  const [failedOutput, setFailedOutput] = useState("");

  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [confirmPublish, setConfirmPublish] = useState(false);
  const [publishBusy, setPublishBusy] = useState(false);
  const [publishError, setPublishError] = useState("");

  // A notice belongs to one piece of work. Selecting the workflow it describes
  // must not wipe it, which is what a plain string did.
  const [notice, setNotice] = useState<{ workflowID: string; text: string } | null>(null);
  const detailGeneration = useRef(0);
  // The pull request wording and the check command are seeded once per piece of
  // work. A background refresh must never overwrite what is being typed.
  const seededFor = useRef("");

  useEffect(() => {
    const abort = new AbortController();
    void (async () => {
      try {
        const body: Connection = await (
          await checked(await apiFetch("/api/connections/github", { signal: abort.signal }))
        ).json();
        if (!abort.signal.aborted) setConnection(body);
      } catch (cause) {
        if (!isAborted(cause) && !abort.signal.aborted) setListError(failureText(cause));
      }
    })();
    return () => abort.abort();
  }, [apiFetch]);

  useEffect(() => {
    const abort = new AbortController();
    setListBusy(true);
    void (async () => {
      try {
        const body = await (
          await checked(await apiFetch("/api/github-workflows", { signal: abort.signal }))
        ).json();
        if (abort.signal.aborted) return;
        setItems(Array.isArray(body.items) ? body.items : []);
        setListError("");
      } catch (cause) {
        if (!isAborted(cause) && !abort.signal.aborted) setListError(failureText(cause));
      } finally {
        if (!abort.signal.aborted) setListBusy(false);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, listRefresh]);

  useEffect(() => {
    setTaskStatus("");
    setReviewError("");
    setFailedOutput("");
    setPublishError("");
    setConfirmPublish(false);
  }, [selectedID]);

  useEffect(() => {
    if (!selectedID) {
      setWorkflow(null);
      return;
    }
    const abort = new AbortController();
    const generation = ++detailGeneration.current;
    setDetailBusy(true);
    setDetailError("");
    void (async () => {
      try {
        const loaded: GitHubWorkflow = await (
          await checked(
            await apiFetch(`/api/github-workflows/${encodeURIComponent(selectedID)}`, { signal: abort.signal }),
          )
        ).json();
        // An answer for a piece of work the reviewer already left is dropped.
        if (abort.signal.aborted || generation !== detailGeneration.current) return;
        setWorkflow(loaded);
        if (seededFor.current !== loaded.id) {
          seededFor.current = loaded.id;
          setTitle(defaultPullRequestTitle(loaded));
          setBody(defaultPullRequestBody(loaded));
          const previous = loaded.review?.test_command ?? [];
          setExecutable(previous[0] ?? "");
          setArgumentLines(previous.slice(1).join("\n"));
        }
        if (!loaded.task_run_id) {
          setTaskStatus("");
          return;
        }
        const taskBody = await (
          await checked(
            await apiFetch(`/api/tasks/${encodeURIComponent(loaded.task_run_id)}`, { signal: abort.signal }),
          )
        ).json();
        if (!abort.signal.aborted && generation === detailGeneration.current) {
          setTaskStatus(String(taskBody.task?.status ?? ""));
        }
      } catch (cause) {
        if (!isAborted(cause) && generation === detailGeneration.current) setDetailError(failureText(cause));
      } finally {
        if (generation === detailGeneration.current) setDetailBusy(false);
      }
    })();
    return () => abort.abort();
  }, [apiFetch, selectedID, detailRefresh]);

  // Reading is safe to repeat, so an unfinished piece of work refreshes itself.
  // Nothing that writes is ever on a timer.
  const settled = ["completed", "failed", "stopped", "blocked"].includes(taskStatus);
  const waiting = workflow ? ["preparing", "running"].includes(workflow.status) && !settled : false;
  useEffect(() => {
    if (!selectedID || !waiting) return;
    const timer = window.setInterval(() => setDetailRefresh((count) => count + 1), 6000);
    return () => window.clearInterval(timer);
  }, [selectedID, waiting]);

  const grantedRepositories = connection?.repositories ?? [];
  const grantedAgents = useMemo(
    () => agents.filter((agent) => (connection?.agent_ids ?? []).includes(agent.id)),
    [agents, connection],
  );
  const agentNames = useMemo(() => new Map(agents.map((agent) => [agent.id, agent.name])), [agents]);
  const connected = Boolean(connection?.enabled && connection.authenticated && connection.installed);

  const command = useMemo(() => parseTestCommand(executable, argumentLines), [executable, argumentLines]);

  const select = useCallback((id: string) => {
    setSelectedID(id);
    setStartOpen(false);
  }, []);

  async function start() {
    if (starting) return;
    const problem = validateStartDraft(startDraft);
    if (problem) {
      setStartError(problem);
      return;
    }
    setStarting(true);
    setStartError("");
    try {
      const response = await checked(
        await apiFetch("/api/github-workflows", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(startRequestBody(startDraft)),
        }),
      );
      const started = await response.json();
      // The start answer describes the conversation and run. The custom header
      // is not readable in every build, so the list is the reliable way back to
      // the new piece of work.
      const headerID = response.headers.get("X-GitHub-Workflow-ID") ?? "";
      const listBody = await (await checked(await apiFetch("/api/github-workflows"))).json();
      const fresh: GitHubWorkflow[] = Array.isArray(listBody.items) ? listBody.items : [];
      setItems(fresh);
      const match = matchStartedWorkflow(fresh, {
        workflowID: headerID,
        conversationID: started.message?.conversation_id,
        runID: started.run?.id,
      });
      setStartOpen(false);
      if (match) {
        seededFor.current = "";
        select(match.id);
        setNotice({
          workflowID: match.id,
          text: `${agentNames.get(match.agent_id) ?? "The agent"} is working on ${match.repository}#${match.issue_number} in its own checkout.`,
        });
      } else {
        setListRefresh((count) => count + 1);
      }
    } catch (cause) {
      setStartError(failureText(cause));
    } finally {
      setStarting(false);
    }
  }

  async function review() {
    if (!workflow || reviewBusy) return;
    const problem = validateTestCommand(command);
    if (problem) {
      setReviewError(problem);
      setFailedOutput("");
      return;
    }
    setReviewBusy(true);
    setReviewError("");
    setFailedOutput("");
    setNotice(null);
    try {
      const updated: GitHubWorkflow = await (
        await checked(
          await apiFetch(`/api/github-workflows/${encodeURIComponent(workflow.id)}/review`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ test_command: command }),
          }),
        )
      ).json();
      setWorkflow(updated);
      setListRefresh((count) => count + 1);
      setNotice({ workflowID: workflow.id, text: "Review ready. Read the changes and the check output before you publish anything." });
    } catch (cause) {
      setReviewError(failureText(cause));
      const output = cause instanceof Error ? ((cause as ApiFailure).testOutput ?? "") : "";
      if (output) setFailedOutput(output);
    } finally {
      setReviewBusy(false);
    }
  }

  async function publish() {
    if (!workflow || publishBusy || !workflow.review) return;
    setPublishBusy(true);
    setPublishError("");
    setNotice(null);
    try {
      const updated: GitHubWorkflow = await (
        await checked(
          await apiFetch(`/api/github-workflows/${encodeURIComponent(workflow.id)}/publish`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              review_token: workflow.review.token,
              review_digest: workflow.review.digest,
              expected_login: workflow.expected_login,
              title: title.trim(),
              body,
            }),
          }),
        )
      ).json();
      setWorkflow(updated);
      setConfirmPublish(false);
      setListRefresh((count) => count + 1);
      setNotice({
        workflowID: workflow.id,
        text: updated.pull_request_url
          ? "Draft pull request opened. Nothing is merged, and it stays a draft until you say otherwise."
          : "The request finished, but no pull request came back. Refresh to see where it stopped.",
      });
    } catch (cause) {
      // The wording stays exactly as typed so it can be sent again, and the
      // refresh below shows whether the branch or the pull request got through.
      setPublishError(failureText(cause));
    } finally {
      setPublishBusy(false);
    }
  }

  const review0 = workflow?.review ?? null;
  const changes = review0 ? diffSummary(review0.diff) : { added: 0, removed: 0 };
  const diffText = review0 ? clip(review0.diff) : { text: "", clipped: false };
  const outputText = review0 ? clip(review0.test_output) : { text: "", clipped: false };
  const failedText = clip(failedOutput);
  const publishBlocked = workflow ? publishDisabledReason(workflow) : "";
  const blockedReason = workflow ? reviewBlockedReason(workflow, taskStatus) : "";
  const reviewable = workflow ? canReview(workflow, taskStatus) : false;

  return (
    <section className="github-workflows">
      <header className="gw-heading">
        <div>
          <div className="eyebrow">Issue to draft pull request</div>
          <h2 id="workspace-panel-title">GitHub work</h2>
          <p>
            An agent works on one issue in a separate checkout on this computer. You read the result, run your
            own checks, and decide whether to push it and open a draft pull request.
          </p>
        </div>
        <button type="button" onClick={onClose} aria-label="Close GitHub work">
          Close
        </button>
      </header>

      {notice && (notice.workflowID === selectedID || !selectedID) && (
        <p className="gw-notice" role="status">
          {notice.text}
        </p>
      )}
      {listError && (
        <p className="gw-error" role="alert">
          {listError}
        </p>
      )}
      {connection && !connected && (
        <p className="gw-help">
          {connection.installed
            ? "Connect GitHub and grant a repository and an agent in Connected apps before starting work here."
            : "The GitHub command line tool is not installed on this computer, so this workspace cannot reach GitHub."}
        </p>
      )}

      <div className="gw-columns">
        <div className="gw-list-column">
          <div className="gw-list-head">
            <button
              type="button"
              className="gw-primary"
              disabled={!connected}
              onClick={() => {
                setStartOpen(true);
                setSelectedID("");
                setStartError("");
                setNotice(null);
              }}
            >
              Start from an issue
            </button>
            <button type="button" onClick={() => setListRefresh((count) => count + 1)} disabled={listBusy}>
              Refresh
            </button>
          </div>
          <div className="gw-list" aria-label="GitHub work" aria-busy={listBusy}>
            {listBusy && items.length === 0 ? (
              <p role="status">Loading…</p>
            ) : items.length === 0 ? (
              <p>Nothing here yet. Start from an issue to have an agent work on it.</p>
            ) : (
              items.map((item) => (
                <button
                  type="button"
                  key={item.id}
                  className={`gw-item${item.id === selectedID ? " is-on" : ""}`}
                  aria-pressed={item.id === selectedID}
                  onClick={() => select(item.id)}
                >
                  <span className="gw-item-name">
                    {item.repository}#{item.issue_number}
                  </span>
                  <span className={`gw-badge is-${item.id === selectedID ? workflowStateKey(item, taskStatus) : item.status}`}>
                    {item.id === selectedID ? workflowStateLabel(item, taskStatus) : workflowStatusLabel(item.status)}
                  </span>
                  <span className="gw-item-meta">
                    {item.issue_title || "Untitled issue"}
                  </span>
                  <span className="gw-item-meta">
                    {agentNames.get(item.agent_id) ?? item.agent_id} · {timestampLabel(item.created_at)}
                  </span>
                </button>
              ))
            )}
          </div>
        </div>

        <div className="gw-detail-column">
          {startOpen ? (
            <form
              className="gw-form"
              onSubmit={(event) => {
                event.preventDefault();
                void start();
              }}
            >
              <h3>Start from an issue</h3>
              <p className="gw-help">
                The agent gets its own checkout of the repository on this computer and is told not to push
                and not to open a pull request. OpenAgentFleet pushes this branch only after you approve the
                review.
              </p>
              <div className="gw-field">
                <label htmlFor={`${fieldID}-repo`}>Repository</label>
                <select
                  id={`${fieldID}-repo`}
                  value={startDraft.repository}
                  disabled={starting}
                  onChange={(event) => setStartDraft({ ...startDraft, repository: event.target.value })}
                >
                  <option value="">Pick a connected repository</option>
                  {grantedRepositories.map((name) => (
                    <option key={name} value={name}>
                      {name}
                    </option>
                  ))}
                </select>
                <p className="gw-help">Only repositories you have already granted appear here.</p>
              </div>
              <div className="gw-field">
                <label htmlFor={`${fieldID}-issue`}>Issue number</label>
                <input
                  id={`${fieldID}-issue`}
                  inputMode="numeric"
                  value={startDraft.issueNumber}
                  disabled={starting}
                  placeholder="214"
                  onChange={(event) => setStartDraft({ ...startDraft, issueNumber: event.target.value })}
                />
              </div>
              <div className="gw-field">
                <label htmlFor={`${fieldID}-agent`}>Agent</label>
                <select
                  id={`${fieldID}-agent`}
                  value={startDraft.agentID}
                  disabled={starting}
                  onChange={(event) => setStartDraft({ ...startDraft, agentID: event.target.value })}
                >
                  <option value="">Pick a granted agent</option>
                  {grantedAgents.map((agent) => (
                    <option key={agent.id} value={agent.id}>
                      {agent.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="gw-field">
                <label htmlFor={`${fieldID}-path`}>Repository folder on this computer</label>
                <input
                  id={`${fieldID}-path`}
                  value={startDraft.localRepoPath}
                  disabled={starting}
                  placeholder="/home/you/code/your-repo"
                  onChange={(event) => setStartDraft({ ...startDraft, localRepoPath: event.target.value })}
                />
                <p className="gw-help">
                  The top folder of your checkout. Its origin has to be the repository you picked above.
                </p>
              </div>
              <div className="gw-field">
                <label htmlFor={`${fieldID}-base`}>Start from branch</label>
                <input
                  id={`${fieldID}-base`}
                  value={startDraft.baseRef}
                  disabled={starting}
                  placeholder="main"
                  onChange={(event) => setStartDraft({ ...startDraft, baseRef: event.target.value })}
                />
                <p className="gw-help">A branch that already exists in that folder. The work starts from it.</p>
              </div>
              {startError && (
                <p className="gw-error" role="alert">
                  {startError}
                </p>
              )}
              <div className="gw-actions">
                <button type="submit" className="gw-primary" disabled={starting || !connected}>
                  {starting ? "Setting up…" : "Start the work"}
                </button>
                <button type="button" disabled={starting} onClick={() => setStartOpen(false)}>
                  Cancel
                </button>
              </div>
            </form>
          ) : !selectedID ? (
            <p className="gw-empty">Pick a piece of work to see what the agent changed, or start from an issue.</p>
          ) : detailError && !workflow ? (
            <p className="gw-error" role="alert">
              {detailError}
            </p>
          ) : !workflow ? (
            <p role="status">Loading…</p>
          ) : (
            <article className="gw-detail" aria-busy={detailBusy}>
              <div className="gw-detail-head">
                <h3>
                  {workflow.repository}#{workflow.issue_number}
                </h3>
                <span className={`gw-badge is-${workflowStateKey(workflow, taskStatus)}`}>
                  {workflowStateLabel(workflow, taskStatus)}
                </span>
              </div>
              <p className="gw-issue-title">{workflow.issue_title || "Untitled issue"}</p>
              <p className="gw-help">{workflowStateDetail(workflow, taskStatus)}</p>
              {detailError && (
                <p className="gw-error" role="alert">
                  {detailError}
                </p>
              )}
              {workflow.error && (
                <p className="gw-error" role="alert">
                  {describeWorkflowError(0, workflow.error)}
                </p>
              )}

              <dl className="gw-facts">
                <div>
                  <dt>Issue</dt>
                  <dd>
                    {workflow.issue_url ? (
                      <a href={workflow.issue_url} target="_blank" rel="noreferrer noopener">
                        {workflow.repository}#{workflow.issue_number}
                      </a>
                    ) : (
                      `${workflow.repository}#${workflow.issue_number}`
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Agent</dt>
                  <dd>{agentNames.get(workflow.agent_id) ?? workflow.agent_id}</dd>
                </div>
                <div>
                  <dt>Branch</dt>
                  <dd>
                    <code>{workflow.branch}</code>
                  </dd>
                </div>
                <div>
                  <dt>Starts from</dt>
                  <dd>
                    <code>{workflow.base_ref}</code>
                  </dd>
                </div>
                <div>
                  <dt>Separate checkout</dt>
                  <dd>
                    <code>{workflow.worktree_path}</code>
                  </dd>
                </div>
                <div>
                  <dt>Publishes as</dt>
                  <dd>{workflow.expected_login || "the connected GitHub account"}</dd>
                </div>
                <div>
                  <dt>Agent's task</dt>
                  <dd>{taskStatus ? taskStatusText(taskStatus) : workflow.task_run_id ? "Loading…" : "Not started"}</dd>
                </div>
              </dl>

              <div className="gw-actions">
                <button type="button" onClick={() => onConversation(workflow.conversation_id)}>
                  Open the agent's conversation
                </button>
                <button type="button" disabled={detailBusy} onClick={() => setDetailRefresh((count) => count + 1)}>
                  Refresh
                </button>
                {workflow.pull_request_url && (
                  <a className="gw-link-button" href={workflow.pull_request_url} target="_blank" rel="noreferrer noopener">
                    Open the draft pull request
                  </a>
                )}
              </div>

              <section className="gw-section" aria-labelledby={`${fieldID}-review-title`}>
                <h4 id={`${fieldID}-review-title`}>Check the work</h4>
                <p className="gw-help">
                  The command below runs in the agent's checkout, exactly as typed. There is no shell, so each
                  line is one argument on its own. For example <code>go</code> with the lines <code>test</code>{" "}
                  and <code>./...</code>.
                </p>
                <div className="gw-field-row">
                  <div className="gw-field">
                    <label htmlFor={`${fieldID}-exe`}>Program</label>
                    <input
                      id={`${fieldID}-exe`}
                      value={executable}
                      disabled={reviewBusy}
                      placeholder="go"
                      onChange={(event) => setExecutable(event.target.value)}
                    />
                  </div>
                  <div className="gw-field">
                    <label htmlFor={`${fieldID}-args`}>Arguments, one per line</label>
                    <textarea
                      id={`${fieldID}-args`}
                      rows={4}
                      value={argumentLines}
                      disabled={reviewBusy}
                      placeholder={"test\n./..."}
                      onChange={(event) => setArgumentLines(event.target.value)}
                    />
                  </div>
                </div>
                <p className="gw-preview">
                  <span>Runs</span> <code>{commandPreview(command) || "nothing yet"}</code>
                </p>
                {!reviewable && blockedReason && (
                  <div className="gw-blocked">
                    <p className="gw-help">{blockedReason}</p>
                    {taskOutcome(taskStatus) === "unsuccessful" && (
                      <div className="gw-actions">
                        <button type="button" onClick={() => onConversation(workflow.conversation_id)}>
                          Open the agent's conversation
                        </button>
                      </div>
                    )}
                  </div>
                )}
                {reviewError && (
                  <p className="gw-error" role="alert">
                    {reviewError}
                  </p>
                )}
                {failedOutput && (
                  <div className="gw-output">
                    <h5>Output from the failed check</h5>
                    <pre tabIndex={0} aria-label="Output from the failed check">{failedText.text}</pre>
                    {failedText.clipped && <p className="gw-help">Output shortened for display.</p>}
                  </div>
                )}
                <div className="gw-actions">
                  <button
                    type="button"
                    className="gw-primary"
                    disabled={reviewBusy || !reviewable}
                    onClick={() => void review()}
                  >
                    {reviewBusy
                      ? "Running your check…"
                      : reviewReady(workflow)
                        ? "Run the check again and rebuild the review"
                        : "Run the check and build the review"}
                  </button>
                </div>
                {reviewReady(workflow) && review0 && (
                  <div className="gw-review">
                    <p className="gw-help">
                      Built {timestampLabel(review0.created_at)} from commit <code>{review0.head_commit.slice(0, 12)}</code>{" "}
                      on top of <code>{review0.base_commit.slice(0, 12)}</code>.
                    </p>
                    <h5>
                      Changed files ({review0.changed_files.length}) · {changes.added} added lines, {changes.removed}{" "}
                      removed
                    </h5>
                    <ul className="gw-files">
                      {review0.changed_files.map((file) => (
                        <li key={file}>
                          <code>{file}</code>
                        </li>
                      ))}
                    </ul>
                    <h5>Changes</h5>
                    <pre className="gw-diff" tabIndex={0} aria-label="Changes in this review">{diffText.text}</pre>
                    {diffText.clipped && (
                      <p className="gw-help">
                        The changes are too long to show in full here. Read the rest in the checkout above.
                      </p>
                    )}
                    <h5>Check output</h5>
                    <p className="gw-help">
                      Ran <code>{commandPreview(review0.test_command)}</code>
                    </p>
                    <pre className="gw-output-block" tabIndex={0} aria-label="Check output">
                      {outputText.text || "The check finished without output."}
                    </pre>
                    {outputText.clipped && <p className="gw-help">Output shortened for display.</p>}
                  </div>
                )}
              </section>

              <section className="gw-section" aria-labelledby={`${fieldID}-publish-title`}>
                <h4 id={`${fieldID}-publish-title`}>Publish</h4>
                <p className="gw-help">
                  {workflow.pushed
                    ? "OpenAgentFleet has already pushed this branch."
                    : "OpenAgentFleet has not pushed anything yet."}{" "}
                  Publishing pushes the branch <code>{workflow.branch}</code>{" "}
                  to origin and opens a draft pull request into <code>{workflow.base_ref}</code> as{" "}
                  {workflow.expected_login || "the connected account"}. It stays a draft and nothing is merged.
                </p>
                {publishBlocked && <p className="gw-help">{publishBlocked}</p>}
                <div className="gw-field">
                  <label htmlFor={`${fieldID}-title`}>Pull request title</label>
                  <input
                    id={`${fieldID}-title`}
                    value={title}
                    maxLength={1024}
                    disabled={publishBusy}
                    onChange={(event) => setTitle(event.target.value)}
                  />
                </div>
                <div className="gw-field">
                  <label htmlFor={`${fieldID}-body`}>Pull request description</label>
                  <textarea
                    id={`${fieldID}-body`}
                    rows={6}
                    value={body}
                    disabled={publishBusy}
                    onChange={(event) => setBody(event.target.value)}
                  />
                </div>
                {publishError && (
                  <>
                    <p className="gw-error" role="alert">
                      {publishError}
                    </p>
                    <p className="gw-help">
                      Your title and description are still here. Refresh to see whether the branch or the pull
                      request got through before trying again.
                    </p>
                  </>
                )}
                {workflow.pushed && !workflow.pull_request_url && (
                  <p className="gw-help">
                    The branch is already on GitHub. Publishing again opens the draft pull request without
                    pushing twice.
                  </p>
                )}
                <div className="gw-actions">
                  {confirmPublish ? (
                    <>
                      <button
                        type="button"
                        className="gw-primary"
                        disabled={publishBusy || !title.trim()}
                        onClick={() => void publish()}
                      >
                        {publishBusy ? "Pushing and opening the draft…" : "Yes, push and open the draft"}
                      </button>
                      <button type="button" disabled={publishBusy} onClick={() => setConfirmPublish(false)}>
                        Not yet
                      </button>
                    </>
                  ) : (
                    <button
                      type="button"
                      className="gw-primary"
                      disabled={Boolean(publishBlocked) || publishBusy || !title.trim()}
                      onClick={() => {
                        setPublishError("");
                        setConfirmPublish(true);
                      }}
                    >
                      Create draft pull request
                    </button>
                  )}
                  <button type="button" disabled={publishBusy} onClick={() => setDetailRefresh((count) => count + 1)}>
                    Refresh
                  </button>
                </div>
                {confirmPublish && (
                  <p className="gw-confirm" role="status">
                    This pushes {review0?.changed_files.length ?? 0} changed files to{" "}
                    <code>{workflow.branch}</code> on GitHub and opens one draft pull request. This is the
                    only step where OpenAgentFleet writes to GitHub.
                  </p>
                )}
              </section>
            </article>
          )}
        </div>
      </div>
    </section>
  );
}
