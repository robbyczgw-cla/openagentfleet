import assert from "node:assert/strict";
import test from "node:test";
import {
  attemptLabel,
  attemptsLeft,
  describeFollowupError,
  followupFingerprint,
  followupRequestBody,
  nextIdempotency,
  validateFollowupIntent,
} from "./taskFollowups.ts";

const input = {
  brief: "Summarise the invoices.",
  agent_id: "bot-a",
  attachments: [
    { id: "file-1", name: "jan.pdf", media_type: "application/pdf", size: 2048 },
    { id: "file-2", name: "feb.pdf", media_type: "application/pdf", size: 4096 },
  ],
};

function intent(overrides = {}) {
  return {
    kind: "retry",
    taskID: "run-1",
    agentID: "bot-a",
    brief: input.brief,
    attachmentIDs: ["file-1"],
    ...overrides,
  };
}

function counter() {
  let count = 0;
  return () => `key-${++count}`;
}

test("a retry sends no brief, a revision sends the edited one", () => {
  assert.deepEqual(followupRequestBody(intent()), { agent_id: "bot-a", attachment_ids: ["file-1"] });
  assert.deepEqual(followupRequestBody(intent({ kind: "revise", brief: "  Sort by date.  " })), {
    agent_id: "bot-a",
    attachment_ids: ["file-1"],
    brief: "Sort by date.",
  });
});

test("resending the same intent reuses the key so one request cannot become two runs", () => {
  const mint = counter();
  const first = nextIdempotency(null, intent(), mint);
  const second = nextIdempotency(first, intent(), mint);
  assert.equal(second.key, "key-1");
  assert.equal(second, first);
});

test("editing the request mints a new key", () => {
  const mint = counter();
  const first = nextIdempotency(null, intent({ kind: "revise", brief: "One" }), mint);
  const edited = nextIdempotency(first, intent({ kind: "revise", brief: "Two" }), mint);
  assert.equal(edited.key, "key-2");
  const files = nextIdempotency(edited, intent({ kind: "revise", brief: "Two", attachmentIDs: [] }), mint);
  assert.equal(files.key, "key-3");
});

test("a retry ignores brief edits when deciding whether the intent changed", () => {
  assert.equal(followupFingerprint(intent({ brief: "anything" })), followupFingerprint(intent()));
  assert.notEqual(
    followupFingerprint(intent({ kind: "revise", brief: "a" })),
    followupFingerprint(intent({ kind: "revise", brief: "b" })),
  );
});

test("attachment order is part of the intent because the server hashes the list", () => {
  assert.notEqual(
    followupFingerprint(intent({ attachmentIDs: ["file-1", "file-2"] })),
    followupFingerprint(intent({ attachmentIDs: ["file-2", "file-1"] })),
  );
});

test("a follow-up must stay on the original agent and its own files", () => {
  assert.equal(validateFollowupIntent(intent(), input), "");
  assert.match(validateFollowupIntent(intent({ agentID: "bot-b" }), input), /same agent/i);
  assert.match(validateFollowupIntent(intent({ attachmentIDs: ["file-9"] }), input), /original request/i);
  assert.match(validateFollowupIntent(intent({ attachmentIDs: ["file-1", "file-1"] }), input), /only be sent once/i);
  assert.match(validateFollowupIntent(intent({ kind: "revise", brief: "   " }), input), /differently/i);
});

test("attempt wording tells the original from a rerun", () => {
  assert.equal(attemptLabel(1, ""), "Attempt 1 of 10 · original request");
  assert.equal(attemptLabel(2, "retry"), "Attempt 2 of 10 · same request, run again");
  assert.equal(attemptLabel(3, "revise"), "Attempt 3 of 10 · reworded request");
  assert.equal(attemptsLeft(10), 0);
  assert.equal(attemptsLeft(1), 9);
});

test("server conflicts are turned into next steps", () => {
  assert.match(describeFollowupError(409, "task attempt limit reached"), /Start a new task/);
  assert.match(describeFollowupError(409, "task status does not allow this followup"), /moved on/);
  assert.match(
    describeFollowupError(409, "task followup idempotency key conflicts with an earlier request"),
    /already sent/,
  );
  assert.equal(describeFollowupError(400, "content is required"), "content is required");
});
