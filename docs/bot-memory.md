# Bot memory

## Status: implemented local MVP

OpenAgentFleet keeps a small, explicit memory per Bot. It is not an automatic
transcript scraper and it is not a hidden model profile.

The current implementation is local SQLite storage, a reviewable macOS
Settings panel, and a bounded snapshot added to a run when it is queued.

## What can be remembered

Each entry belongs to exactly one Bot and has:

- a category: `fact`, `preference`, `instruction`, or `project`;
- a priority from 1 to 5;
- optional RFC3339 expiry;
- immutable source (`user` or reserved `agent_proposal`); and
- a status (`approved` or `archived`).

The current UI creates only `user` + `approved` records. The `agent_proposal`
source is reserved for a future proposal/review flow; no current harness can
write it through the local API.

Archiving removes an entry from future agent context but retains it for review
and restoration. Permanent deletion requires a separate confirmation in the
UI and deletes that row from SQLite.

## Context behavior

When the user queues a run, `botd` reads only approved, non-expired entries for
that Bot. It snapshots at most 20 records and 12 KiB of entry content into a
data-shaped context block before the current user task. The original chat
message remains unchanged in message storage.

The memory block explicitly says that it is contextual information and cannot
override the current task, approval policy, or higher-priority instructions.
This snapshot makes a queued run reproducible even if the memory is edited
later.

There is deliberately no embedding store or semantic retrieval in this MVP.
Selection is deterministic: priority, then newest update, then a stable ID
tiebreak. Semantic search will need its own privacy, model, retention, and
evaluation contract before it is enabled.

## Safety boundary

The store rejects likely secrets before they reach SQLite, including common API
token formats, bearer headers, private keys, JWTs, passwords, PINs, credential
URLs, and environment-variable assignments. This is a guardrail, not a claim
that it can detect every secret or that a user should paste credentials into a
memory editor.

Memory is included only in the target Bot's queued prompt. It is never copied
into another Bot's list or context, and archived/expired records are excluded
from retrieval. The normal API remains protected by the local app token (and
the remote-mobile API does not expose memory administration).

## Local API

All endpoints are on the authenticated local `botd` API:

| Method | Endpoint | Meaning |
| --- | --- | --- |
| `GET` | `/api/memories?bot_id=…` | List every reviewable record for one Bot, including archived and expired records. |
| `POST` | `/api/memories` | Add an explicit user-approved record. |
| `PATCH` | `/api/memories/{id}?bot_id=…` | Edit content, category, priority, expiry, or archive state. |
| `DELETE` | `/api/memories/{id}?bot_id=…` | Permanently delete after an explicit UI confirmation. |
| `GET` | `/api/bootstrap` | Includes the selected conversation Bot's reviewable memory list. |

The caller must supply `bot_id` for every scoped operation; a memory ID alone
is deliberately insufficient.

## Follow-up work

1. Add a user-reviewed pending state for agent-proposed memories.
2. Add Bot/project scopes and source links such as files, conversations, and
   citations.
3. Add full-text recall with visible match reasons before considering embeddings.
4. Add a per-run “why this was remembered” inspector and context diff.
5. Add retention/export controls and a true hard-delete audit policy.
