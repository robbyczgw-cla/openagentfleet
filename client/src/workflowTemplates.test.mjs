import assert from "node:assert/strict";
import test from "node:test";
import {
  STARTER_TEMPLATES,
  cronToSchedule,
  describeSchedule,
  exportWorkflowMarkdown,
  importWorkflowMarkdown,
  isValidCronExpression,
  isValidTimeZone,
  normalizeCronExpression,
  scheduleToCron,
  selectExportableRoutines,
  validateTemplate,
} from "./workflowTemplates.ts";

test("cron validation mirrors the five-field rules the server enforces", () => {
  for (const valid of [
    "0 9 * * *",
    "30 6 * * 1-5",
    "0 0 1 1 0",
    "*/15 * * * *",
    "0 9 * * 7",
    "0,30 8-17 * * 1,3,5",
  ]) {
    assert.equal(isValidCronExpression(valid), true, valid);
  }
  for (const invalid of [
    "",
    "0 9 * *",
    "0 9 * * * *",
    "60 9 * * *",
    "0 24 * * *",
    "0 9 0 * *",
    "0 9 * 13 *",
    "0 9 * * 8",
    "0 9 * * 5-1",
    "*/0 9 * * *",
    "a 9 * * *",
    "0 9 * * ,",
  ]) {
    assert.equal(isValidCronExpression(invalid), false, JSON.stringify(invalid));
  }
  assert.equal(normalizeCronExpression("  30   6 *  * 1-5 "), "30 6 * * 1-5");
});

test("the easy picker round-trips through cron without losing the schedule", () => {
  const cases = [
    [{ kind: "daily", time: "09:00" }, "0 9 * * *"],
    [{ kind: "weekdays", time: "06:30" }, "30 6 * * 1-5"],
    [{ kind: "weekly", time: "17:05", weekday: 3 }, "5 17 * * 3"],
    [{ kind: "weekly", time: "00:00", weekday: 0 }, "0 0 * * 0"],
  ];
  for (const [schedule, cron] of cases) {
    assert.equal(scheduleToCron(schedule), cron);
    assert.deepEqual(cronToSchedule(cron), schedule);
  }
  // Cron treats 7 as Sunday; the picker only ever shows one Sunday.
  assert.deepEqual(cronToSchedule("0 0 * * 7"), { kind: "weekly", time: "00:00", weekday: 0 });
  assert.equal(scheduleToCron({ kind: "daily", time: "25:00" }), null);
  assert.equal(scheduleToCron({ kind: "weekly", time: "09:00", weekday: 9 }), null);
});

test("expressions the picker cannot show fall through to the advanced field", () => {
  // Returning a rounded-off schedule here would silently change when a routine
  // runs, so anything outside the picker's shapes must return null.
  for (const advanced of ["*/15 * * * *", "0 9 1 * *", "0 9 * 6 *", "0 9,17 * * *", "0 9 * * 1-3"]) {
    assert.equal(cronToSchedule(advanced), null, advanced);
    assert.equal(describeSchedule(advanced), `Custom schedule (${advanced})`);
  }
  assert.equal(describeSchedule("0 9 * * *"), "Every day at 09:00");
  assert.equal(describeSchedule("30 6 * * 1-5"), "Every weekday at 06:30");
  assert.equal(describeSchedule("0 17 * * 5"), "Every Friday at 17:00");
  assert.equal(describeSchedule("nope"), "Not a valid schedule");
});

test("time zones are checked against the runtime database", () => {
  assert.equal(isValidTimeZone("Europe/Vienna"), true);
  assert.equal(isValidTimeZone("UTC"), true);
  assert.equal(isValidTimeZone("Mars/Olympus"), false);
  assert.equal(isValidTimeZone(""), false);
  assert.equal(isValidTimeZone("   "), false);
});

test("starter templates are valid and survive an export/import round trip", () => {
  for (const template of STARTER_TEMPLATES) {
    assert.deepEqual(validateTemplate(template), [], template.name);
  }
  const markdown = exportWorkflowMarkdown(STARTER_TEMPLATES);
  const imported = importWorkflowMarkdown(markdown);
  assert.deepEqual(imported.errors, []);
  assert.deepEqual(imported.warnings, []);
  assert.deepEqual(imported.templates, STARTER_TEMPLATES);
});

test("exported Markdown stays readable rather than encoded", () => {
  const markdown = exportWorkflowMarkdown([
    {
      name: "Morning standup notes",
      instructions: "Summarize yesterday.\n\nKeep it short.",
      cron: "30 6 * * 1-5",
      timeZone: "Europe/Vienna",
    },
  ]);
  assert.match(markdown, /^# OpenAgentFleet workflows\n/);
  assert.match(markdown, /## Morning standup notes/);
  assert.match(markdown, /- Schedule: Every weekday at 06:30/);
  assert.match(markdown, /- Cron: `30 6 \* \* 1-5`/);
  assert.match(markdown, /- Time zone: Europe\/Vienna/);
  assert.match(markdown, /### Instructions\n\nSummarize yesterday\.\n\nKeep it short\./);
  assert.equal(markdown.endsWith("\n"), true);
});

test("import never carries an owner, an enabled flag or anything private", () => {
  const imported = importWorkflowMarkdown(
    [
      "# OpenAgentFleet workflows",
      "",
      "## Nightly triage",
      "",
      "- Schedule: Every day at 22:00",
      "- Cron: `0 22 * * *`",
      "- Time zone: UTC",
      "- Agent: bot-1234",
      "- Bot id: bot-1234",
      "- Status: enabled",
      "- Webhook secret: hunter2",
      "- Last run: 2026-09-05T22:00:00Z",
      "",
      "### Instructions",
      "",
      "Read the most recent test run.",
      "",
    ].join("\n"),
  );
  assert.deepEqual(imported.errors, []);
  assert.equal(imported.templates.length, 1);
  // Only these four fields exist on an imported template. An owner or an
  // enabled bit could only arrive by being added here.
  assert.deepEqual(Object.keys(imported.templates[0]).sort(), [
    "cron",
    "instructions",
    "name",
    "timeZone",
  ]);
  assert.deepEqual(imported.templates[0], {
    name: "Nightly triage",
    instructions: "Read the most recent test run.",
    cron: "0 22 * * *",
    timeZone: "UTC",
  });
  const serialized = JSON.stringify(imported.templates[0]);
  for (const leaked of ["bot-1234", "hunter2", "enabled", "2026-09-05"]) {
    assert.equal(serialized.includes(leaked), false, leaked);
  }
  // The user is told what was dropped instead of it disappearing silently.
  assert.deepEqual(imported.warnings, [
    "Nightly triage: ignored agent, bot id, last run, status, webhook secret",
  ]);
});

test("instructions containing Markdown survive an exact round trip", () => {
  // Agent instructions routinely contain headings and shell blocks. Read as
  // section boundaries these truncate the body and invent phantom workflows,
  // so the export has to make the boundary explicit.
  const bodies = [
    "Group them:\n\n## Fixes\n\n- one\n- two\n\nThen post it.",
    "# Top level heading\n\nStill the same instruction.",
    "Run this:\n\n```sh\n# build it, this comment is not a heading\nmake test\n```\n\nReport failures.",
    "A nested fence:\n\n````md\n```sh\necho hi\n```\n````\n\n## Wrap up\n\nDone.",
    "~~~text\n## not a heading\n~~~",
    "    # an indented code block\n\nplain tail",
    "No Markdown at all, just a sentence.",
    "Line one\n\n\nLine four after two blank lines.",
  ];
  for (const instructions of bodies) {
    const original = [
      { name: "Release notes", instructions, cron: "0 9 * * 1", timeZone: "Europe/Vienna" },
      { name: "Second workflow", instructions: "Unrelated.", cron: "0 10 * * *", timeZone: "UTC" },
    ];
    const roundTripped = importWorkflowMarkdown(exportWorkflowMarkdown(original));
    assert.deepEqual(roundTripped.errors, [], JSON.stringify(instructions));
    assert.deepEqual(roundTripped.warnings, []);
    // The heading inside the body must not become a third workflow.
    assert.equal(roundTripped.templates.length, 2, JSON.stringify(instructions));
    assert.deepEqual(roundTripped.templates, original, JSON.stringify(instructions));
  }
});

test("a bare instruction body stays unfenced so the file reads normally", () => {
  const plain = exportWorkflowMarkdown([
    { name: "Simple", instructions: "Just do the thing.", cron: "0 9 * * *", timeZone: "UTC" },
  ]);
  assert.match(plain, /### Instructions\n\nJust do the thing\./);
  assert.equal(plain.includes("```"), false);

  const risky = exportWorkflowMarkdown([
    { name: "Risky", instructions: "## Heading\n\nBody.", cron: "0 9 * * *", timeZone: "UTC" },
  ]);
  assert.match(risky, /### Instructions\n\n```\n## Heading\n\nBody\.\n```/);

  // The fence has to outgrow the longest backtick run inside the body.
  const nested = exportWorkflowMarkdown([
    { name: "Nested", instructions: "# x\n\n````\ninner\n````", cron: "0 9 * * *", timeZone: "UTC" },
  ]);
  assert.match(nested, /### Instructions\n\n`````\n# x\n\n````\ninner\n````\n`````/);
});

test("a workflow named Instructions is its own workflow, not a body marker", () => {
  const original = [
    { name: "Daily report", instructions: "Write the report.", cron: "0 9 * * *", timeZone: "UTC" },
    { name: "Instructions", instructions: "Restate the house style.", cron: "0 10 * * 1", timeZone: "UTC" },
    { name: "Wrap up", instructions: "Close the loop.", cron: "0 17 * * 5", timeZone: "UTC" },
  ];
  const roundTripped = importWorkflowMarkdown(exportWorkflowMarkdown(original));
  assert.deepEqual(roundTripped.errors, []);
  // Previously the "Instructions" heading was read as the body marker of the
  // workflow above it, which merged the two and lost one workflow entirely.
  assert.equal(roundTripped.templates.length, 3);
  assert.deepEqual(roundTripped.templates, original);
  assert.deepEqual(
    roundTripped.templates.map((t) => t.name),
    ["Daily report", "Instructions", "Wrap up"],
  );
});

test("workflows may share a name and both survive the round trip", () => {
  // The store allows the same name for two agents or two schedules, so the file
  // format has to carry both rather than dropping the second.
  const original = [
    { name: "Nightly triage", instructions: "Triage the web build.", cron: "0 22 * * *", timeZone: "UTC" },
    { name: "Nightly triage", instructions: "Triage the mobile build.", cron: "0 23 * * *", timeZone: "Europe/Vienna" },
  ];
  const roundTripped = importWorkflowMarkdown(exportWorkflowMarkdown(original));
  assert.deepEqual(roundTripped.errors, []);
  assert.equal(roundTripped.templates.length, 2);
  assert.deepEqual(roundTripped.templates, original);
  assert.notEqual(roundTripped.templates[0].cron, roundTripped.templates[1].cron);
});

test("only cron routines are exportable and the rest are named", () => {
  const selection = selectExportableRoutines([
    { name: "Morning notes", kind: "cron", description: "Summarize.", cron_expression: "30 6 * * 1-5", time_zone: "UTC" },
    { name: "Watch the queue", kind: "heartbeat", description: "Poll.", time_zone: "UTC" },
    { name: "Broken", kind: "cron", cron_expression: "0 99 * * *", time_zone: "UTC" },
  ]);
  assert.deepEqual(selection.templates, [
    { name: "Morning notes", instructions: "Summarize.", cron: "30 6 * * 1-5", timeZone: "UTC" },
  ]);
  // A heartbeat cadence has no cron field, so exporting it would emit an empty
  // expression that fails to import.
  assert.deepEqual(selection.unsupported, ["Watch the queue", "Broken"]);
  assert.deepEqual(validateTemplate(selection.templates[0]), []);
  const roundTripped = importWorkflowMarkdown(exportWorkflowMarkdown(selection.templates));
  assert.deepEqual(roundTripped.errors, []);
  assert.deepEqual(roundTripped.templates, selection.templates);
});

test("import reports bad workflows per name and keeps the good ones", () => {
  const imported = importWorkflowMarkdown(
    [
      "# Workflows",
      "",
      "## Good one",
      "",
      "- Cron: `0 9 * * *`",
      "- Time zone: UTC",
      "",
      "### Instructions",
      "",
      "Do the thing.",
      "",
      "## Broken cron",
      "",
      "- Cron: `0 99 * * *`",
      "- Time zone: UTC",
      "",
      "### Instructions",
      "",
      "Never runs.",
      "",
      "## Broken zone",
      "",
      "- Cron: `0 9 * * *`",
      "- Time zone: Mars/Olympus",
      "",
      "### Instructions",
      "",
      "Nowhere.",
      "",
      "## Good one",
      "",
      "- Cron: `0 10 * * *`",
      "- Time zone: UTC",
      "",
      "### Instructions",
      "",
      "Same name, different schedule.",
      "",
    ].join("\n"),
  );
  assert.equal(imported.templates.length, 2);
  assert.deepEqual(
    imported.templates.map((t) => t.cron),
    ["0 9 * * *", "0 10 * * *"],
  );
  assert.deepEqual(imported.errors, [
    "Broken cron: schedule must be five valid cron fields",
    "Broken zone: time zone is not recognized",
  ]);
});

test("import rejects a file with no workflows in it", () => {
  const empty = importWorkflowMarkdown("# Just a document\n\nNothing here.\n");
  assert.deepEqual(empty.templates, []);
  assert.deepEqual(empty.errors, [
    "No workflows found. Each workflow needs a '## Name' heading.",
  ]);
});

test("instruction bodies keep their own structure but lose control characters", () => {
  const imported = importWorkflowMarkdown(
    [
      "## Release notes",
      "",
      "- Cron: `0 9 * * 1`",
      "- Time zone: UTC",
      "",
      "### Instructions",
      "",
      "Group the changes:",
      "",
      "#### Fixes",
      "",
      "- one thing",
      "- another thing",
      "",
      "Then post it.",
      "",
    ].join("\n"),
  );
  assert.deepEqual(imported.errors, []);
  assert.equal(
    imported.templates[0].instructions,
    "Group the changes:\n\n#### Fixes\n\n- one thing\n- another thing\n\nThen post it.",
  );
});

test("oversized names and instructions are refused before they reach the server", () => {
  assert.deepEqual(
    validateTemplate({ name: "x".repeat(161), instructions: "", cron: "0 9 * * *", timeZone: "UTC" }),
    ["name must be at most 160 characters"],
  );
  assert.deepEqual(
    validateTemplate({ name: "ok", instructions: "y".repeat(4097), cron: "0 9 * * *", timeZone: "UTC" }),
    ["instructions must be at most 4096 bytes"],
  );
  assert.deepEqual(
    validateTemplate({ name: "  ", instructions: "", cron: "bad", timeZone: "Mars/Olympus" }),
    ["name is required", "schedule must be five valid cron fields", "time zone is not recognized"],
  );
});
