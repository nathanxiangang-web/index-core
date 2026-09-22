# Index Core — Project State

> This file is the concise current-state truth.
> Keep it short enough for fast AI/session recovery.
> Historical detail belongs in research reports and ADRs.

## Last updated

2026-09-23

## Main branch

Latest accepted baseline:

`74ae0ab2598ea7e86e4dfbc16fb9b6ec7dc7ea35`

Accepted content includes:

- project blueprint v0.1
- context governance
- PostgreSQL-first persistence decision
- Discovery 01 Xiaoya research
- Discovery 02 AList/OpenList collector research
- Discovery 03 rclone/fsspec collector gap comparison

## Current phase

**Gate 1 — Architecture**

Status:

**READY TO START / NOT YET FROZEN**

Discovery is closed.

No product implementation is authorized yet.

## Latest accepted phase

**Discovery 03 — Collector Gap Comparison**

Status:

**ACCEPTED**

Accepted consolidated report:

`docs/research/D03-COLLECTOR-GAP-COMPARISON.md`

## Accepted D03 comparison findings

1. rclone has the richest general Collector capability of the investigated options.
2. rclone identity remains DRIVER_DEPENDENT; `IDer` is optional and absent on important backends such as local/WebDAV/S3.
3. rclone completeness is PARTIAL:
   - successful traversal is a contract-level success signal
   - explicit traversal errors propagate and can mark a Snapshot incomplete
   - silent backend truncation still cannot be disproven
4. fsspec does not provide a general per-object stable identity abstraction.
5. fsspec does not show a material architectural advantage for the current project.
6. AList remains a viable Provider aggregation / Snapshot-source candidate:
   - provider `id` is DRIVER_DEPENDENT
   - `hash_info` is DRIVER_DEPENDENT
   - error/completeness semantics are weaker than rclone's
7. OpenList public FS API does not expose provider `id`.
8. Broad donor discovery should stop. Remaining unknowns are implementation/integration validation items, not blockers for first architecture contracts.

## Important non-decision

D03 does **not** select the final Collector.

Gate 1 must compare and assign responsibilities without prematurely choosing:

- AList/OpenList
- rclone
- direct provider adapters
- combinations of the above

## Capability ownership candidates

### Kernel safety semantic candidates

- canonical resource identity decision / continuity rules
- Snapshot completeness acceptance / Safety Gate
- Canonical Inventory
- Safe Reconcile / removal safety
- Change Journal

### Collector / Scanner responsibility candidates

- traversal / pagination
- provider error capture
- skipped-path evidence
- cache bypass / refresh policy
- scan checkpoint / resume

### Optional capabilities

- provider_object_id
- hash / content hash
- native delta / change notify
- provider metadata

Final ownership is a Gate 1 architecture decision.

## Accepted persistence decision

**PostgreSQL-first**

- PoC inventory uses PostgreSQL
- MVP canonical inventory uses PostgreSQL
- there is no SQLite-first formal implementation stage
- final schema is still deferred until Gate 1
- Kernel remains dependent on Store Interface rather than PostgreSQL-specific Domain types

## Gate 1 must define

1. Domain Model
2. Snapshot Contract
3. Collector / Input Contract
4. Store Contract
5. Query Contract
6. Failure Model
7. Stable Identity v1 semantics
8. Completeness / Safe Reconcile semantics
9. PostgreSQL persistence boundaries and transaction guarantees
10. Collector selection criteria and adapter boundary
11. Change Journal semantics distinct from provider-native delta
12. responsibilities for checkpoint / skipped-path evidence / cache policy

## Implementation status

**NO PRODUCT CODE**

Do not create yet:

- formal kernel implementation
- provider adapters
- migrations
- production APIs
- UI
- CloudSite integration

## Current project risks

- selecting a Collector before contracts are defined
- treating provider ID/path/hash as universally stable
- treating `err == nil` as proof against silent backend truncation
- pushing Scanner mechanics into the Kernel
- making optional hash/native-delta mandatory
- confusing provider-native delta with Canonical Change Journal
- letting PostgreSQL schema become the Domain model
- reopening broad donor research without a concrete blocker

## State maintenance rule

Accepted project state lives in Git.

Architecture decisions must be explicit and reviewable.

Workers may investigate assigned design questions, but cannot freeze architecture without Architect review.
