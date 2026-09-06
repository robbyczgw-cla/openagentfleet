import assert from "node:assert/strict";
import test from "node:test";
import {
  baseRefProblem,
  canReview,
  commandPreview,
  defaultPullRequestTitle,
  describeWorkflowError,
  diffSummary,
  matchStartedWorkflow,
  parseTestCommand,
  pathProblem,
  publishDisabledReason,
  reviewBlockedReason,
  taskOutcome,
  taskStatusText,
  workflowStateDetail,
  workflowStateKey,
  workflowStateLabel,
  startRequestBody,
  validateStartDraft,
  validateTestCommand,
} from "./githubWorkflowModel.ts";

function workflow(overrides = {}) {
  return {
    id: "ghwf-1",
    status: "running",
    repository: "acme/widgets",
    issue_number: 214,
    issue_title: "Crash on empty upload",
    issue_url: "https://github.com/acme/widgets/issues/214",
    agent_id: "bot-a",
    conversation_id: "conv-1",
    task_run_id: "run-1",
    local_repo_path: "/home/you/widgets",
    worktree_path: "/home/you/.openagentfleet-worktrees/ghwf-1",
    base_ref: "main",
    base_commit: "a".repeat(40),
    branch: "oaf/issue-214-1",
    expected_login: "you",
    pushed: false,
    created_at: "2026-05-01T10:00:00Z",
    updated_at: "2026-05-01T10:00:00Z",
    ...overrides,
  };
}

const draft = {
  repository: "acme/widgets",
  issueNumber: "214",
  agentID: "bot-a",
  localRepoPath: "/home/you/widgets",
  baseRef: "main",
};

test("each argument line stays one argument, with no shell splitting", () => {
  assert.deepEqual(parseTestCommand(" go ", "test\n./...\n"), ["go", "test", "./..."]);
  assert.deepEqual(parseTestCommand("pnpm", "run\ntest -- --watch=false"), [
    "pnpm",
    "run",
    "test -- --watch=false",
  ]);
  assert.deepEqual(parseTestCommand("", "test"), ["test"]);
  assert.deepEqual(parseTestCommand("  ", "  \n  "), []);
});

test("the command is checked against the same limits the server applies", () => {
  assert.equal(validateTestCommand(["go", "test", "./..."]), "");
  assert.match(validateTestCommand([]), /Name the program/);
  assert.match(validateTestCommand(Array(33).fill("x")), /at most 31 arguments/);
  assert.match(validateTestCommand(["go", "x".repeat(4097)]), /too long/);
  assert.match(validateTestCommand(["go", "a\u0000b"]), /cannot be sent/);
});

test("the preview shows the argument list without inventing a shell", () => {
  assert.equal(commandPreview(["go", "test", "./..."]), "go test ./...");
  assert.equal(commandPreview(["go", "test -run X"]), 'go "test -run X"');
});

test("a start draft is checked before it reaches the server", () => {
  assert.equal(validateStartDraft(draft), "");
  assert.match(validateStartDraft({ ...draft, repository: "widgets" }), /repositories you have already connected/i);
  assert.match(validateStartDraft({ ...draft, issueNumber: "0" }), /issue number/i);
  assert.match(validateStartDraft({ ...draft, issueNumber: "12.5" }), /issue number/i);
  assert.match(validateStartDraft({ ...draft, agentID: " " }), /agent/i);
});

test("only a tidy absolute path is accepted, matching the server", () => {
  assert.equal(pathProblem("/home/you/widgets"), "");
  assert.equal(pathProblem("C:\\code\\widgets"), "");
  assert.match(pathProblem("widgets"), /full path/i);
  assert.match(pathProblem("/home/you/../widgets"), /without \. or \.\./);
  assert.match(pathProblem("/home/you/widgets/"), /trailing slash/i);
  assert.match(pathProblem("/home//you/widgets"), /repeated slashes/i);
  assert.match(pathProblem("   "), /repository folder/i);
});

test("branch names that git would reject are caught early", () => {
  assert.equal(baseRefProblem("main"), "");
  assert.equal(baseRefProblem("release/2026.05"), "");
  assert.match(baseRefProblem("main..old"), /usable branch name/);
  assert.match(baseRefProblem("main@{1}"), /usable branch name/);
  assert.match(baseRefProblem("feature/"), /usable branch name/);
  assert.match(baseRefProblem("-start"), /usable branch name/);
  assert.match(baseRefProblem(""), /branch this work starts from/);
});

test("the start body is normalised the way the server expects it", () => {
  assert.deepEqual(startRequestBody({ ...draft, repository: " ACME/Widgets ", issueNumber: " 214 " }), {
    local_repo_path: "/home/you/widgets",
    repository: "acme/widgets",
    issue_number: 214,
    agent_id: "bot-a",
    base_ref: "main",
  });
});

test("the new piece of work is found by header, run or conversation", () => {
  const items = [workflow({ id: "ghwf-1" }), workflow({ id: "ghwf-2", conversation_id: "conv-2", task_run_id: "run-2" })];
  assert.equal(matchStartedWorkflow(items, { workflowID: "ghwf-2" })?.id, "ghwf-2");
  assert.equal(matchStartedWorkflow(items, { runID: "run-2" })?.id, "ghwf-2");
  assert.equal(matchStartedWorkflow(items, { conversationID: "conv-1" })?.id, "ghwf-1");
  assert.equal(matchStartedWorkflow(items, { workflowID: "gone", conversationID: "conv-2" })?.id, "ghwf-2");
  assert.equal(matchStartedWorkflow(items, {}), null);
});

test("diff counts ignore the file headers", () => {
  const diff = [
    "diff --git a/main.go b/main.go",
    "--- a/main.go",
    "+++ b/main.go",
    "@@ -1,3 +1,4 @@",
    "+added one",
    "+added two",
    "-removed one",
    " unchanged",
  ].join("\n");
  assert.deepEqual(diffSummary(diff), { added: 2, removed: 1 });
});

test("a task that ended badly is described as such instead of still running", () => {
  // The server never moves the row off "running" on its own, so the panel has
  // to read the agent's task to avoid promising progress that will not come.
  const stuck = workflow({ status: "running" });
  assert.equal(workflowStateLabel(stuck, "failed"), "Task did not finish");
  assert.equal(workflowStateKey(stuck, "failed"), "task_unsuccessful");
  assert.match(workflowStateDetail(stuck, "failed"), /ended as failed and stopped there/);
  // Every claim names OpenAgentFleet rather than the agent's own shell.
  assert.match(workflowStateDetail(stuck, "failed"), /OpenAgentFleet has not pushed anything/);
  assert.match(workflowStateDetail(stuck, "stopped"), /ended as stopped/);
  assert.match(workflowStateDetail(stuck, "blocked"), /ended as blocked/);
  // Still running is still running.
  assert.equal(workflowStateLabel(stuck, "running"), "With the agent");
  // A finished task reads as ready even though the row still says running.
  assert.equal(workflowStateLabel(stuck, "completed"), "Ready to review");
  // Once a review or publish happened, the stored status wins.
  assert.equal(workflowStateLabel(workflow({ status: "draft_pr" }), "failed"), "Draft pull request open");
});

test("a blocked review names the next step instead of asking the user to wait forever", () => {
  const stuck = workflow({ status: "running" });
  for (const ended of ["failed", "stopped", "blocked"]) {
    const reason = reviewBlockedReason(stuck, ended);
    assert.match(reason, /finished successfully/);
    assert.match(reason, new RegExp(`ended as ${ended}`));
    assert.match(reason, /conversation/);
    assert.match(reason, /Tasks & results/);
    assert.equal(canReview(stuck, ended), false);
  }
  assert.match(reviewBlockedReason(stuck, "running"), /Wait for the agent's task to finish/);
  assert.equal(reviewBlockedReason(stuck, "completed"), "");
  assert.equal(reviewBlockedReason(workflow({ task_run_id: undefined }), "completed"), "The agent has not started its task yet.");
});

test("task outcomes are grouped the way the server groups them", () => {
  assert.equal(taskOutcome(""), "unknown");
  assert.equal(taskOutcome("completed"), "completed");
  assert.equal(taskOutcome("queued"), "waiting");
  assert.equal(taskOutcome("running"), "waiting");
  assert.equal(taskOutcome("waiting_approval"), "waiting");
  for (const ended of ["failed", "stopped", "blocked"]) assert.equal(taskOutcome(ended), "unsuccessful");
  assert.equal(taskStatusText("waiting_approval"), "waiting approval");
});

test("review is offered only once the agent's task has finished", () => {
  assert.equal(canReview(workflow(), "completed"), true);
  assert.equal(canReview(workflow(), "running"), false);
  assert.equal(canReview(workflow({ task_run_id: undefined }), "completed"), false);
  assert.equal(canReview(workflow({ status: "draft_pr" }), "completed"), false);
});

test("publishing is blocked until there is a review, and once a draft exists", () => {
  assert.match(publishDisabledReason(workflow()), /Build a review first/);
  const reviewed = workflow({ review: { token: "t", digest: "d", changed_files: [], diff: "", test_command: [], test_output: "", base_commit: "", head_commit: "", run_state_digest: "", created_at: "" } });
  assert.equal(publishDisabledReason(reviewed), "");
  assert.match(
    publishDisabledReason({ ...reviewed, pull_request_url: "https://github.com/acme/widgets/pull/9" }),
    /already open/,
  );
});

test("the default pull request title names the issue", () => {
  assert.equal(defaultPullRequestTitle(workflow()), "Crash on empty upload (#214)");
  assert.equal(defaultPullRequestTitle(workflow({ issue_title: "  " })), "Issue 214 (#214)");
});

test("server refusals become next steps for the reviewer", () => {
  assert.match(
    describeWorkflowError(409, "worktree must be clean and all reviewed changes must be committed"),
    /commit everything/,
  );
  assert.match(describeWorkflowError(409, "review token or digest is stale"), /new review before publishing/);
  assert.match(describeWorkflowError(502, "dedicated branch push failed"), /could not reach GitHub/);
  assert.equal(describeWorkflowError(400, "issue_number must be positive"), "issue_number must be positive");
});
