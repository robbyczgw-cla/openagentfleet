import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import "./RoutinesWorkspace.css";
import {
  STARTER_TEMPLATES,
  WEEKDAY_NAMES,
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
} from "./workflowTemplates";
import type { SimpleSchedule, WorkflowTemplate } from "./workflowTemplates";
import { ProjectSelector } from "./ProjectsWorkspace";

// The parent renders the dialog: backdrop, role, focus trap and Escape all live
// in App.tsx. This component is only the contents, so it adds no outer chrome
// and never moves focus on its own.
export type RoutinesWorkspaceProps = {
  fetch: (input: string, init?: RequestInit) => Promise<Response>;
  agents: { id: string; name: string }[];
  onClose?: () => void;
  onAdvanced?: () => void;
  initialDraft?: { name?: string; instructions: string };
};

type Routine = {
  id: string;
  bot_id: string;
  name: string;
  description?: string;
  kind: string;
  status: string;
  cron_expression?: string;
  time_zone: string;
  heartbeat_opt_in?: boolean;
  heartbeat_interval_seconds?: number;
  next_run_at?: string;
  last_run_at?: string;
  attention_reason?: string;
};

type RoutineEvent = {
  id: string;
  type: string;
  message?: string;
  created_at: string;
};

type ScheduleDraft = {
  name: string;
  instructions: string;
  mode: "easy" | "advanced";
  repeat: "daily" | "weekdays" | "weekly";
  weekday: number;
  time: string;
  cron: string;
  timeZone: string;
};

// The easy path pins one lead/worker pair, Grok Build leading and Claude
// working, and asks before anything risky. Choosing different engines is what
// the advanced form is for, so this path never quietly widens what an agent may
// do. Both engines have to be configured or the routine cannot run.
const DEFAULT_LEAD_HARNESS = "grok_build";
const DEFAULT_WORKER = "claude";
const DEFAULT_APPROVAL_POLICY = "on_risk";
const DEFAULT_RETRY = { max_attempts: 1, backoff_seconds: 0 };

const CALENDAR_DAYS = 7;
const PREVIEW_LIMIT = 32;

const STATUS_LABELS: Record<string, string> = {
  enabled: "On",
  paused: "Paused",
  disabled: "Off",
  needs_attention: "Needs attention",
};

const FALLBACK_TIME_ZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/New_York",
  "Europe/London",
  "Europe/Vienna",
  "Asia/Tokyo",
  "Australia/Sydney",
];

function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function listTimeZones(): string[] {
  // Intl.supportedValuesOf is newer than this project's TS lib target.
  const intl = Intl as unknown as { supportedValuesOf?: (key: string) => string[] };
  let zones: string[] = [];
  try {
    if (typeof intl.supportedValuesOf === "function") zones = intl.supportedValuesOf("timeZone");
  } catch {
    zones = [];
  }
  if (zones.length === 0) zones = FALLBACK_TIME_ZONES;
  return [...new Set(["UTC", browserTimeZone(), ...zones])].sort();
}

function draftFromInitial(initial: RoutinesWorkspaceProps["initialDraft"]): ScheduleDraft {
  const base = emptyDraft();
  if (!initial) return base;
  return { ...base, name: initial.name ?? base.name, instructions: initial.instructions };
}

function emptyDraft(): ScheduleDraft {
  return {
    name: "",
    instructions: "",
    mode: "easy",
    repeat: "daily",
    weekday: 1,
    time: "09:00",
    cron: "0 9 * * *",
    timeZone: browserTimeZone(),
  };
}

function draftFromRoutine(routine: Routine): ScheduleDraft {
  const cron = normalizeCronExpression(routine.cron_expression ?? "");
  const schedule = cronToSchedule(cron);
  const base: ScheduleDraft = {
    ...emptyDraft(),
    name: routine.name,
    instructions: routine.description ?? "",
    cron: cron || "0 9 * * *",
    timeZone: routine.time_zone,
    // An expression the picker cannot represent opens in the advanced field so
    // editing the name never silently rewrites the cadence.
    mode: schedule ? "easy" : "advanced",
  };
  if (!schedule) return base;
  base.time = schedule.time;
  base.repeat = schedule.kind;
  if (schedule.kind === "weekly") base.weekday = schedule.weekday;
  return base;
}

function draftSchedule(draft: ScheduleDraft): SimpleSchedule {
  if (draft.repeat === "weekly") {
    return { kind: "weekly", time: draft.time, weekday: draft.weekday };
  }
  return { kind: draft.repeat, time: draft.time };
}

function draftCron(draft: ScheduleDraft): string {
  if (draft.mode === "advanced") return normalizeCronExpression(draft.cron);
  return scheduleToCron(draftSchedule(draft)) ?? "";
}

function draftTemplate(draft: ScheduleDraft): WorkflowTemplate {
  return {
    name: draft.name,
    instructions: draft.instructions,
    cron: draftCron(draft),
    timeZone: draft.timeZone,
  };
}

function templateToDraft(template: WorkflowTemplate): ScheduleDraft {
  const schedule = cronToSchedule(template.cron);
  const base: ScheduleDraft = {
    ...emptyDraft(),
    name: template.name,
    instructions: template.instructions,
    cron: template.cron,
    timeZone: template.timeZone,
    mode: schedule ? "easy" : "advanced",
  };
  if (!schedule) return base;
  base.time = schedule.time;
  base.repeat = schedule.kind;
  if (schedule.kind === "weekly") base.weekday = schedule.weekday;
  return base;
}

async function readError(response: Response): Promise<string> {
  try {
    const body = (await response.json()) as { error?: string };
    if (body && typeof body.error === "string" && body.error) return body.error;
  } catch {
    // Fall through to the status text below.
  }
  return `Request failed (${response.status})`;
}

function startOfDay(value: Date): Date {
  const copy = new Date(value);
  copy.setHours(0, 0, 0, 0);
  return copy;
}

function addDays(value: Date, days: number): Date {
  const copy = new Date(value);
  copy.setDate(copy.getDate() + days);
  return copy;
}

function dayKey(value: Date): string {
  return `${value.getFullYear()}-${value.getMonth() + 1}-${value.getDate()}`;
}

function formatDayHeading(value: Date): string {
  return value.toLocaleDateString(undefined, {
    weekday: "long",
    month: "short",
    day: "numeric",
  });
}

function formatTimeOfDay(value: Date): string {
  return value.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function formatTimestamp(value?: string): string {
  if (!value) return "";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

type Occurrence = { routine: Routine; at: Date };

export function RoutinesWorkspace(props: RoutinesWorkspaceProps): React.ReactElement {
  const { fetch: apiFetch, agents, onClose, onAdvanced, initialDraft } = props;

  const [tab, setTab] = useState<"schedule" | "new" | "templates">(
    initialDraft ? "new" : "schedule",
  );
  const [routines, setRoutines] = useState<Routine[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  const [agentFilter, setAgentFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState("all");
  const [selectedID, setSelectedID] = useState("");

  const [calendarView, setCalendarView] = useState<"agenda" | "week">("agenda");
  const [windowStart, setWindowStart] = useState(() => startOfDay(new Date()));
  const [occurrences, setOccurrences] = useState<Record<string, string[]>>({});
  const [calendarBusy, setCalendarBusy] = useState(false);
  const [truncated, setTruncated] = useState<string[]>([]);
  const [unavailable, setUnavailable] = useState<string[]>([]);

  const [history, setHistory] = useState<RoutineEvent[]>([]);
  const [historyFor, setHistoryFor] = useState("");

  const [editDraft, setEditDraft] = useState<ScheduleDraft | null>(null);
  // Seeding from initialDraft here rather than in an effect means the prefilled
  // form is correct on the first paint instead of flashing empty.
  const [createDraft, setCreateDraft] = useState<ScheduleDraft>(() =>
    draftFromInitial(initialDraft),
  );
  const [createAgent, setCreateAgent] = useState(agents[0]?.id ?? "");
  const [createPreview, setCreatePreview] = useState<string[]>([]);
  const [createPreviewError, setCreatePreviewError] = useState("");

  const exportRef = useRef<HTMLTextAreaElement>(null);
  const [importText, setImportText] = useState("");
  const [importResult, setImportResult] = useState<ReturnType<typeof importWorkflowMarkdown> | null>(
    null,
  );

  const timeZones = useMemo(listTimeZones, []);
  const agentNames = useMemo(() => {
    const map = new Map<string, string>();
    for (const agent of agents) map.set(agent.id, agent.name);
    return map;
  }, [agents]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const response = await apiFetch("/api/routines");
      if (!response.ok) {
        setError(await readError(response));
        return;
      }
      const body = (await response.json()) as { routines?: Routine[] };
      setRoutines(body.routines ?? []);
      setError("");
    } catch {
      setError("Could not reach the server.");
    } finally {
      setLoading(false);
    }
  }, [apiFetch]);

  useEffect(() => {
    void load();
  }, [load]);

  // Applying a draft once, by identity, keeps a re-render from overwriting what
  // the user has typed since. The draft present at mount is already applied.
  const appliedDraft = useRef<RoutinesWorkspaceProps["initialDraft"]>(initialDraft);
  useEffect(() => {
    if (!initialDraft || appliedDraft.current === initialDraft) return;
    appliedDraft.current = initialDraft;
    setCreateDraft((previous) => ({
      ...previous,
      name: initialDraft.name ?? previous.name,
      instructions: initialDraft.instructions,
    }));
    setTab("new");
  }, [initialDraft]);

  useEffect(() => {
    if (!createAgent && agents.length > 0) setCreateAgent(agents[0].id);
  }, [agents, createAgent]);

  const visibleRoutines = useMemo(
    () =>
      routines.filter(
        (routine) =>
          (agentFilter === "all" || routine.bot_id === agentFilter) &&
          (statusFilter === "all" || routine.status === statusFilter),
      ),
    [routines, agentFilter, statusFilter],
  );

  const selected = useMemo(
    () => visibleRoutines.find((routine) => routine.id === selectedID) ?? null,
    [visibleRoutines, selectedID],
  );

  const calendarRoutines = useMemo(
    () =>
      visibleRoutines.filter(
        (routine) => routine.kind === "cron" && isValidCronExpression(routine.cron_expression ?? ""),
      ),
    [visibleRoutines],
  );

  // Refetching the calendar on every render would loop, so the effect keys off
  // the cadences it actually reads rather than the routine objects.
  const calendarKey = useMemo(
    () =>
      JSON.stringify([
        windowStart.getTime(),
        calendarRoutines.map((routine) => [routine.id, routine.cron_expression, routine.time_zone]),
      ]),
    [windowStart, calendarRoutines],
  );

  useEffect(() => {
    let ignore = false;
    const controller = new AbortController();
    const windowEnd = addDays(windowStart, CALENDAR_DAYS);
    const after = new Date(windowStart.getTime() - 60_000).toISOString();

    async function loadCalendar() {
      if (calendarRoutines.length === 0) {
        setOccurrences({});
        setTruncated([]);
        return;
      }
      setCalendarBusy(true);
      const next: Record<string, string[]> = {};
      const cut: string[] = [];
      const failed: string[] = [];
      await Promise.all(
        calendarRoutines.map(async (routine) => {
          try {
            const response = await apiFetch("/api/routines/preview", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              signal: controller.signal,
              body: JSON.stringify({
                cron_expression: routine.cron_expression,
                time_zone: routine.time_zone,
                after,
                count: PREVIEW_LIMIT,
              }),
            });
            if (!response.ok) {
              failed.push(routine.name);
              return;
            }
            const body = (await response.json()) as { occurrences?: string[] };
            const times = body.occurrences ?? [];
            next[routine.id] = times;
            const lastTime = times[times.length - 1];
            if (times.length === PREVIEW_LIMIT && lastTime && new Date(lastTime) < windowEnd) {
              cut.push(routine.name);
            }
          } catch (cause) {
            // Dropping a routine silently would read as "nothing scheduled",
            // which is the one thing a calendar must never get wrong.
            if ((cause as { name?: string } | null)?.name !== "AbortError") {
              failed.push(routine.name);
            }
          }
        }),
      );
      if (ignore) return;
      setOccurrences(next);
      setTruncated(cut);
      setUnavailable(failed);
      setCalendarBusy(false);
    }

    void loadCalendar();
    return () => {
      ignore = true;
      controller.abort();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [calendarKey, apiFetch]);

  const days = useMemo(() => {
    const windowEnd = addDays(windowStart, CALENDAR_DAYS);
    const buckets = new Map<string, Occurrence[]>();
    for (let index = 0; index < CALENDAR_DAYS; index += 1) {
      buckets.set(dayKey(addDays(windowStart, index)), []);
    }
    for (const routine of calendarRoutines) {
      for (const iso of occurrences[routine.id] ?? []) {
        const at = new Date(iso);
        if (Number.isNaN(at.getTime()) || at < windowStart || at >= windowEnd) continue;
        buckets.get(dayKey(at))?.push({ routine, at });
      }
    }
    return Array.from({ length: CALENDAR_DAYS }, (_, index) => {
      const date = addDays(windowStart, index);
      const items = (buckets.get(dayKey(date)) ?? []).sort(
        (left, right) => left.at.getTime() - right.at.getTime(),
      );
      return { date, items };
    });
  }, [windowStart, calendarRoutines, occurrences]);

  const totalOccurrences = days.reduce((sum, day) => sum + day.items.length, 0);

  const request = useCallback(
    async (path: string, init: RequestInit, success: string): Promise<boolean> => {
      setBusy(true);
      setNotice("");
      try {
        const response = await apiFetch(path, init);
        if (!response.ok) {
          setError(await readError(response));
          return false;
        }
        setError("");
        setNotice(success);
        return true;
      } catch {
        setError("Could not reach the server.");
        return false;
      } finally {
        setBusy(false);
      }
    },
    [apiFetch],
  );

  const showHistory = useCallback(
    async (routineID: string) => {
      if (historyFor === routineID) {
        setHistoryFor("");
        return;
      }
      try {
        const response = await apiFetch(`/api/routines/${routineID}/history`);
        if (!response.ok) {
          setError(await readError(response));
          return;
        }
        const body = (await response.json()) as { history?: RoutineEvent[] };
        setHistory(body.history ?? []);
        setHistoryFor(routineID);
      } catch {
        setError("Could not reach the server.");
      }
    },
    [apiFetch, historyFor],
  );

  const runTest = useCallback(
    async (routine: Routine) => {
      setBusy(true);
      setNotice("");
      try {
        const response = await apiFetch(`/api/routines/${routine.id}/test`, { method: "POST" });
        if (!response.ok) {
          setError(await readError(response));
          return;
        }
        const body = (await response.json()) as { waiting_for_approval?: boolean };
        setError("");
        setNotice(
          body.waiting_for_approval
            ? "Test run is waiting for approval. Approve it in Review."
            : `Test run started for ${routine.name}.`,
        );
        await load();
      } catch {
        setError("Could not reach the server.");
      } finally {
        setBusy(false);
      }
    },
    [apiFetch, load],
  );

  const setLifecycle = useCallback(
    async (routine: Routine, action: "enable" | "pause" | "resolve", success: string) => {
      const init: RequestInit = { method: "POST" };
      if (action === "pause") {
        init.headers = { "Content-Type": "application/json" };
        init.body = JSON.stringify({ reason: "paused from the routines workspace" });
      }
      if (await request(`/api/routines/${routine.id}/${action}`, init, success)) await load();
    },
    [request, load],
  );

  const pauseAndEdit = useCallback(
    async (routine: Routine) => {
      // Editing an enabled routine is refused by the server: the scheduler could
      // claim it between the read and the write. Pausing first is the safe order.
      if (routine.status === "enabled" || routine.status === "needs_attention") {
        const paused = await request(
          `/api/routines/${routine.id}/pause`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ reason: "paused to edit" }),
          },
          `Paused ${routine.name} so it can be edited.`,
        );
        if (!paused) return;
        await load();
      }
      setEditDraft(draftFromRoutine(routine));
    },
    [request, load],
  );

  const saveEdit = useCallback(async () => {
    if (!selected || !editDraft) return;
    const heartbeat = selected.kind === "heartbeat";
    const cron = heartbeat ? "" : draftCron(editDraft);
    if (!heartbeat && !isValidCronExpression(cron)) {
      setError("That schedule is not valid. Check the time or the cron expression.");
      return;
    }
    if (!isValidTimeZone(editDraft.timeZone)) {
      setError("That time zone is not recognized.");
      return;
    }
    const saved = await request(
      `/api/routines/${selected.id}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: editDraft.name,
          description: editDraft.instructions,
          cron_expression: cron,
          time_zone: editDraft.timeZone,
        }),
      },
      `Saved ${editDraft.name.trim() || selected.name}. It stays off until you turn it on.`,
    );
    if (!saved) return;
    setEditDraft(null);
    await load();
  }, [selected, editDraft, request, load]);

  const createRoutine = useCallback(
    async (draft: ScheduleDraft, botID: string): Promise<boolean> => {
      const template = draftTemplate(draft);
      const problems = validateTemplate(template);
      if (!botID) problems.unshift("choose which agent owns this workflow");
      if (problems.length > 0) {
        setError(`Cannot create this workflow: ${problems.join(", ")}.`);
        return false;
      }
      // No next_run_at is sent, so the routine is stored disabled. Turning it on
      // stays a separate, deliberate step.
      return request(
        "/api/routines",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            bot_id: botID,
            name: template.name.trim(),
            description: template.instructions.trim(),
            kind: "cron",
            cron_expression: template.cron,
            time_zone: template.timeZone.trim(),
            lead_harness: DEFAULT_LEAD_HARNESS,
            worker: DEFAULT_WORKER,
            approval_policy: DEFAULT_APPROVAL_POLICY,
            retry: DEFAULT_RETRY,
          }),
        },
        `Created ${template.name.trim()}. It is off until you turn it on.`,
      );
    },
    [request],
  );

  const createCron = draftCron(createDraft);

  // The preview is server-computed so the picker cannot promise a time the
  // scheduler would not actually pick.
  useEffect(() => {
    if (tab !== "new") return;
    if (!isValidCronExpression(createCron) || !isValidTimeZone(createDraft.timeZone)) {
      setCreatePreview([]);
      setCreatePreviewError(
        createCron === "" ? "" : "Enter a valid schedule and time zone to see the next runs.",
      );
      return;
    }
    let ignore = false;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const response = await apiFetch("/api/routines/preview", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          signal: controller.signal,
          body: JSON.stringify({
            cron_expression: createCron,
            time_zone: createDraft.timeZone,
            count: 5,
          }),
        });
        if (ignore) return;
        if (!response.ok) {
          setCreatePreview([]);
          setCreatePreviewError(await readError(response));
          return;
        }
        const body = (await response.json()) as { occurrences?: string[] };
        setCreatePreview(body.occurrences ?? []);
        setCreatePreviewError("");
      } catch (cause) {
        // Aborting is how this effect cleans up after itself, so only a real
        // failure is worth reporting. Staying quiet would leave the last
        // schedule's times on screen next to a schedule that never loaded.
        if (ignore || (cause as { name?: string } | null)?.name === "AbortError") return;
        setCreatePreview([]);
        setCreatePreviewError("Could not reach the server to preview this schedule.");
      }
    }, 200);
    return () => {
      ignore = true;
      controller.abort();
      clearTimeout(timer);
    };
  }, [tab, createCron, createDraft.timeZone, apiFetch]);

  // Only these four fields cross the export boundary, and only cron routines
  // can cross it at all. The routine id, the owning agent, the lifecycle state
  // and the run history stay behind.
  const exportable = useMemo(() => selectExportableRoutines(visibleRoutines), [visibleRoutines]);
  const exportMarkdown = useMemo(
    () => exportWorkflowMarkdown(exportable.templates),
    [exportable],
  );

  // The Clipboard API is missing on insecure origins and can be refused by
  // permission. Claiming success without waiting for it tells the user their
  // workflows are on the clipboard when nothing was copied, so wait, and on
  // failure select the text and say plainly that they have to copy it.
  const copyExport = useCallback(async () => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
      await navigator.clipboard.writeText(exportMarkdown);
      setError("");
      setNotice("Copied the workflow Markdown.");
    } catch {
      exportRef.current?.focus();
      exportRef.current?.select();
      setNotice("");
      setError("Could not copy automatically. The Markdown is selected: press Ctrl+C or Cmd+C.");
    }
  }, [exportMarkdown]);

  const tabs = [
    { id: "schedule", label: "Schedule" },
    { id: "new", label: "New workflow" },
    { id: "templates", label: "Templates" },
  ] as const;

  function onTabKeyDown(event: React.KeyboardEvent<HTMLDivElement>) {
    const index = tabs.findIndex((entry) => entry.id === tab);
    if (event.key === "ArrowRight") setTab(tabs[(index + 1) % tabs.length].id);
    else if (event.key === "ArrowLeft") setTab(tabs[(index - 1 + tabs.length) % tabs.length].id);
    else if (event.key === "Home") setTab(tabs[0].id);
    else if (event.key === "End") setTab(tabs[tabs.length - 1].id);
    else return;
    event.preventDefault();
  }

  function renderSchedulePicker(draft: ScheduleDraft, update: (next: ScheduleDraft) => void, idPrefix: string) {
    const cron = draftCron(draft);
    const valid = isValidCronExpression(cron);
    return (
      <div className="rw-schedule">
        <div className="rw-mode" role="group" aria-label="Schedule editor">
          <button
            type="button"
            className={draft.mode === "easy" ? "rw-chip is-on" : "rw-chip"}
            aria-pressed={draft.mode === "easy"}
            onClick={() => update({ ...draft, mode: "easy" })}
          >
            Simple
          </button>
          <button
            type="button"
            className={draft.mode === "advanced" ? "rw-chip is-on" : "rw-chip"}
            aria-pressed={draft.mode === "advanced"}
            onClick={() => update({ ...draft, mode: "advanced", cron: cron || draft.cron })}
          >
            Advanced cron
          </button>
        </div>

        {draft.mode === "easy" ? (
          <div className="rw-field-row">
            <div className="rw-field">
              <label htmlFor={`${idPrefix}-repeat`}>Repeat</label>
              <select
                id={`${idPrefix}-repeat`}
                value={draft.repeat}
                onChange={(event) =>
                  update({ ...draft, repeat: event.target.value as ScheduleDraft["repeat"] })
                }
              >
                <option value="daily">Every day</option>
                <option value="weekdays">Every weekday</option>
                <option value="weekly">Every week</option>
              </select>
            </div>
            {draft.repeat === "weekly" && (
              <div className="rw-field">
                <label htmlFor={`${idPrefix}-weekday`}>Day</label>
                <select
                  id={`${idPrefix}-weekday`}
                  value={draft.weekday}
                  onChange={(event) => update({ ...draft, weekday: Number(event.target.value) })}
                >
                  {WEEKDAY_NAMES.map((name, index) => (
                    <option key={name} value={index}>
                      {name}
                    </option>
                  ))}
                </select>
              </div>
            )}
            <div className="rw-field">
              <label htmlFor={`${idPrefix}-time`}>Time</label>
              <input
                id={`${idPrefix}-time`}
                type="time"
                value={draft.time}
                onChange={(event) => update({ ...draft, time: event.target.value })}
              />
            </div>
          </div>
        ) : (
          <div className="rw-field">
            <label htmlFor={`${idPrefix}-cron`}>Cron expression</label>
            <input
              id={`${idPrefix}-cron`}
              className="rw-mono"
              value={draft.cron}
              spellCheck={false}
              aria-describedby={`${idPrefix}-cron-help`}
              aria-invalid={!valid}
              onChange={(event) => update({ ...draft, cron: event.target.value })}
            />
            <p id={`${idPrefix}-cron-help`} className="rw-help">
              Five fields: minute, hour, day of month, month, day of week.
            </p>
          </div>
        )}

        <div className="rw-field">
          <label htmlFor={`${idPrefix}-zone`}>Time zone</label>
          <input
            id={`${idPrefix}-zone`}
            list="rw-time-zones"
            value={draft.timeZone}
            spellCheck={false}
            aria-invalid={!isValidTimeZone(draft.timeZone)}
            onChange={(event) => update({ ...draft, timeZone: event.target.value })}
          />
          {!isValidTimeZone(draft.timeZone) && (
            <p className="rw-help rw-bad">That time zone is not recognized.</p>
          )}
        </div>

        <p className="rw-summary">{describeSchedule(cron)}</p>
      </div>
    );
  }

  return (
    <div className="rw">
      <header className="rw-header">
        <div>
          <p className="rw-eyebrow">Runs on a schedule</p>
          <h2 className="rw-title">Routines and workflows</h2>
        </div>
        <div className="rw-header-actions">
          {onAdvanced && (
            <button type="button" onClick={onAdvanced}>
              Advanced setup
            </button>
          )}
          {onClose && (
            <button type="button" onClick={onClose}>
              Close
            </button>
          )}
        </div>
      </header>

      <div className="rw-tabs" role="tablist" aria-label="Routines workspace" onKeyDown={onTabKeyDown}>
        {tabs.map((entry) => (
          <button
            key={entry.id}
            type="button"
            role="tab"
            id={`rw-tab-${entry.id}`}
            aria-selected={tab === entry.id}
            aria-controls={`rw-panel-${entry.id}`}
            tabIndex={tab === entry.id ? 0 : -1}
            className={tab === entry.id ? "rw-tab is-on" : "rw-tab"}
            onClick={() => setTab(entry.id)}
          >
            {entry.label}
          </button>
        ))}
      </div>

      {error && (
        <p className="rw-alert" role="alert">
          {error}
        </p>
      )}
      <p className="rw-status" role="status" aria-live="polite">
        {notice}
      </p>

      <datalist id="rw-time-zones">
        {timeZones.map((zone) => (
          <option key={zone} value={zone} />
        ))}
      </datalist>

      {tab === "schedule" && (
        <section
          id="rw-panel-schedule"
          role="tabpanel"
          aria-labelledby="rw-tab-schedule"
          tabIndex={0}
          className="rw-panel"
        >
          <div className="rw-filters">
            <div className="rw-field">
              <label htmlFor="rw-filter-agent">Agent</label>
              <select
                id="rw-filter-agent"
                value={agentFilter}
                onChange={(event) => setAgentFilter(event.target.value)}
              >
                <option value="all">All agents</option>
                {agents.map((agent) => (
                  <option key={agent.id} value={agent.id}>
                    {agent.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="rw-field">
              <label htmlFor="rw-filter-status">Status</label>
              <select
                id="rw-filter-status"
                value={statusFilter}
                onChange={(event) => setStatusFilter(event.target.value)}
              >
                <option value="all">Any status</option>
                <option value="enabled">On</option>
                <option value="paused">Paused</option>
                <option value="disabled">Off</option>
                <option value="needs_attention">Needs attention</option>
              </select>
            </div>
            <div className="rw-mode" role="group" aria-label="Calendar view">
              <button
                type="button"
                className={calendarView === "agenda" ? "rw-chip is-on" : "rw-chip"}
                aria-pressed={calendarView === "agenda"}
                onClick={() => setCalendarView("agenda")}
              >
                Agenda
              </button>
              <button
                type="button"
                className={calendarView === "week" ? "rw-chip is-on" : "rw-chip"}
                aria-pressed={calendarView === "week"}
                onClick={() => setCalendarView("week")}
              >
                Week
              </button>
            </div>
          </div>

          <div className="rw-columns">
            <div className="rw-list-column">
              <h3 className="rw-subtitle">
                Routines <span className="rw-count">{visibleRoutines.length}</span>
              </h3>
              {loading ? (
                <p className="rw-empty">Loading routines…</p>
              ) : visibleRoutines.length === 0 ? (
                <p className="rw-empty">
                  No routines match these filters. Use New workflow to add one.
                </p>
              ) : (
                <ul className="rw-list">
                  {visibleRoutines.map((routine) => (
                    <li key={routine.id}>
                      <button
                        type="button"
                        className={routine.id === selectedID ? "rw-item is-on" : "rw-item"}
                        aria-current={routine.id === selectedID}
                        onClick={() => {
                          setSelectedID(routine.id);
                          setEditDraft(null);
                        }}
                      >
                        <span className="rw-item-name">{routine.name}</span>
                        <span className={`rw-badge is-${routine.status}`}>
                          {STATUS_LABELS[routine.status] ?? routine.status}
                        </span>
                        <span className="rw-item-meta">
                          {routine.kind === "heartbeat"
                            ? `Every ${routine.heartbeat_interval_seconds ?? 0}s`
                            : describeSchedule(routine.cron_expression ?? "")}
                          {" · "}
                          {agentNames.get(routine.bot_id) ?? "Unassigned agent"}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div className="rw-detail-column">
              {selected ? (
                <article className="rw-detail">
                  <h3 className="rw-subtitle">{selected.name}</h3>
                  <dl className="rw-facts">
                    <div>
                      <dt>Agent</dt>
                      <dd>{agentNames.get(selected.bot_id) ?? "Unassigned"}</dd>
                    </div>
                    <div>
                      <dt>Status</dt>
                      <dd>{STATUS_LABELS[selected.status] ?? selected.status}</dd>
                    </div>
                    <div>
                      <dt>Schedule</dt>
                      <dd>
                        {selected.kind === "heartbeat"
                          ? `Every ${selected.heartbeat_interval_seconds ?? 0} seconds`
                          : describeSchedule(selected.cron_expression ?? "")}
                      </dd>
                    </div>
                    <div>
                      <dt>Time zone</dt>
                      <dd>{selected.time_zone}</dd>
                    </div>
                    <div>
                      <dt>Next run</dt>
                      <dd>{formatTimestamp(selected.next_run_at) || "Not scheduled"}</dd>
                    </div>
                    <div>
                      <dt>Last run</dt>
                      <dd>{formatTimestamp(selected.last_run_at) || "Never"}</dd>
                    </div>
                  </dl>

                  {selected.description && <p className="rw-instructions">{selected.description}</p>}
                  {selected.attention_reason &&
                    (selected.status === "needs_attention" ? (
                      <p className="rw-alert" role="alert">
                        Needs attention: {selected.attention_reason}
                      </p>
                    ) : (
                      // The server stores a pause reason in the same field. Only a
                      // needs_attention routine is actually asking for anything.
                      <p className="rw-help">Paused: {selected.attention_reason}</p>
                    ))}

                  <div className="rw-actions">
                    <button type="button" disabled={busy} onClick={() => void runTest(selected)}>
                      Test run
                    </button>
                    {selected.status === "enabled" ? (
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => void setLifecycle(selected, "pause", `Paused ${selected.name}.`)}
                      >
                        Pause
                      </button>
                    ) : selected.status === "needs_attention" ? (
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          void setLifecycle(selected, "resolve", `Resolved and turned on ${selected.name}.`)
                        }
                      >
                        Resolve and turn on
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="rw-primary"
                        disabled={busy}
                        onClick={() => void setLifecycle(selected, "enable", `Turned on ${selected.name}.`)}
                      >
                        Turn on
                      </button>
                    )}
                    <button type="button" disabled={busy} onClick={() => void pauseAndEdit(selected)}>
                      {selected.status === "enabled" || selected.status === "needs_attention"
                        ? "Pause and edit"
                        : "Edit"}
                    </button>
                    <button type="button" onClick={() => void showHistory(selected.id)}>
                      {historyFor === selected.id ? "Hide history" : "History"}
                    </button>
                  </div>

                  <div className="rw-project">
                    <ProjectSelector
                      apiFetch={apiFetch}
                      subjectType="routine"
                      subjectID={selected.id}
                      agentID={selected.bot_id}
                    />
                    <p className="rw-help">
                      Each run works from the project brief as it stands when that run is queued. A routine
                      can only join a project its agent belongs to.
                    </p>
                  </div>

                  {editDraft && (
                    <form
                      className="rw-edit"
                      onSubmit={(event) => {
                        event.preventDefault();
                        void saveEdit();
                      }}
                    >
                      <h4 className="rw-subtitle">Edit routine</h4>
                      <p className="rw-help">
                        Editing changes the name, the instructions and the cadence. The owning agent
                        and the approval policy stay as they are.
                      </p>
                      <div className="rw-field">
                        <label htmlFor="rw-edit-name">Name</label>
                        <input
                          id="rw-edit-name"
                          value={editDraft.name}
                          onChange={(event) => setEditDraft({ ...editDraft, name: event.target.value })}
                        />
                      </div>
                      <div className="rw-field">
                        <label htmlFor="rw-edit-instructions">Instructions</label>
                        <textarea
                          id="rw-edit-instructions"
                          rows={5}
                          value={editDraft.instructions}
                          onChange={(event) =>
                            setEditDraft({ ...editDraft, instructions: event.target.value })
                          }
                        />
                      </div>
                      {selected.kind === "heartbeat" ? (
                        <p className="rw-help">
                          This routine runs on an interval, not a calendar. Its cadence is unchanged.
                        </p>
                      ) : (
                        renderSchedulePicker(editDraft, setEditDraft, "rw-edit")
                      )}
                      <div className="rw-actions">
                        <button type="submit" className="rw-primary" disabled={busy}>
                          Save changes
                        </button>
                        <button type="button" onClick={() => setEditDraft(null)}>
                          Cancel
                        </button>
                      </div>
                    </form>
                  )}

                  {historyFor === selected.id && (
                    <div className="rw-history">
                      <h4 className="rw-subtitle">History</h4>
                      {history.length === 0 ? (
                        <p className="rw-empty">Nothing recorded yet.</p>
                      ) : (
                        <ul className="rw-events">
                          {history.map((event) => (
                            <li key={event.id}>
                              <span className="rw-event-type">{event.type}</span>
                              <span className="rw-event-time">{formatTimestamp(event.created_at)}</span>
                              {event.message && <span className="rw-event-text">{event.message}</span>}
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  )}
                </article>
              ) : (
                <p className="rw-empty">Pick a routine to see its schedule, history and controls.</p>
              )}
            </div>
          </div>

          <div className="rw-calendar">
            <div className="rw-calendar-head">
              <h3 className="rw-subtitle">
                {calendarView === "week" ? "Week" : "Next 7 days"}
                <span className="rw-count">{totalOccurrences}</span>
              </h3>
              <div className="rw-actions">
                <button
                  type="button"
                  onClick={() => setWindowStart(addDays(windowStart, -CALENDAR_DAYS))}
                >
                  Previous
                </button>
                <button type="button" onClick={() => setWindowStart(startOfDay(new Date()))}>
                  Today
                </button>
                <button
                  type="button"
                  onClick={() => setWindowStart(addDays(windowStart, CALENDAR_DAYS))}
                >
                  Next
                </button>
              </div>
            </div>

            {truncated.length > 0 && (
              <p className="rw-help">
                Showing the first {PREVIEW_LIMIT} runs for {truncated.join(", ")}.
              </p>
            )}
            {unavailable.length > 0 && (
              <p className="rw-help rw-bad">
                Could not work out when these will run: {unavailable.join(", ")}. They are missing
                from the calendar below.
              </p>
            )}

            <div aria-busy={calendarBusy}>
              {calendarView === "agenda" ? (
                <ol className="rw-agenda">
                  {days.map((day) => (
                    <li key={day.date.toISOString()}>
                      <h4 className="rw-day">{formatDayHeading(day.date)}</h4>
                      {day.items.length === 0 ? (
                        <p className="rw-empty">Nothing scheduled.</p>
                      ) : (
                        <ul className="rw-day-items">
                          {day.items.map((item) => (
                            <li key={`${item.routine.id}-${item.at.toISOString()}`}>
                              <span className="rw-when">{formatTimeOfDay(item.at)}</span>
                              <span className="rw-what">{item.routine.name}</span>
                              <span className={`rw-badge is-${item.routine.status}`}>
                                {STATUS_LABELS[item.routine.status] ?? item.routine.status}
                              </span>
                            </li>
                          ))}
                        </ul>
                      )}
                    </li>
                  ))}
                </ol>
              ) : (
                <table className="rw-week">
                  <caption className="rw-visually-hidden">
                    Scheduled runs for the week beginning {formatDayHeading(windowStart)}
                  </caption>
                  <thead>
                    <tr>
                      {days.map((day) => (
                        <th key={day.date.toISOString()} scope="col">
                          {formatDayHeading(day.date)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    <tr>
                      {days.map((day) => (
                        <td key={day.date.toISOString()} data-day={formatDayHeading(day.date)}>
                          {day.items.length === 0 ? (
                            <span className="rw-empty">—</span>
                          ) : (
                            <ul className="rw-day-items">
                              {day.items.map((item) => (
                                <li key={`${item.routine.id}-${item.at.toISOString()}`}>
                                  <span className="rw-when">{formatTimeOfDay(item.at)}</span>
                                  <span className="rw-what">{item.routine.name}</span>
                                </li>
                              ))}
                            </ul>
                          )}
                        </td>
                      ))}
                    </tr>
                  </tbody>
                </table>
              )}
            </div>
            <p className="rw-help">
              Times are shown in your local time zone. Paused and off routines appear here so you can
              plan, but they will not run.
            </p>
          </div>
        </section>
      )}

      {tab === "new" && (
        <section
          id="rw-panel-new"
          role="tabpanel"
          aria-labelledby="rw-tab-new"
          tabIndex={0}
          className="rw-panel"
        >
          <form
            className="rw-form"
            onSubmit={async (event) => {
              event.preventDefault();
              if (await createRoutine(createDraft, createAgent)) {
                setCreateDraft(emptyDraft());
                setTab("schedule");
                await load();
              }
            }}
          >
            <div className="rw-field">
              <label htmlFor="rw-new-agent">Agent</label>
              <select
                id="rw-new-agent"
                value={createAgent}
                required
                onChange={(event) => setCreateAgent(event.target.value)}
              >
                <option value="">Choose an agent</option>
                {agents.map((agent) => (
                  <option key={agent.id} value={agent.id}>
                    {agent.name}
                  </option>
                ))}
              </select>
              <p className="rw-help">The workflow runs as this agent. Nothing runs until you turn it on.</p>
            </div>

            <div className="rw-field">
              <label htmlFor="rw-new-name">Name</label>
              <input
                id="rw-new-name"
                value={createDraft.name}
                required
                onChange={(event) => setCreateDraft({ ...createDraft, name: event.target.value })}
              />
            </div>

            <div className="rw-field">
              <label htmlFor="rw-new-instructions">Instructions</label>
              <textarea
                id="rw-new-instructions"
                rows={6}
                value={createDraft.instructions}
                onChange={(event) =>
                  setCreateDraft({ ...createDraft, instructions: event.target.value })
                }
              />
            </div>

            {renderSchedulePicker(createDraft, setCreateDraft, "rw-new")}

            <div className="rw-preview">
              <h3 className="rw-subtitle">Next runs</h3>
              {createPreviewError ? (
                <p className="rw-help rw-bad">{createPreviewError}</p>
              ) : createPreview.length === 0 ? (
                <p className="rw-empty">Set a schedule to see when this would run.</p>
              ) : (
                <ul className="rw-day-items">
                  {createPreview.map((iso) => (
                    <li key={iso}>
                      <span className="rw-when">{formatTimestamp(iso)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <p className="rw-help">
              New workflows run with Grok Build as the lead and Claude as the worker, and ask before
              anything risky. Both engines have to be set up for the workflow to run. Use Advanced
              setup to pick different engines, or to change the approval policy or the retry rules.
            </p>

            <div className="rw-actions">
              <button type="submit" className="rw-primary" disabled={busy}>
                Create workflow
              </button>
              <button type="button" onClick={() => setCreateDraft(emptyDraft())}>
                Reset
              </button>
            </div>
          </form>
        </section>
      )}

      {tab === "templates" && (
        <section
          id="rw-panel-templates"
          role="tabpanel"
          aria-labelledby="rw-tab-templates"
          tabIndex={0}
          className="rw-panel"
        >
          <h3 className="rw-subtitle">Start from a template</h3>
          <ul className="rw-templates">
            {STARTER_TEMPLATES.map((template) => (
              <li key={template.name}>
                <div>
                  <strong>{template.name}</strong>
                  <span className="rw-item-meta">{describeSchedule(template.cron)}</span>
                  <p className="rw-instructions">{template.instructions}</p>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    setCreateDraft(templateToDraft(template));
                    setTab("new");
                  }}
                >
                  Use this
                </button>
              </li>
            ))}
          </ul>

          <h3 className="rw-subtitle">Export</h3>
          <p className="rw-help">
            Markdown for {exportable.templates.length} of the {visibleRoutines.length} routine(s)
            matching your filters. Names, instructions and cadences only: no agent, no routine id, no
            webhook secret and no run history.
          </p>
          {exportable.unsupported.length > 0 && (
            <p className="rw-help">
              Left out {exportable.unsupported.length}: {exportable.unsupported.join(", ")}. This
              format carries a cron schedule, so routines that run on an interval cannot be written
              to it. They keep working, and you can still edit them from the Schedule tab.
            </p>
          )}
          <textarea
            ref={exportRef}
            className="rw-mono"
            readOnly
            rows={10}
            value={exportMarkdown}
            aria-label="Exported workflow Markdown"
          />
          <div className="rw-actions">
            <button type="button" onClick={() => void copyExport()}>
              Copy
            </button>
          </div>

          <h3 className="rw-subtitle">Import</h3>
          <p className="rw-help">
            Paste exported Markdown. Imported workflows arrive turned off, and you pick the agent
            that owns them. An owner, a schedule state or a secret in the file is ignored.
          </p>
          <div className="rw-field">
            <label htmlFor="rw-import">Workflow Markdown</label>
            <textarea
              id="rw-import"
              className="rw-mono"
              rows={8}
              value={importText}
              onChange={(event) => setImportText(event.target.value)}
            />
          </div>
          <div className="rw-actions">
            <button type="button" onClick={() => setImportResult(importWorkflowMarkdown(importText))}>
              Read file
            </button>
          </div>

          {importResult && (
            <div className="rw-import-result">
              {importResult.errors.map((message, index) => (
                <p key={`error-${index}`} className="rw-help rw-bad">
                  {message}
                </p>
              ))}
              {importResult.warnings.map((message, index) => (
                <p key={`warning-${index}`} className="rw-help">
                  {message}
                </p>
              ))}
              {importResult.templates.length > 0 && (
                <>
                  <div className="rw-field">
                    <label htmlFor="rw-import-agent">Owning agent</label>
                    <select
                      id="rw-import-agent"
                      value={createAgent}
                      onChange={(event) => setCreateAgent(event.target.value)}
                    >
                      <option value="">Choose an agent</option>
                      {agents.map((agent) => (
                        <option key={agent.id} value={agent.id}>
                          {agent.name}
                        </option>
                      ))}
                    </select>
                  </div>
                  <ul className="rw-templates">
                    {/* Two workflows may share a name, so position is the key. */}
                    {importResult.templates.map((template, index) => (
                      <li key={`import-${index}`}>
                        <div>
                          <strong>{template.name}</strong>
                          <span className="rw-item-meta">{describeSchedule(template.cron)}</span>
                        </div>
                        <button
                          type="button"
                          disabled={busy || !createAgent}
                          onClick={async () => {
                            if (await createRoutine(templateToDraft(template), createAgent)) {
                              await load();
                            }
                          }}
                        >
                          Add as off
                        </button>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </div>
          )}
        </section>
      )}
    </div>
  );
}

export default RoutinesWorkspace;
