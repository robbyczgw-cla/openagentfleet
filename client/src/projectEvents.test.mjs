import assert from "node:assert/strict";
import test from "node:test";
import {
  notifyProjectsChanged,
  projectsRevision,
  resetProjectEvents,
  subscribeProjects,
} from "./projectEvents.ts";

test("every mounted listener hears one write", () => {
  resetProjectEvents();
  const heard = [];
  subscribeProjects(() => heard.push("conversation selector"));
  subscribeProjects(() => heard.push("routine selector"));
  notifyProjectsChanged();
  assert.deepEqual(heard, ["conversation selector", "routine selector"]);
});

test("the revision only moves on a write, so nothing reloads on its own", () => {
  resetProjectEvents();
  assert.equal(projectsRevision(), 0);
  subscribeProjects(() => {});
  assert.equal(projectsRevision(), 0);
  notifyProjectsChanged();
  notifyProjectsChanged();
  assert.equal(projectsRevision(), 2);
});

test("the revision read during a notification is already the new one", () => {
  resetProjectEvents();
  let seen = -1;
  subscribeProjects(() => {
    seen = projectsRevision();
  });
  notifyProjectsChanged();
  assert.equal(seen, 1);
});

test("unsubscribing stops the reloads", () => {
  resetProjectEvents();
  let count = 0;
  const stop = subscribeProjects(() => {
    count += 1;
  });
  notifyProjectsChanged();
  stop();
  notifyProjectsChanged();
  assert.equal(count, 1);
});

test("a listener that unsubscribes during a notification does not break the rest", () => {
  resetProjectEvents();
  const heard = [];
  const stop = subscribeProjects(() => {
    heard.push("first");
    stop();
  });
  subscribeProjects(() => heard.push("second"));
  notifyProjectsChanged();
  assert.deepEqual(heard, ["first", "second"]);
  notifyProjectsChanged();
  assert.deepEqual(heard, ["first", "second", "second"]);
});
