import assert from "node:assert/strict";
import test from "node:test";
import { NETWORK_FAILURE_TEXT, fallbackErrorText, isNetworkFailure } from "./apiErrors.ts";
import { describeFollowupError } from "./taskFollowups.ts";
import { describeProjectError } from "./projects.ts";
import { describeProposalError } from "./memoryProposalModel.ts";
import { describeWorkflowError } from "./githubWorkflowModel.ts";

test("the browser's own wording for an unreachable service is replaced", () => {
  // WebKit says the first, Chromium the second, Firefox the third.
  for (const message of ["Load failed", "Failed to fetch", "NetworkError when attempting to fetch resource."]) {
    assert.equal(isNetworkFailure(message), true, message);
    assert.equal(fallbackErrorText(0, message), NETWORK_FAILURE_TEXT);
  }
});

test("a blocked preflight reaches every surface as the same sentence", () => {
  // A request header the server does not allow fails before it is sent, and
  // looks exactly like the service being down.
  assert.equal(describeFollowupError(0, "Load failed"), NETWORK_FAILURE_TEXT);
  assert.equal(describeProjectError(0, "Failed to fetch"), NETWORK_FAILURE_TEXT);
  assert.equal(describeProposalError(0, "Load failed"), NETWORK_FAILURE_TEXT);
  assert.equal(describeWorkflowError(0, "Failed to fetch"), NETWORK_FAILURE_TEXT);
});

test("a real server message still wins over the generic sentence", () => {
  assert.equal(fallbackErrorText(400, "brief is required"), "brief is required");
  assert.equal(describeFollowupError(400, "agent_id is required"), "agent_id is required");
  assert.match(describeFollowupError(409, "task attempt limit reached"), /Start a new task/);
});

test("an empty message falls back by status", () => {
  assert.equal(fallbackErrorText(0, ""), NETWORK_FAILURE_TEXT);
  assert.equal(fallbackErrorText(500, ""), "The request failed (500).");
  assert.equal(fallbackErrorText(500, "   "), "The request failed (500).");
});

test("ordinary words that merely mention loading are left alone", () => {
  assert.equal(isNetworkFailure("Loading the diff failed for this file"), false);
  assert.equal(fallbackErrorText(409, "workflow diff is unavailable or empty"), "workflow diff is unavailable or empty");
});
