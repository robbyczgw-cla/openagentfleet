# Workspace productivity

Three panels sit in the workspace column beside the chat: Tasks & results,
Routines, and Connected apps. On a narrow window, open that column with the ▦
button in the header. Esc closes any of the three.

## Tasks and results

A task is one Agent run. It keeps the brief you sent, the answer that run
saved, and the files the run linked. One chat thread usually holds several
tasks.

The panel lists tasks from every Agent, newest first, so you do not have to
remember which chat a run happened in.

### Finding a task

- Search matches the original brief or the title supplied by a routine.
- Agent narrows the list to one Agent.
- Status filters to Queued, Running, Needs me, Completed, Failed, Blocked or
  Stopped. Needs me means the run waits for a pending approval.
- The list holds 50 tasks and reloads every 5 seconds. Load older tasks pages
  further back; past the first page, use Refresh instead of waiting.

### Reading one task

The detail pane shows the brief, the Agent, the engine that ran it, and the
answer that run saved. Later messages in the same conversation do not
overwrite that answer. A failed run shows its error above the result.

Four actions, depending on status:

- Open conversation jumps to the thread the task ran in.
- Review approval appears while the task waits on you.
- Stop task appears while it is queued, running, or waiting for approval.
- Save as workflow appears on a completed task. It opens the Routines panel
  with the task prefilled, which turns a one-off into a schedule.

### Files

Agents are asked to save deliverables under `outputs/<run-id>/` and to link
each one in the final answer. When the run finishes, the controller copies
eligible linked files from `outputs/` into the database. Links outside that
folder and symlinks are skipped. Each saved copy is immutable. A later run that
rewrites the source file does not change what this task shows.

Text files and PNG, JPEG, GIF and WebP images preview in place. Everything
else downloads. A text preview stops at 100 KB and says so. Download always
gives you the whole file.

Per run: at most 20 files, 10 MiB each, and at most 100 links read out of the
answer. A file over the size limit is skipped, not truncated.

Links pointing straight at `outputs/` still work, for Agents that were not
told about the per-run folder. The snapshot then proves what the file
contained when the run finished, but not which run wrote it. Two runs writing
the same shared path at the same time cannot be told apart. Use
`outputs/<run-id>/` when that distinction matters.

### What the panel does not show

- Runs that finished before this feature shipped have no saved result. They
  still appear in the list and say so. There is no backfill.
- Group chat runs stay in the group chat. This panel shows
  an Agent's individual tasks.

## Routines and workflows

A routine is an Agent-owned instruction with a schedule and an on, paused or
off state. Turn Routines on in Settings first, otherwise the button stays
disabled.

### Schedule tab

The left column lists routines, filtered by Agent and by status (On, Paused,
Off, Needs attention). Select one to see its schedule, time zone, next run,
last run and instructions.

- Test run starts it once, now. If the Agent needs approval, the test waits in
  Review.
- Pause stops future occurrences and keeps the definition.
- Turn on schedules it. Resolve and turn on clears a needs-attention state.
- Pause and edit opens the editor, pausing first if the routine is on.
- History lists what the scheduler recorded for that routine.

Editing is deliberately narrow. The server refuses to edit a routine that is
on, because the scheduler can claim it between the read and the write, so the
button pauses it first. An edit changes the name, the instructions and the
cadence. The owning Agent, the engine pair, the approval policy and the retry
rules stay as they were, so an edit can never widen what a routine may do.
Saving clears the computed next run, and the next time you turn the routine on
it is recomputed from the new schedule.

A paused routine that still owns a running occurrence cannot be edited either.
Wait for that occurrence to finish.

The right column is a calendar of the next 7 days, shown as an agenda or a
week grid. Previous, Today and Next move the window. Times display in your
local zone, not the routine's. Paused and off routines appear so you can plan
around them, but they do not run. Each routine contributes at most 32
occurrences to the window, and the panel says which ones it cut.

### New workflow tab

Choose the Agent, name the workflow, write the instructions, then set the
schedule: Every day, Every weekday, or Every week with a weekday, plus a time.
Anything the picker cannot express goes in the cron field as five fields. The
time zone box completes from your system's zone list.

Next runs shows the next five firing times, computed by the server, before you
create anything. Check it. A cron expression that reads correctly and fires at
the wrong hour is the usual mistake.

New workflows use Grok as lead and Claude as worker, and ask before risky actions.
Advanced setup opens the older form for the engine pair, the approval policy
and the retry rules.

Limits: 160 characters for the name, 4096 bytes for the instructions, 256
bytes for the cron expression.

### Templates tab

Three starters ship with the app: morning standup notes, weekly dependency
check, nightly test triage. Use this copies one into the New workflow tab.

Export renders the cron routines matching your current filters as Markdown: name,
schedule in words, cron expression, time zone, instructions. Nothing else
travels. No Agent, no routine id, no webhook secret, no run history.
Heartbeat routines retain their interval controls but are excluded from this
export; the panel identifies them.

Import reads that Markdown back. Paste it, press Read file, choose the owning
Agent, then press Add as off for each workflow you want. Imported workflows
arrive turned off and never assign themselves an Agent, so a file from someone
else cannot start running the moment you open it. You turn each one on
yourself from the Schedule tab. Bullets the importer does not recognize are
reported as ignored rather than applied.

Instructions survive the round trip byte for byte, including their own `##`
headings and fenced code blocks. The exporter wraps a body that would
otherwise be misread in a fence longer than any backtick run inside it.
Control characters in an imported file are stripped; tabs and newlines
survive.

A minimal file the importer accepts:

```markdown
# OpenAgentFleet workflows

## Weekly dependency check

- Cron: `0 9 * * 1`
- Time zone: Europe/Berlin

### Instructions

Check the project's dependencies for new releases and known advisories.
Report only the ones that need action.
```

## Connected apps

Connected apps currently means GitHub, and GitHub means reading issues and
pull requests.

There is no OAuth service and no token in the app's database. The panel uses
the GitHub CLI already signed in on this computer. Install `gh`, run
`gh auth login`, then press Check again.

Setup has two grants, and both are explicit:

1. Repositories. The panel lists up to 100 of your most recently updated
   repositories. Tick the ones Agents may read. Anything missing can be typed
   in as `owner/name`.
2. Agents. Tick the Agents that receive the GitHub tools. An Agent led by Pi
   cannot be selected, because Pi does not support MCP tools.

Enable connection pins the login you are signed in as. If the account later
changes, the panel reports the connection as not authenticated and the tools
refuse to run until you reconnect. Disconnect clears the repository list and
every Agent grant.

A granted Agent gets four tools: `github_list_issues`, `github_get_issue`,
`github_list_pull_requests`, `github_get_pull_request`. Lists return up to 30
open items. Every call re-checks that the connection is still on, that this
Agent is still granted, that the repository is still allowed, and that the
login has not changed, so revoking access takes effect on the next call.

Limits worth knowing:

- Read only. No comments, no labels, no branches, no merges.
- Only open issues and pull requests are listed. Fetch a closed one by number.
- github.com only. The host is fixed and cannot be pointed elsewhere.
- A GitHub response over 2 MiB, or one that takes longer than 20 seconds, is
  rejected.
- Repository content is untrusted input. An issue body can contain
  instructions aimed at your Agent; treat what comes back as data, not orders.
