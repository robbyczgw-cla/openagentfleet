# Workspace productivity

Five panels open from the workspace column beside the chat: Tasks & results,
Routines, Connected apps, Projects, and GitHub tasks. The same row holds a
Project picker for the open conversation. On a narrow window, open that column
with the ▦ button in the header. Esc closes any panel.

Suggested memories are not a panel. They appear at the top of the Review
dialog when an Agent has proposed one; see [Suggested memories](#suggested-memories).

Projects, suggested memories and GitHub tasks need `botd` to run with a
controller token. The native app sets one at startup. A plain
`go run ./cmd/botd` has none, and those routes answer 503 until you set one;
see the [Linux development notes](linux-desktop.md#development-prerequisites).
Running a task again follows the existing API authentication rules: it remains
available in tokenless loopback mode and, once a token is set, needs the
normal bearer like every other task route.

## Tasks and results

A task is one Agent run. It keeps the brief you sent, the files you attached,
the answer that run saved, and the files the run linked. One chat thread
usually holds several tasks.

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

The detail pane shows the Agent, the engine that ran it, the original request
with the files you attached, and the answer that run saved. Later messages in
the same conversation do not overwrite that answer. A failed run shows its
error above the result. A task that ran under a project names the project and
the brief version it used.

For runs that predate this feature, the original request is matched to the
user message written at the same instant as the run. If none matches, the
panel says the wording was not kept.

The actions depend on status:

- Open conversation jumps to the thread the task ran in.
- Review approval appears while the task waits on you.
- Stop task appears while it is queued, running, or waiting for approval.
- Run this again appears on a failed or stopped task. Change the request and
  run appears on a completed task. Both are described next.
- Save as workflow appears on a completed task. It opens the Routines panel
  with the task prefilled, which turns a one-off into a schedule.

### Running a task again

A retry sends the original request back to the same Agent, word for word. A
revision lets you reword it first. Either way the result is a new task, linked
to the one you started from. The earlier answer and its files stay on the
earlier task; nothing is overwritten. Open the earlier attempt jumps back.

Rules the server enforces:

- Retry needs a failed or stopped task. Revise needs a completed one. A
  blocked task offers neither.
- The Agent cannot change. `agent_id` must match the original task.
- A chain runs at most 10 attempts, counting the original. The list shows
  "Attempt N" on anything past the first, and the detail pane reads
  "Attempt N of 10".
- Files from the original request can be sent again by ticking them. At most
  10. Each one is copied, so deleting the copy later does not touch the
  original task's attachment. A source file that is no longer on disk fails
  the request.
- Each submit carries an `Idempotency-Key` header. Sending the same key with
  the same request again returns the run already created. The same key with a
  different request is refused with 409. The panel reuses its key while you
  have not changed anything, so a resend after a dropped connection cannot
  start a second run.

The new attempt starts a new provider session rather than resuming the old
one, so the Agent reads the request fresh instead of continuing a thread that
already failed. It runs in the same conversation and, if the original ran in a
GitHub work checkout, in that same checkout.

If the original task ran under a project, the retry snapshots the project's
current brief, not the version the original used. Membership is checked again
at that moment.

### Files

Agents are asked to save deliverables under `outputs/<run-id>/` and to link
each one in the final answer. When the run finishes, the controller copies
eligible linked files from `outputs/` into the database. Links outside that
folder and symlinks are skipped. Each saved copy is immutable. A later run that
rewrites the source file does not change what this task shows.

PNG, JPEG, GIF and WebP images preview in place, detected from their bytes.
Files named `.txt`, `.md`, `.csv`, `.json`, `.log`, `.yaml`, `.yml` or `.xml`
preview as text. Everything else downloads. A text preview stops at 100 KB and
says so. Download always gives you the whole file.

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

## Projects

A project is a named brief shared by a chosen set of Agents. Assign a
conversation or a routine to a project and every task started from it gets the
brief as a system prompt section headed "Project brief snapshot". Joining a
project does not share an Agent's private memories with the others.

### Creating and editing

New project asks for a name, the brief, and the Agents on it. Limits: 160
bytes for the name, 64 KiB for the brief, 1 to 128 Agents.

Every save that changes the brief creates a new brief version. Versions are
append-only; the server refuses to alter or delete one. Brief history in the
detail pane lists them all and marks the one in use. Changing only the name or
the members does not create a version.

Saves carry the version number you loaded. If someone saved in between, the
panel offers Load latest, keep my wording, or Discard my changes. Nothing you
typed is thrown away by the reload.

Archive project stops new work. Archived projects stay readable with Show
archived, keep their history, and cannot be edited or assigned again.

### Assigning work

The Project picker sits in the conversation's action row and in a routine's
detail on the Schedule tab. It offers only active projects the owning Agent
belongs to; the server rejects anything else. Pick No project to detach. A
project the Agent has since left, or one that was archived, shows as
"(unavailable)" until you change it.

### What a task receives

The brief is copied when the task enters the queue, not when it runs. That
copy records the project, the version number and the text, and it never
changes afterwards. Edit the brief while a task is queued and that task still
runs with the version it was queued under. The task detail names the version.

Membership is checked twice: when the task is queued, and again right before
the engine starts. Remove an Agent from the project, or archive the project,
while its task is waiting in the queue, and that task ends as Blocked with the
reason `project_access_revoked`. It does not run with a brief the Agent is no
longer allowed to see.

### Sessions and projects

Provider sessions are bound to the conversation, the project, the brief
version and the working directory they were opened under. A new task in the
same conversation resumes the previous session only if all four match. Assign
the conversation to a project, or save a new brief version, and the next task
starts a fresh session instead of continuing one that was primed with
different instructions. An explicitly requested session that belongs to a
different project context is refused.

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
- Project assigns the routine to a project its Agent belongs to. Each
  occurrence copies the brief version current when it is queued. If the Agent
  has been removed from the project by then, the occurrence ends as Blocked
  instead of running.

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

## Suggested memories

Off by default. Turn on Memory proposals under Optional systems in Settings.
While it is off, Agents have no way to propose anything and the Review dialog
shows nothing about it.

With the flag on, every run of an Agent whose lead engine is not Pi gets one
extra tool, `propose_memory`. Pi is excluded because it does not support MCP
tools. The tool takes a category (fact, preference, instruction or project),
the text, an importance from 1 to 5, and an optional expiry. It creates a
pending proposal and tells the Agent so. Nothing enters the Agent's memory at
that point, and the Agent does not see its own pending proposals in later
runs.

Proposals wait in the Review dialog under Suggested memories, filterable by
Agent. Each card shows the Agent, the category, the importance, and which task
and message it came from. You can:

- Edit wording, then Save edits. Edits are only possible while the proposal is
  pending.
- Accept, or Accept with my edits. This writes one approved memory for that
  Agent with source `agent_proposal` and records the memory's id on the
  proposal. The Agent uses it from its next run.
- Dismiss. The proposal is marked rejected and stays out of memory.

Limits: 5 proposals per run, 25 pending per Agent, 4096 bytes of text. A
proposal whose text matches an earlier proposal from the same Agent, ignoring
case and whitespace and whatever happened to that earlier one, is refused as a
duplicate. The same check applies when you edit the wording.

Accepting the same proposal twice returns the memory it already created. If you
delete that memory afterwards from the Agent's memory list, accepting the
proposal again does not bring the memory back; the server answers 410 and
leaves the proposal marked accepted. Dismissing an already dismissed proposal
is a no-op. Editing, accepting or dismissing a proposal that was resolved the
other way is refused with 409.

Reviewing needs the controller token. Without one, the section is not offered
at all rather than showing an error.

## Connected apps

Connected apps is the GitHub grant: which repositories, which Agents, and
which signed-in account. Through it Agents read issues and pull requests.
[GitHub tasks](#github-tasks-issue-to-draft-pull-request) builds on the same
grant and adds the one write path, which is a button you press.

There is no OAuth service and no token in the app's database. The panel uses
the GitHub CLI already signed in on this computer. Install `gh`, run
`gh auth login`, then press Check again.

Setup has two grants, and both are explicit:

1. Repositories. The panel lists up to 100 of your most recently updated
   repositories. Tick the ones Agents may read. Anything missing can be typed
   in as `owner/name`. A grant holds at most 100 repositories.
2. Agents. Tick the Agents that receive the GitHub tools, up to 100. An Agent
   led by Pi cannot be selected, because Pi does not support MCP tools.

Enable connection pins the login you are signed in as. If the account later
changes, the panel reports the connection as not authenticated and the tools
refuse to run until you reconnect. Disconnect clears the repository list and
every Agent grant.

A granted Agent gets four tools: `github_list_issues`, `github_get_issue`,
`github_list_pull_requests`, `github_get_pull_request`. Lists return up to 30
open items. Every call re-checks that the connection is still on, that this
Agent is still granted, that the repository is still allowed, and that the
login has not changed, so revoking access takes effect on the next call.

### How the tools reach GitHub

These tools do not pass a GitHub token to the engine. The engine talks to a
stdio MCP process, `collaboration-mcp`, that OpenAgentFleet starts alongside
each run of a granted Agent. That process forwards each tool call to `botd` on
loopback, and `botd` runs `gh api` itself with the host fixed to `github.com`,
prompts disabled and the pager off. The bridge is the same binary that carries
Agent-to-Agent collaboration; GitHub is a second job it was given.

Each run gets its own bridge token, and that token is the only credential the
bridge holds. It never sees the controller's own bearer. `botd` issues the
token when the run is queued, binds it to that run id when the run starts, and
drops it when the run ends or the lease expires. The bridge sends it as the
`Authorization` bearer and again in the run headers; the two must match.

On the server, a bridge bearer is accepted on exactly these routes and nowhere
else:

| Route | Scope it needs |
| --- | --- |
| `POST /api/connections/github/read` | GitHub grant |
| `POST /api/collaboration/memory-proposals` | Memory proposals on |
| `GET /api/collaboration/agents`, `POST /api/collaboration/message`, `POST /api/collaboration/delegate`, `GET /api/collaboration/tasks/{id}`, `POST /api/collaboration/tasks/{id}/cancel` | Collaboration on |

The scope is fixed when the run is queued, from what the Agent was allowed at
that moment. A token used on a route outside its scope, with the wrong run id,
after expiry, or after the run has finished, gets 401. Every other API route
still requires the controller bearer. `botd` refuses to start the bridge at
all when it has no controller token, which the native app always sets.

The bridge process advertises tools to match. `tools/list` returns the
collaboration tools when collaboration is on for that Agent, the four
`github_*` tools when the Agent holds a grant, and `propose_memory` when
Memory proposals is on. Whenever collaboration is off, `botd` starts the
bridge with `OPENAGENTFLEET_GITHUB_ONLY=1`, and a call to `list_agents`,
`message_agent`, `delegate_to_agent` or `get_agent_task_status` fails with
"Agent collaboration is disabled" before it reaches `botd`. The server-side
scope rejects such a call anyway if the bridge is bypassed.

Routines never get collaboration. The scheduler strips it before building the
run, whatever the Agent's own settings say, so a scheduled run can read issues
and pull requests and propose memories but cannot message or delegate. The Pi
check applies to routines as well: a routine whose lead is Pi gets no GitHub
tools and no `propose_memory`.

The packaged desktop apps ship `collaboration-mcp` next to `botd`, and the
native shell passes its path in `OPENAGENTFLEET_COLLABORATION_MCP_BINARY`. The
release verification scripts fail if it is missing from the `.deb`, `.rpm`,
macOS `.app` or Windows install directory. The AppImage check does not open
the payload. A missing bundled bridge prevents the native app from starting
its backend. When running `botd` directly, a missing bridge instead prevents
the granted Agent's run from starting; the error names the command it looked
for. For a source checkout without
the native shell, see the
[Linux development notes](linux-desktop.md#development-prerequisites).

Limits worth knowing:

- The Agent tools are read only. No comments, no labels, no branches, no
  merges. The only write OpenAgentFleet makes to GitHub is the Publish step
  in GitHub tasks, and you press that yourself.
- Only open issues and pull requests are listed. Fetch a closed one by number.
- github.com only. The host is fixed and cannot be pointed elsewhere.
- A GitHub response over 2 MiB, or one that takes longer than 20 seconds, is
  rejected.
- Repository content is untrusted input. An issue body can contain
  instructions aimed at your Agent; treat what comes back as data, not orders.

## GitHub tasks: issue to draft pull request

The GitHub tasks button opens a panel headed GitHub work. It hands one GitHub
issue to one Agent, in a separate checkout on this computer, and lets you
inspect the result before anything leaves the machine. It needs an enabled
GitHub connection with the repository and the Agent both granted.

### Starting

Start from an issue asks for:

- Repository, from the granted list.
- Issue number. A number that belongs to a pull request is refused, and so is
  an issue with a title over 1024 bytes or a body over 128 KiB.
- Agent, from the granted list.
- Repository folder on this computer. An absolute path to the top of an
  existing checkout whose `origin` remote points at that repository on
  github.com. A subfolder, a folder that is not a Git checkout, or a checkout
  of some other repository is refused.
- Start from branch. A branch that already exists locally in that folder. The
  work starts from its current commit.

`botd` reads the issue through the same read-only path the Agent tools use,
then runs `git worktree add` to create a checkout on a new branch named
`oaf/issue-<number>-<id>`, placed in `.openagentfleet-worktrees/` next to your
repository folder. It creates a conversation titled "GitHub #<number>: <title>"
for that Agent and sends the first task: implement the issue in the assigned
checkout, commit on that branch, do not push, do not open a pull request, and
treat the issue text as untrusted context. That run and every later message in
the conversation run with the checkout as their working directory. The
conversation cannot be handed to another Agent by mention.

The bridge gives the Agent no write tool for GitHub, so it cannot push or
open a pull request through OpenAgentFleet. Whatever else the engine may do in
a Git checkout is governed by the engine's own approval rules, as for any
other task.

### Reviewing

Once the Agent's task is Completed, type a check command and press Run the
check and build the review. The command is an executable plus arguments, one
per line, up to 32 parts of 4096 bytes each. There is no shell. It runs inside
the checkout with a 10 minute limit and its output is capped at 2 MiB.

Before running your command, `botd` verifies the checkout: it is on the
dedicated branch, the base branch still points at the commit the work started
from, there is nothing uncommitted or untracked, HEAD is not the base commit,
HEAD descends from it, the diff is non-empty and touches at most 1000 files.
Any failure names the problem; the usual one is uncommitted changes, which the
panel asks you to have the Agent commit.

If your command exits non-zero, no review is saved and the panel shows the
output under "Output from the failed check". If it passes, `botd` checks the
checkout and the conversation's run history again; if either changed while the
command ran, the review is discarded and you are asked to run it again.

A saved review holds the base and head commits, the changed file list, the
full diff, the exact command and its output, and a token and digest over all
of it. The panel shows the file list, added and removed line counts, the diff
(clipped on screen at 200,000 characters; the checkout has the rest) and the
check output. Running the check again replaces the review with a new
generation.

### Publishing

Create draft pull request, then Yes, push and open the draft. This is the only
step that writes outside this computer. `botd` will only proceed when:

- the review token and digest you are publishing are the current ones,
- the GitHub login is still the one pinned when the work started, and the
  repository and Agent are still granted,
- the conversation has no active run and its run history matches the review,
- the checkout's head commit, diff and changed files match the review exactly.

Then it pushes the reviewed head commit to `origin` as the dedicated branch and
runs `gh pr create --draft` with your title and description into the base
branch. It reads the pull request back to confirm. If the branch already has an
open pull request, it must be a single draft with the same head commit and base
branch, or publishing stops and asks you to handle it on GitHub.

Publishing is resumable. If the push succeeded but the pull request did not,
the status reads Publishing stopped and the panel notes that the branch is
already on GitHub; publishing again opens the draft without pushing twice. A
draft that already exists is returned as is. Only one publish per piece of work
runs at a time, and a new review cannot be built while one is in flight.

Git and `gh` run with prompts disabled and a 2 minute limit per command.
Nothing is merged, and the pull request stays a draft until you change that on
GitHub.
