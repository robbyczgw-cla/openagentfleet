# Optional Source-Backed Research Run Core

`research` is a side-effect-free data model for a future, user-controlled
Research Run. It does not fetch URLs, start a browser, call a model, execute a
tool, write artifacts, or resolve DNS.

## Safe defaults and toggles

The zero-value `Policy` denies research execution. Queuing needs
`ExperimentalResearchEnabled`; starting a plan that requested network access
also needs `NetworkFetchEnabled`. Both flags are intended for explicit settings
toggles. The core merely records state transitions for a future executor.

## Evidence contract

- Every claim has one or more citations, each bound to exactly one source.
- `verified` claims are directly supported and cannot have an inference basis.
- `inference` claims still require citations and a concise reasoning basis.
- Artifacts bind to claims and optionally sources, have a managed
  `artifact://run-id/artifact-id` URI, and carry a SHA-256 digest.
- A run cannot complete without claims, citations, and valid cross-references.

## Boundaries for a future executor

`WorkPlan` caps sources, claims, artifacts, duration, and whether network access
was requested. URL validation permits only public HTTPS syntax and rejects
direct private/loopback IP literals. A real fetch layer must additionally apply
DNS-aware egress restrictions, redirect limits, content-type/size limits,
robots/terms policy, and citation extraction safeguards.

The caller supplies IDs and persistence. Keep raw fetched content outside this
domain record, retain source hashes, and never place credentials or unreviewed
model output in source metadata.
