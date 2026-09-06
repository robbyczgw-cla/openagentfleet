// Schedule and template helpers for the Routines workspace. Everything here is
// pure so it can be unit tested without a DOM or a server, and so the workspace
// can validate a draft before spending a round trip on it.
//
// The cron rules below mirror internal/domain/routine.go. When they drift, the
// server rejects a draft the picker already accepted, which reads to the user
// as the form silently failing.

export const CRON_FIELD_RANGES: [number, number][] = [
  [0, 59],
  [0, 23],
  [1, 31],
  [1, 12],
  [0, 7],
];

export const ROUTINE_NAME_MAX_RUNES = 160;
export const ROUTINE_INSTRUCTIONS_MAX_BYTES = 4096;
export const ROUTINE_CRON_MAX_BYTES = 256;

export const WEEKDAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

export type SimpleSchedule =
  | { kind: "daily"; time: string }
  | { kind: "weekdays"; time: string }
  | { kind: "weekly"; time: string; weekday: number };

export type WorkflowTemplate = {
  name: string;
  instructions: string;
  cron: string;
  timeZone: string;
};

export type WorkflowImport = {
  templates: WorkflowTemplate[];
  errors: string[];
  warnings: string[];
};

function countBytes(value: string): number {
  return new TextEncoder().encode(value).length;
}

function parseCronNumber(value: string): number | null {
  // Go's strconv.Atoi accepts a leading sign, so the picker does too rather
  // than rejecting an expression the server would have taken.
  if (!/^[+-]?\d+$/.test(value)) return null;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : null;
}

function validCronField(field: string, minimum: number, maximum: number): boolean {
  for (const listItem of field.split(",")) {
    if (listItem === "") return false;
    let base = listItem;
    if (listItem.includes("/")) {
      const parts = listItem.split("/");
      if (parts.length !== 2 || parts[0] === "") return false;
      const step = parseCronNumber(parts[1]);
      if (step === null || step < 1 || step > maximum - minimum + 1) return false;
      base = parts[0];
    }
    if (base === "*") continue;
    if (base.includes("-")) {
      const bounds = base.split("-");
      if (bounds.length !== 2) return false;
      const start = parseCronNumber(bounds[0]);
      const end = parseCronNumber(bounds[1]);
      if (start === null || end === null) return false;
      if (start < minimum || end > maximum || start > end) return false;
      continue;
    }
    const number = parseCronNumber(base);
    if (number === null || number < minimum || number > maximum) return false;
  }
  return true;
}

export function normalizeCronExpression(value: string): string {
  return value.trim().split(/\s+/).filter(Boolean).join(" ");
}

export function isValidCronExpression(value: string): boolean {
  const normalized = normalizeCronExpression(value);
  if (normalized === "" || countBytes(normalized) > ROUTINE_CRON_MAX_BYTES) return false;
  const fields = normalized.split(" ");
  if (fields.length !== 5) return false;
  return fields.every((field, index) =>
    validCronField(field, CRON_FIELD_RANGES[index][0], CRON_FIELD_RANGES[index][1]),
  );
}

export function isValidTimeZone(zone: string): boolean {
  const trimmed = zone.trim();
  if (trimmed === "") return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: trimmed });
    return true;
  } catch {
    return false;
  }
}

export function parseClockTime(value: string): { hour: number; minute: number } | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim());
  if (!match) return null;
  const hour = Number.parseInt(match[1], 10);
  const minute = Number.parseInt(match[2], 10);
  if (hour < 0 || hour > 23 || minute < 0 || minute > 59) return null;
  return { hour, minute };
}

export function formatClockTime(hour: number, minute: number): string {
  return `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
}

export function scheduleToCron(schedule: SimpleSchedule): string | null {
  const clock = parseClockTime(schedule.time);
  if (!clock) return null;
  const when = `${clock.minute} ${clock.hour}`;
  if (schedule.kind === "daily") return `${when} * * *`;
  if (schedule.kind === "weekdays") return `${when} * * 1-5`;
  if (!Number.isInteger(schedule.weekday) || schedule.weekday < 0 || schedule.weekday > 6) {
    return null;
  }
  return `${when} * * ${schedule.weekday}`;
}

// cronToSchedule is the inverse of scheduleToCron for the shapes the easy
// picker can produce. Anything else returns null so the workspace shows the
// advanced cron field instead of quietly rounding the schedule off.
export function cronToSchedule(cron: string): SimpleSchedule | null {
  if (!isValidCronExpression(cron)) return null;
  const [minute, hour, dayOfMonth, month, dayOfWeek] = normalizeCronExpression(cron).split(" ");
  if (dayOfMonth !== "*" || month !== "*") return null;
  const parsedMinute = parseCronNumber(minute);
  const parsedHour = parseCronNumber(hour);
  if (parsedMinute === null || parsedHour === null) return null;
  const time = formatClockTime(parsedHour, parsedMinute);
  if (dayOfWeek === "*") return { kind: "daily", time };
  if (dayOfWeek === "1-5") return { kind: "weekdays", time };
  const weekday = parseCronNumber(dayOfWeek);
  if (weekday === null || weekday < 0 || weekday > 7) return null;
  // Cron treats both 0 and 7 as Sunday.
  return { kind: "weekly", time, weekday: weekday === 7 ? 0 : weekday };
}

export function describeSchedule(cron: string): string {
  const schedule = cronToSchedule(cron);
  if (!schedule) {
    return isValidCronExpression(cron)
      ? `Custom schedule (${normalizeCronExpression(cron)})`
      : "Not a valid schedule";
  }
  if (schedule.kind === "daily") return `Every day at ${schedule.time}`;
  if (schedule.kind === "weekdays") return `Every weekday at ${schedule.time}`;
  return `Every ${WEEKDAY_NAMES[schedule.weekday]} at ${schedule.time}`;
}

export function validateTemplate(template: WorkflowTemplate): string[] {
  const problems: string[] = [];
  const name = template.name.trim();
  if (name === "") {
    problems.push("name is required");
  } else if ([...name].length > ROUTINE_NAME_MAX_RUNES) {
    problems.push(`name must be at most ${ROUTINE_NAME_MAX_RUNES} characters`);
  }
  if (countBytes(template.instructions) > ROUTINE_INSTRUCTIONS_MAX_BYTES) {
    problems.push(`instructions must be at most ${ROUTINE_INSTRUCTIONS_MAX_BYTES} bytes`);
  }
  if (!isValidCronExpression(template.cron)) {
    problems.push("schedule must be five valid cron fields");
  }
  if (!isValidTimeZone(template.timeZone)) {
    problems.push("time zone is not recognized");
  }
  return problems;
}

// A control character in a name or instruction body is rejected by the server.
// Imported files come from outside the app, so strip rather than fail: tabs and
// newlines survive, everything else that cannot be typed is dropped.
function stripControlCharacters(value: string, keepWhitespace: boolean): string {
  let cleaned = "";
  for (const character of value) {
    const code = character.codePointAt(0) ?? 0;
    const isControl = code < 0x20 || (code >= 0x7f && code <= 0x9f);
    if (!isControl) {
      cleaned += character;
      continue;
    }
    if (keepWhitespace && (character === "\n" || character === "\t")) cleaned += character;
  }
  return cleaned;
}

export const EXPORT_HEADING = "# OpenAgentFleet workflows";

const EXPORT_PREAMBLE = [
  "Each workflow below describes what to run and when. Importing one creates a",
  "disabled routine: you choose which agent owns it and turn it on yourself.",
].join("\n");

const FENCE_LINE = /^\s{0,3}(`{3,}|~{3,})\s*\S*\s*$/;

// Instructions routinely contain Markdown of their own: a "## Fixes" heading, a
// shell block whose first line is a comment. Left bare, those lines read as new
// sections and truncate the body. Wrapping the body in a fence longer than any
// backtick run inside it makes the boundary unambiguous, so export/import is
// exact. Bodies with no heading and no fence stay bare, which keeps the common
// file readable.
function instructionFence(body: string): string | null {
  const risky =
    body !== body.trim() ||
    body.split("\n").some((line) => /^\s{0,3}#/.test(line) || FENCE_LINE.test(line));
  if (!risky) return null;
  let longest = 0;
  for (const run of body.matchAll(/`+/g)) longest = Math.max(longest, run[0].length);
  return "`".repeat(Math.max(3, longest + 1));
}

export function exportWorkflowMarkdown(templates: WorkflowTemplate[]): string {
  const sections = templates.map((template) => {
    const cron = normalizeCronExpression(template.cron);
    const fence = instructionFence(template.instructions);
    // A fenced body is written byte for byte, including its own indentation.
    // A bare body is trimmed, which is what keeps the ordinary file tidy.
    const body = fence ? template.instructions : template.instructions.trim();
    return [
      `## ${template.name.trim()}`,
      "",
      `- Schedule: ${describeSchedule(cron)}`,
      `- Cron: \`${cron}\``,
      `- Time zone: ${template.timeZone.trim()}`,
      "",
      "### Instructions",
      "",
      ...(fence ? [fence, body, fence] : [body]),
    ].join("\n");
  });
  return [EXPORT_HEADING, "", EXPORT_PREAMBLE, "", ...sections].join("\n").trimEnd() + "\n";
}

function parseBullet(line: string): { key: string; value: string } | null {
  const match = /^[-*]\s+([^:]+):\s*(.*)$/.exec(line.trim());
  if (!match) return null;
  return { key: match[1].trim().toLowerCase(), value: match[2].trim() };
}

function stripInlineCode(value: string): string {
  const match = /^`(.*)`$/.exec(value.trim());
  return match ? match[1].trim() : value.trim();
}

type ParsedSection = {
  name: string;
  cron: string;
  timeZone: string;
  instructions: string[];
  // A fenced body is taken verbatim. A bare one is trimmed, so a hand-written
  // file does not depend on exactly where the blank lines fall.
  fenced: boolean;
  ignoredKeys: string[];
};

// importWorkflowMarkdown deliberately reads only the name, cadence, zone and
// instructions. It never reads an owner, an enabled flag, a routine id, a
// webhook secret or run history, so a file from someone else cannot hand itself
// an agent or start running on import.
export function importWorkflowMarkdown(markdown: string): WorkflowImport {
  const lines = markdown.split(/\r?\n/);
  const sections: ParsedSection[] = [];
  let current: ParsedSection | null = null;
  let inInstructions = false;
  // Set while reading a fenced instruction body. Nothing inside is interpreted,
  // so headings and code blocks in the body survive intact.
  let openFence = "";
  let awaitingBody = false;

  for (const line of lines) {
    if (openFence !== "") {
      const closing = FENCE_LINE.exec(line);
      if (closing && closing[1][0] === openFence[0] && closing[1].length >= openFence.length) {
        openFence = "";
        inInstructions = false;
        continue;
      }
      current?.instructions.push(line);
      continue;
    }
    if (awaitingBody) {
      if (line.trim() === "") continue;
      const opening = FENCE_LINE.exec(line);
      awaitingBody = false;
      if (opening) {
        openFence = opening[1];
        if (current) current.fenced = true;
        continue;
      }
      // A bare body. Fall through so this line is read as instructions.
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = heading[1].length;
      const text = heading[2].trim();
      // A level-2 heading always starts a workflow, including one named
      // "Instructions". Treating that name as a body marker silently merged the
      // workflow into whichever one came before it.
      if (level <= 2) {
        current =
          level === 2
            ? { name: text, cron: "", timeZone: "", instructions: [], fenced: false, ignoredKeys: [] }
            : null;
        if (current) sections.push(current);
        inInstructions = false;
        awaitingBody = false;
        continue;
      }
      if (/^instructions$/i.test(text) && !inInstructions) {
        inInstructions = current !== null;
        awaitingBody = inInstructions;
        continue;
      }
      // A deeper heading inside the instruction body is part of the body.
      if (inInstructions && current) current.instructions.push(line);
      continue;
    }
    if (!current) continue;
    if (!inInstructions) {
      const bullet = parseBullet(line);
      if (bullet) {
        if (bullet.key === "cron") current.cron = stripInlineCode(bullet.value);
        else if (bullet.key === "time zone" || bullet.key === "timezone") {
          current.timeZone = stripInlineCode(bullet.value);
        } else if (bullet.key !== "schedule") {
          current.ignoredKeys.push(bullet.key);
        }
      }
      continue;
    }
    current.instructions.push(line);
  }

  const templates: WorkflowTemplate[] = [];
  const errors: string[] = [];
  const warnings: string[] = [];

  for (const section of sections) {
    const label = section.name.trim() || "Untitled workflow";
    const body = stripControlCharacters(section.instructions.join("\n"), true);
    const template: WorkflowTemplate = {
      name: stripControlCharacters(section.name, false).trim(),
      instructions: section.fenced ? body : body.trim(),
      cron: normalizeCronExpression(section.cron),
      timeZone: section.timeZone.trim(),
    };
    const problems = validateTemplate(template);
    if (problems.length > 0) {
      errors.push(`${label}: ${problems.join(", ")}`);
      continue;
    }
    // Names are not unique: the same workflow can exist for two agents or on two
    // schedules. Both sections are kept, and callers key them by position.
    if (section.ignoredKeys.length > 0) {
      warnings.push(`${label}: ignored ${[...new Set(section.ignoredKeys)].sort().join(", ")}`);
    }
    templates.push(template);
  }

  if (sections.length === 0) {
    errors.push("No workflows found. Each workflow needs a '## Name' heading.");
  }
  return { templates, errors, warnings };
}

export type RoutineForExport = {
  name: string;
  kind: string;
  description?: string;
  cron_expression?: string;
  time_zone: string;
};

export type ExportSelection = { templates: WorkflowTemplate[]; unsupported: string[] };

// Only cron routines can be written as a workflow template. A heartbeat routine's
// cadence lives in an interval this format has no field for, so exporting one
// emitted an empty cron that could never be imported back. Naming the ones left
// out is better than writing a section that silently fails later.
export function selectExportableRoutines(routines: RoutineForExport[]): ExportSelection {
  const templates: WorkflowTemplate[] = [];
  const unsupported: string[] = [];
  for (const routine of routines) {
    const cron = normalizeCronExpression(routine.cron_expression ?? "");
    if (routine.kind !== "cron" || !isValidCronExpression(cron)) {
      unsupported.push(routine.name);
      continue;
    }
    templates.push({
      name: routine.name,
      instructions: routine.description ?? "",
      cron,
      timeZone: routine.time_zone,
    });
  }
  return { templates, unsupported };
}

export const STARTER_TEMPLATES: WorkflowTemplate[] = [
  {
    name: "Morning standup notes",
    instructions:
      "Summarize what the team merged yesterday. List each change in one line, then flag anything that looks unfinished or risky. Keep it under 20 lines.",
    cron: "30 6 * * 1-5",
    timeZone: "UTC",
  },
  {
    name: "Weekly dependency check",
    instructions:
      "Check the project's dependencies for new releases and known advisories. Report only the ones that need action, with the version to move to and why.",
    cron: "0 9 * * 1",
    timeZone: "UTC",
  },
  {
    name: "Nightly test triage",
    instructions:
      "Read the most recent test run. For each failure, say whether it is a real regression or a flake, and point at the change most likely responsible.",
    cron: "0 22 * * *",
    timeZone: "UTC",
  },
];
