# Index Core — Next Actions

## Current phase

**Gate 1B — Core Semantics Rework**

Execution owner:

**Codex**

Architecture / acceptance owner:

**ChatGPT Architect**

Active task:

**Issue #37 — [CODEX][GATE-1B] Core Semantics Rework**

## Immediate objective

Produce one internally consistent Gate 1B architecture package that answers:

> When is an observed object the same canonical resource, when is input safe enough to affect absence/removal semantics, and how does canonical truth change deterministically without accidental identity corruption or deletion?

## Codex work package

Codex should start from current `main`, read Issue #37 and reuse correct material from legacy PR #35 only as reference.

The next clean branch is:

`architecture/gate1b-core-semantics-codex`

### Identity
Freeze:
- provider-qualified stable identity evidence
- hash as fingerprint, not identity
- conservative fallback when provider ID/hash are absent
- rename/move continuity
- same-path replacement handling
- directory-move v1 behavior
- continuity horizon vs removal safety

### Completeness
Freeze:
- Snapshot acceptance states
- provider-neutral freshness evidence
- Collector failure-visibility/completeness assurance
- destructive-safe qualification
- PARTIAL/STALE/SUSPICIOUS unknown-coverage semantics

### Reconcile / failure
Freeze:
- add/update/rename/move
- unknown coverage vs canonical missing
- removal evidence lifecycle
- conflict/unresolved behavior
- failure preservation of prior truth
- removal validation boundary without Kernel provider traversal

### Change Journal
Freeze:
- semantic events
- append-only/canonical-wins repair semantics
- distinction from provider delta and snapshot diff
- MOVE/RENAME + UPDATE semantic result

### Ordering
Freeze:
- Kernel-owned per-root accepted input ordering
- duplicate replay idempotency
- out-of-order old input behavior
- CAS as Store enforcement, not ordering policy

### Root lifecycle
Freeze:
- disjoint root ownership
- root_id immutability
- NEW / ACTIVE / DEPRECATED / DELETED or equivalent
- logical retirement and historical retention

## Required adversarial cases

Before opening the PR, Codex must verify the design against:

- identical-content copy
- same-path/same-size replacement
- stable provider ID vs unqualified provider ID
- long-gap rename/move
- directory move
- partial scan
- silent truncation suspicion
- weak error-visibility Collector
- permission-denied subtree
- stale input
- duplicate replay
- out-of-order input
- concurrent same-root inputs
- root delete/recreate
- Journal/canonical disagreement
- MOVE/RENAME + UPDATE in one accepted input

## Explicitly deferred to Gate 1C

- PostgreSQL tables/indexes/constraints/migrations
- exact transaction implementation
- exact Query API shape
- journal physical persistence/event schema
- final Collector ADR / selection

## Explicitly deferred post-MVP

### Scanner Resume
- durable scanner
- checkpoint
- resume
- bounded concurrency

### Incremental
- native delta
- provider cursor
- dirty scope
- incremental hints

## Gate 1B exit condition

ChatGPT Architect must be able to answer:

1. What evidence can establish canonical identity?
2. What evidence can never establish identity by itself?
3. When does rename/move preserve identity?
4. When must matching remain UNRESOLVED/CONFLICT?
5. What input can authorize canonical absence/removal?
6. How is incomplete coverage prevented from changing prior truth?
7. What makes removal confirmation independent and safe?
8. What semantic events enter Change Journal?
9. How are duplicate/concurrent/out-of-order inputs deterministic?
10. How are roots retired/recreated without identity reuse?

Only after Architect ACCEPT may Gate 1C begin.
