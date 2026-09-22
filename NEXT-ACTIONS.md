# Index Core — Next Actions

> Operational near-term queue only.

## Current phase

**Gate 1 — Architecture**

No implementation is authorized yet.

## Immediate objective

Freeze the smallest safe IndexCore contracts using D01-D03 evidence.

Do not choose a Collector by momentum.

Do not write product code.

## Gate 1 design questions

### A. Domain / Identity

Define:

- ResourceRoot
- Snapshot
- SnapshotEntry
- CanonicalResource
- ChangeRecord

Specify Stable Identity v1 when:

- provider_object_id exists
- provider_object_id is absent
- path changes
- hash is absent
- rename/move is ambiguous

### B. Snapshot / Completeness

Define:

- snapshot lifecycle
- completeness evidence
- failure / skipped-path representation
- cache/freshness evidence
- conditions under which destructive reconcile is forbidden

### C. Collector boundary

Define a provider-neutral Collector/Input Contract.

Then evaluate:

- AList/OpenList adapter
- rclone adapter
- direct provider adapter only where necessary

Selection must be based on contract fit, not feature count.

### D. Store / PostgreSQL

Define:

- Store Interface
- transaction boundary
- atomic root reconcile guarantee
- generation / version semantics
- previous-truth preservation on failure

PostgreSQL is the first persistence implementation.

Domain types must not depend on PostgreSQL schema.

### E. Reconcile / Safety

Define:

- add
- update
- rename
- move
- missing
- removal candidate
- confirmed removed
- conflict

Missing must never equal deleted automatically.

### F. Change Journal

Define Canonical Change Journal separately from:

- provider-native delta
- polling
- snapshot diff
- search index updates

### G. Query Contract

Define read-only consumer access for:

- CloudSite
- Search
- Catalog
- future consumers

Consumers must not redefine canonical truth.

## Gate 1 deliverables

At minimum:

```text
docs/architecture/
  DOMAIN-MODEL.md
  SNAPSHOT-CONTRACT.md
  COLLECTOR-CONTRACT.md
  STORE-CONTRACT.md
  QUERY-CONTRACT.md
  FAILURE-MODEL.md
  IDENTITY-V1.md
  SAFE-RECONCILE.md
  CHANGE-JOURNAL.md

docs/decisions/
  ADR-001-COLLECTOR-BOUNDARY.md
  ADR-002-POSTGRESQL-STORE.md
```

File names may be adjusted by Architect, but responsibilities must remain explicit.

## Current prohibited actions

Until Gate 1 is accepted:

- no product code
- no migrations
- no PoC
- no CloudSite integration
- no UI
- no broad donor research
- no final Collector implementation
- no forced content hashing
- no assumption that rclone/AList is already selected

## Gate 1 exit condition

Architect must be able to answer:

1. What exactly is IndexCore responsible for?
2. What exactly is a Collector responsible for?
3. What evidence makes a Snapshot acceptable for destructive reconcile?
4. How is identity preserved when provider ID is missing?
5. What PostgreSQL transaction boundary preserves previous truth?
6. What can consumers read, and what can they never mutate?
7. Which parts are MVP and which are deferred?

Only then may Gate 2 PoC begin.
