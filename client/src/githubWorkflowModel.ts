// GitHub workflow state that does not touch React or the DOM, so it can be
// tested directly with node --test.

import { fallbackErrorText } from "./apiErrors.ts";

export type GitHubWorkflowReview = {
  token: string;
  digest: string;
  base_commit: string;
  head_commit: string;
  changed_files: string[];
  diff: string;
  test_command: string[];
  test_output: string;
  run_state_digest: string;
  created_at: string;
};

export type GitHubWorkflow = {
  id: string;
  status: string;
  repository: string;
  issue_number: number;
  issue_title: string;
  issue_url: string;
  agent_id: string;
  conversation_id: string;
  task_run_id?: string;
  local_repo_path: string;
  worktree_path: string;
  base_ref: string;
  base_commit: string;
  branch: string;
  expected_login: string;
  review?: GitHubWorkflowReview | null;
  pushed: boolean;
  pull_request_url?: string;
  pull_request_number?: number;
  error?: string;
  created_at: string;
  updated_at: string;
};

export type StartDraft = {
  repository: string;
  issueNumber: string;
  agentID: string;
  localRepoPath: string;
  baseRef: string;
};

export type StartRequest = {
  local_repo_path: string;
  repository: string;
  issue_number: number;
  agent_id: string;
  base_ref: string;
};

// Mirrors internal/httpapi/github_workflows.go.
export const TEST_COMMAND_MAX_PARTS = 32;
export const TEST_ARGUMENT_MAX_BYTES = 4096;

const REPOSITORY_PATTERN = /^[a-z0-9][a-z0-9-]{0,38}\/[a-z0-9_.-]{1,100}$/;
const BASE_REF_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,200}$/;
const encoder = new TextEncoder();

const STATUS_TEXT: Record<string, string> = {
  preparing: "Setting up",
  running: "With the agent",
  ready: "Ready to review",
  reviewed: "Reviewed",
  pushed: "Branch pushed",
  draft_pr: "Draft pull request open",
  publish_failed: "Publishing stopped",
};

const STATUS_DETAIL: Record<string, string> = {
  preparing: "A separate checkout is being made for this issue.",
  running: "The agent is working in its own checkout. OpenAgentFleet has not pushed anything.",
  ready: "The agent finished. Run your checks to build a review.",
  reviewed: "The changes and check output below are what a pull request would contain.",
  pushed: "The branch reached GitHub, but no pull request was opened yet.",
  draft_pr: "A draft pull request is open. Nothing was merged.",
  publish_failed: "The last publish attempt stopped part way. Refresh to see how far it got.",
};

export function workflowStatusLabel(status: string): string {
  return STATUS_TEXT[status] ?? status;
}

export function workflowStatusDetail(status: string): string {
  return STATUS_DETAIL[status] ?? "";
}

// The stored status only moves when a review or a publish happens, so a
// workflow whose task failed still reads "running". The screen has to describe
// the agent's task as well, or it promises progress that will never arrive.
export type TaskOutcome = "unknown" | "waiting" | "completed" | "unsuccessful";

export function taskOutcome(taskStatus: string): TaskOutcome {
  if (!taskStatus) return "unknown";
  if (taskStatus === "completed") return "completed";
  if (taskStatus === "failed" || taskStatus === "stopped" || taskStatus === "blocked") return "unsuccessful";
  return "waiting";
}

export function taskStatusText(taskStatus: string): string {
  return taskStatus.replace(/_/g, " ");
}

// Only the two statuses the server never advances on its own are reinterpreted.
// Once a review or publish has happened, the stored status is the truth.
function pending(workflow: GitHubWorkflow): boolean {
  return workflow.status === "preparing" || workflow.status === "running";
}

export function workflowStateKey(workflow: GitHubWorkflow, taskStatus: string): string {
  if (pending(workflow) && taskOutcome(taskStatus) === "unsuccessful") return "task_unsuccessful";
  if (pending(workflow) && taskOutcome(taskStatus) === "completed") return "ready";
  return workflow.status;
}

export function workflowStateLabel(workflow: GitHubWorkflow, taskStatus: string): string {
  const key = workflowStateKey(workflow, taskStatus);
  if (key === "task_unsuccessful") return "Task did not finish";
  return workflowStatusLabel(key);
}

export function workflowStateDetail(workflow: GitHubWorkflow, taskStatus: string): string {
  const key = workflowStateKey(workflow, taskStatus);
  if (key === "task_unsuccessful") {
    return `The agent's task ended as ${taskStatusText(taskStatus)} and stopped there. OpenAgentFleet has not pushed anything, and the separate checkout is still on this computer.`;
  }
  return workflowStatusDetail(key);
}

// One place decides whether a review can be built, and says why when it cannot.
export function reviewBlockedReason(workflow: GitHubWorkflow, taskStatus: string): string {
  if (workflow.status === "draft_pr") return "A draft pull request is already open for this branch.";
  if (!workflow.task_run_id) return "The agent has not started its task yet.";
  const outcome = taskOutcome(taskStatus);
  if (outcome === "unsuccessful") {
    return `A review needs a task that finished successfully, and this one ended as ${taskStatusText(taskStatus)}. Open the agent's conversation to see what happened, or run the task again from Tasks & results, then come back here.`;
  }
  if (outcome === "waiting") return "Wait for the agent's task to finish, then build the review.";
  return "";
}

export function emptyStartDraft(baseRef = "main"): StartDraft {
  return { repository: "", issueNumber: "", agentID: "", localRepoPath: "", baseRef };
}

export function looksAbsolutePath(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.startsWith("/") || /^[A-Za-z]:[\\/]/.test(trimmed);
}

// The server takes only an absolute, already-tidy path. Saying so here avoids a
// round trip for a trailing slash.
export function pathProblem(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "Point at the repository folder on this computer.";
  if (!looksAbsolutePath(trimmed)) return "Use the full path to the folder, starting at the top of the drive.";
  const parts = trimmed.split(/[\\/]+/);
  if (parts.some((part) => part === "." || part === "..")) {
    return "Use the plain path to the folder, without . or .. in it.";
  }
  if (/[\\/]{2,}/.test(trimmed.replace(/^[\\/]{2}/, "/"))) return "Remove the repeated slashes from the path.";
  if (trimmed.length > 1 && /[\\/]$/.test(trimmed)) return "Remove the trailing slash from the path.";
  return "";
}

export function baseRefProblem(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "Name the branch this work starts from.";
  if (
    !BASE_REF_PATTERN.test(trimmed) ||
    trimmed.includes("..") ||
    trimmed.includes("@{") ||
    trimmed.includes("//") ||
    trimmed.endsWith(".") ||
    trimmed.endsWith("/")
  ) {
    return "That is not a usable branch name.";
  }
  return "";
}

export function validateStartDraft(draft: StartDraft): string {
  if (!REPOSITORY_PATTERN.test(draft.repository.trim().toLowerCase())) {
    return "Pick one of the repositories you have already connected.";
  }
  const issue = Number(draft.issueNumber.trim());
  if (!Number.isInteger(issue) || issue < 1) return "Enter the issue number, for example 214.";
  if (!draft.agentID.trim()) return "Pick the agent that should do the work.";
  const path = pathProblem(draft.localRepoPath);
  if (path) return path;
  const ref = baseRefProblem(draft.baseRef);
  if (ref) return ref;
  return "";
}

export function startRequestBody(draft: StartDraft): StartRequest {
  return {
    local_repo_path: draft.localRepoPath.trim(),
    repository: draft.repository.trim().toLowerCase(),
    issue_number: Number(draft.issueNumber.trim()),
    agent_id: draft.agentID.trim(),
    base_ref: draft.baseRef.trim(),
  };
}

// Every argument is passed to the program exactly as typed. Nothing is split on
// spaces and no shell is involved, so one line is one argument.
export function parseTestCommand(executable: string, argumentLines: string): string[] {
  const program = executable.trim();
  const rest = argumentLines
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0);
  return program ? [program, ...rest] : rest;
}

export function validateTestCommand(command: string[]): string {
  if (command.length === 0 || !command[0].trim()) {
    return "Name the program to run, for example go.";
  }
  if (command.length > TEST_COMMAND_MAX_PARTS) {
    return `Use at most ${TEST_COMMAND_MAX_PARTS - 1} arguments.`;
  }
  for (const argument of command) {
    if (encoder.encode(argument).length > TEST_ARGUMENT_MAX_BYTES) return "One of the arguments is too long.";
    if (argument.includes("\u0000")) return "An argument contains a character that cannot be sent.";
  }
  return "";
}

// Shown back to the reviewer so the exact argument list is never a guess.
export function commandPreview(command: string[]): string {
  return command.map((part) => (/[\s"'\\]/.test(part) ? JSON.stringify(part) : part)).join(" ");
}

export function diffSummary(diff: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+++") || line.startsWith("---")) continue;
    if (line.startsWith("+")) added += 1;
    else if (line.startsWith("-")) removed += 1;
  }
  return { added, removed };
}

// The start response describes the conversation and run, not the workflow, so
// the new workflow is found by the conversation it was created for.
export function matchStartedWorkflow(
  items: GitHubWorkflow[],
  started: { workflowID?: string; conversationID?: string; runID?: string },
): GitHubWorkflow | null {
  if (started.workflowID) {
    const byID = items.find((item) => item.id === started.workflowID);
    if (byID) return byID;
  }
  if (started.runID) {
    const byRun = items.find((item) => item.task_run_id === started.runID);
    if (byRun) return byRun;
  }
  if (started.conversationID) {
    const byConversation = items.find((item) => item.conversation_id === started.conversationID);
    if (byConversation) return byConversation;
  }
  return null;
}

export function reviewReady(workflow: GitHubWorkflow): boolean {
  return Boolean(workflow.review && workflow.review.token && workflow.review.digest);
}

// A review is only worth asking for once the agent's task has finished, which
// is what the server checks before it looks at the checkout.
export function canReview(workflow: GitHubWorkflow, taskStatus: string): boolean {
  return reviewBlockedReason(workflow, taskStatus) === "";
}

export function publishDisabledReason(workflow: GitHubWorkflow): string {
  if (workflow.pull_request_url) return "A draft pull request is already open for this branch.";
  if (!reviewReady(workflow)) return "Build a review first. The pull request comes from exactly what you reviewed.";
  return "";
}

export function defaultPullRequestTitle(workflow: GitHubWorkflow): string {
  const title = workflow.issue_title.trim() || `Issue ${workflow.issue_number}`;
  return `${title} (#${workflow.issue_number})`.slice(0, 1024);
}

export function defaultPullRequestBody(workflow: GitHubWorkflow): string {
  const lines = [`Closes #${workflow.issue_number}.`, ""];
  if (workflow.issue_url) lines.push(workflow.issue_url, "");
  if (workflow.review?.test_command?.length) {
    lines.push(`Checked with: ${commandPreview(workflow.review.test_command)}`, "");
  }
  return lines.join("\n");
}

const WORKFLOW_ERROR_TEXT: [RegExp, string][] = [
  [
    /require an existing grant|GitHub access was revoked/i,
    "This repository and agent are not connected yet. Open Connected apps and grant them first.",
  ],
  [
    /GitHub account changed|account or grant changed/i,
    "The signed-in GitHub account is not the one this work started with. Reconnect GitHub, then build a new review.",
  ],
  [
    /worktree must be clean and all reviewed changes must be committed/i,
    "The agent left changes that are not committed. Ask it to commit everything on its branch, then review again.",
  ],
  [/workflow branch has no committed changes/i, "The agent has not committed anything yet, so there is nothing to review."],
  [/latest workflow task must finish successfully before review/i, "Wait for the agent's task to finish, then build the review."],
  [/workflow HEAD does not descend from the selected base/i, "The branch no longer builds on the base branch you chose. Start again from a fresh base."],
  [/review token or digest is stale|worktree changed|tasks changed/i, "The work changed after you reviewed it. Build a new review before publishing."],
  [/expected GitHub login does not match/i, "The GitHub account changed since this review. Build a new review before publishing."],
  [/dedicated branch push failed/i, "The branch could not reach GitHub. Check your connection and network access, then try again."],
  [/already has an unexpected pull request/i, "This branch already has a pull request that this workflow did not open. Handle it on GitHub."],
  [/local_repo_path/i, "That folder is not the repository root. Point at the top folder of the checkout."],
  [/local origin does not match/i, "That folder's origin is a different GitHub repository."],
  [/has no origin remote/i, "That folder has no origin remote, so there is nowhere to push."],
  [/selected local base does not resolve to a commit/i, "That branch does not exist in the folder you picked."],
  [/could not create the isolated Git worktree/i, "A separate checkout could not be made. Make sure the folder is a clean Git repository."],
  [/identifies a pull request/i, "That number belongs to a pull request, not an issue."],
  [/GitHub workflow not found/i, "This piece of work no longer exists."],
  [
    /configure a controller token/i,
    "Controller authentication is unavailable. Restart the desktop app; a development server needs a controller token.",
  ],
];

export function describeWorkflowError(status: number, message: string): string {
  for (const [pattern, text] of WORKFLOW_ERROR_TEXT) {
    if (pattern.test(message)) return text;
  }
  return fallbackErrorText(status, message);
}
