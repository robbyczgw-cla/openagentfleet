import assert from "node:assert/strict";
import test from "node:test";
import {
  assignableProjects,
  describeProjectError,
  draftFromProject,
  isProjectVersionConflict,
  projectDraftChanged,
  removedMembers,
  toggleMember,
  validateProjectDraft,
} from "./projects.ts";

function project(overrides = {}) {
  return {
    id: "prj-1",
    name: "Launch",
    brief: "Ship the beta.",
    status: "active",
    version: 3,
    brief_revision: 2,
    members: [{ agent_id: "bot-a", name: "Ada", title: "" }],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}

test("a draft needs a name and at least one agent", () => {
  assert.match(validateProjectDraft({ name: "  ", brief: "", agentIDs: ["bot-a"] }), /name/i);
  assert.match(validateProjectDraft({ name: "Launch", brief: "", agentIDs: [] }), /at least one agent/i);
  assert.equal(validateProjectDraft({ name: "Launch", brief: "", agentIDs: ["bot-a"] }), "");
});

test("name length is counted in bytes, matching the server", () => {
  const wide = "é".repeat(81); // 162 bytes, 81 characters
  assert.match(validateProjectDraft({ name: wide, brief: "", agentIDs: ["bot-a"] }), /shorten the name/i);
  assert.equal(validateProjectDraft({ name: "é".repeat(80), brief: "", agentIDs: ["bot-a"] }), "");
});

test("a duplicated agent is rejected before the request goes out", () => {
  assert.match(
    validateProjectDraft({ name: "Launch", brief: "", agentIDs: ["bot-a", "bot-a"] }),
    /only be on the project once/i,
  );
});

test("member order does not count as an edit", () => {
  const saved = project({ members: [{ agent_id: "bot-a", name: "Ada", title: "" }, { agent_id: "bot-b", name: "Bo", title: "" }] });
  const draft = { name: " Launch ", brief: "Ship the beta.", agentIDs: ["bot-b", "bot-a"] };
  assert.equal(projectDraftChanged(draft, saved), false);
  assert.equal(projectDraftChanged({ ...draft, brief: "Ship it." }, saved), true);
  assert.equal(projectDraftChanged({ ...draft, agentIDs: ["bot-a"] }, saved), true);
});

test("removed members are named so the warning can list them", () => {
  const saved = project({ members: [{ agent_id: "bot-a", name: "Ada", title: "" }, { agent_id: "bot-b", name: "Bo", title: "" }] });
  const losing = removedMembers({ ...draftFromProject(saved), agentIDs: ["bot-a"] }, saved);
  assert.deepEqual(losing.map((member) => member.name), ["Bo"]);
});

test("toggling a member adds once and removes cleanly", () => {
  assert.deepEqual(toggleMember(["bot-a"], "bot-b", true), ["bot-a", "bot-b"]);
  assert.deepEqual(toggleMember(["bot-a", "bot-b"], "bot-b", true), ["bot-a", "bot-b"]);
  assert.deepEqual(toggleMember(["bot-a", "bot-b"], "bot-a", false), ["bot-b"]);
});

test("only active projects the agent belongs to can take new work", () => {
  const list = [
    project({ id: "prj-1" }),
    project({ id: "prj-2", status: "archived" }),
    project({ id: "prj-3", members: [{ agent_id: "bot-z", name: "Zed", title: "" }] }),
  ];
  assert.deepEqual(assignableProjects(list, "bot-a").map((item) => item.id), ["prj-1"]);
  assert.deepEqual(assignableProjects(list, "").map((item) => item.id), ["prj-1", "prj-3"]);
});

test("a version conflict is recognised and explained", () => {
  assert.equal(isProjectVersionConflict(409, "project version conflict"), true);
  assert.equal(isProjectVersionConflict(409, "project is archived"), false);
  assert.equal(isProjectVersionConflict(400, "project version conflict"), false);
  assert.match(describeProjectError(409, "project version conflict"), /Load the latest copy/);
  assert.match(describeProjectError(409, "agent is not a project member"), /Add the agent to the project/);
});

test("an unrecognised server message is passed through rather than swallowed", () => {
  assert.equal(describeProjectError(400, "brief must be valid text"), "brief must be valid text");
  assert.match(describeProjectError(500, ""), /failed \(500\)/);
});
