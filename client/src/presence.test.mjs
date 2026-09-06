import assert from "node:assert/strict";
import test from "node:test";
import { deriveAgentPresence } from "./presence.ts";

test("queued turns explain waiting even while the shared computer is active", () => {
  const presence = deriveAgentPresence({
    botID: "agent",
    run: { status: "queued" },
    computer: { running: true, takeover: false, agent_control: true },
    handoffs: [{ source_bot_id: "other", target_bot_id: "agent", status: "queued" }],
  });
  assert.equal(presence.label, "Queued");
  assert.equal(presence.detail, "Waiting to start. This Agent runs one task at a time.");
});

test("running turns retain computer and approval states", () => {
  assert.equal(deriveAgentPresence({
    botID: "agent", run: { status: "running" },
    computer: { running: true, takeover: false, agent_control: true },
  }).label, "Using computer");
  assert.equal(deriveAgentPresence({ botID: "agent", run: { status: "waiting_for_approval" } }).label, "Needs approval");
});
