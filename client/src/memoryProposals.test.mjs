import assert from "node:assert/strict";
import test from "node:test";
import {
  describeProposalError,
  draftFromProposal,
  expiryFromInput,
  expiryToInput,
  proposalDraftChanged,
  proposalPatch,
  validateProposalDraft,
} from "./memoryProposals.ts";

function proposal(overrides = {}) {
  return {
    id: "mp-1",
    bot_id: "bot-a",
    source_run_id: "run-1",
    source_message_id: "msg-1",
    category: "fact",
    status: "pending",
    content: "The invoice folder is /finance/2026.",
    priority: 3,
    created_at: "2026-02-01T10:00:00Z",
    updated_at: "2026-02-01T10:00:00Z",
    ...overrides,
  };
}

test("an untouched suggestion produces no patch", () => {
  const item = proposal({ expires_at: "2026-03-01T00:00:00Z" });
  assert.equal(proposalPatch(draftFromProposal(item), item), null);
  assert.equal(proposalDraftChanged(draftFromProposal(item), item), false);
});

test("only edited fields are sent", () => {
  const item = proposal();
  const draft = { ...draftFromProposal(item), content: "The invoice folder is /finance/2027." };
  assert.deepEqual(proposalPatch(draft, item), { content: "The invoice folder is /finance/2027." });
});

test("clearing an expiry sends null and setting one sends a timestamp", () => {
  const withExpiry = proposal({ expires_at: "2026-03-01T00:00:00Z" });
  assert.deepEqual(proposalPatch({ ...draftFromProposal(withExpiry), expiry: "" }, withExpiry), {
    expires_at: null,
  });
  const withoutExpiry = proposal();
  assert.deepEqual(proposalPatch({ ...draftFromProposal(withoutExpiry), expiry: "2026-04-05" }, withoutExpiry), {
    expires_at: "2026-04-05T00:00:00Z",
  });
});

test("an expiry survives the round trip through the date field", () => {
  assert.equal(expiryToInput("2026-03-01T00:00:00Z"), "2026-03-01");
  assert.equal(expiryToInput(undefined), "");
  assert.equal(expiryFromInput("2026-03-01"), "2026-03-01T00:00:00Z");
  assert.equal(expiryFromInput("  "), "");
  assert.equal(expiryFromInput("next tuesday"), "");
});

test("a same-day expiry edit is not treated as a change", () => {
  const item = proposal({ expires_at: "2026-03-01T18:30:00Z" });
  assert.equal(proposalPatch({ ...draftFromProposal(item), expiry: "2026-03-01" }, item), null);
});

test("wording and importance are checked before the request goes out", () => {
  const draft = draftFromProposal(proposal());
  assert.equal(validateProposalDraft(draft), "");
  assert.match(validateProposalDraft({ ...draft, content: "  " }), /remember/i);
  assert.match(validateProposalDraft({ ...draft, priority: 9 }), /importance/i);
  assert.match(validateProposalDraft({ ...draft, content: "x".repeat(4097) }), /too long/i);
  assert.match(validateProposalDraft({ ...draft, expiry: "soon" }), /date/i);
});

test("server refusals become something a reviewer can act on", () => {
  assert.match(describeProposalError(400, "memory content appears to contain a secret"), /password or key/);
  assert.match(describeProposalError(409, "memory proposal is already resolved"), /already accepted or dismissed/);
  assert.equal(describeProposalError(400, "unexpected field"), "unexpected field");
});
