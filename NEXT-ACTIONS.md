# Index Core — Next Actions

## Current phase

**Gate 1B — Core Semantics**

No product code.

## Immediate objective

Turn the Gate 1A boundaries into deterministic Domain and safety semantics.

Gate 1B must answer:

> When is an observed object the same canonical resource, when is a Snapshot safe enough to reconcile, and how does canonical truth change without accidental deletion or identity corruption?

## Workstreams

### Worker A — Domain + Stable Identity v1

Define:

- ResourceRoot
- Snapshot
- SnapshotEntry
- CanonicalResource
- canonical Generation
- identity evidence model
- Stable Identity v1 rules
- ambiguous identity -> conflict/unresolved

Must handle:

- provider_object_id present / absent
- path change
- rename / move
- hash absent
- same-name/same-size collisions
- directory identity
- root scoping

Do not require hash or provider ID.

### Worker B — Snapshot + Completeness Acceptance

Define:

- Snapshot lifecycle
- traversal evidence
- error / skipped-scope / freshness evidence
- completeness acceptance states
- destructive-reconcile eligibility

Hard rule:

`traversal_status=success` is evidence, not proof of provider completeness.

Completeness heuristics may inspect only submitted evidence + prior committed canonical state.

### Worker C — Safe Reconcile + Failure Model + Change Journal semantics

Define:

- add
- update
- rename
- move
- missing
- removal candidate
- confirmed removed
- conflict
- rejected/incomplete input
- canonical Change Journal semantic events

Must guarantee:

- missing != deleted
- incomplete Snapshot cannot authorize destructive removal
- failed reconcile leaves previous canonical truth intact

Do not design PostgreSQL transaction implementation yet.

### Worker D — Adversarial state-machine review

Attack A/B/C with scenarios:

- file rename
- file move
- directory rename/move
- same path reused by a different object
- provider ID disappears
- provider ID changes unexpectedly
- duplicate name/size/mtime
- partial scan
- silent truncation suspicion
- permission-denied subtree
- stale cache
- concurrent snapshots for same root
- overlapping roots
- retry of same Snapshot
- crash before commit / after decision but before persistence
- Journal disagreement with Canonical Inventory

D does not design the primary solution.

## Gate 1B deliverables

Suggested:

```text
docs/architecture/
  GATE1B-DOMAIN-MODEL.md
  GATE1B-IDENTITY-V1.md
  GATE1B-SNAPSHOT-COMPLETENESS.md
  GATE1B-SAFE-RECONCILE.md
  GATE1B-FAILURE-MODEL.md
  GATE1B-CHANGE-JOURNAL-SEMANTICS.md
  GATE1B-ADVERSARIAL-CASES.md
```

Foreman may consolidate files, but semantic responsibilities must remain separable.

## Explicitly deferred to Gate 1C

- PostgreSQL tables/indexes/constraints/migrations
- exact transaction implementation
- exact Query API shape
- journal persistence/event schema
- projection rebuild implementation
- final Collector ADR / selection

## Explicitly deferred post-MVP

### Scanner Resume phase
- durable scanner
- checkpoint
- resume
- bounded concurrency

### Incremental phase
- native delta
- provider cursor
- dirty scope
- incremental hints

## Gate 1B exit condition

Architect must be able to answer:

1. How is canonical identity chosen when provider ID is missing?
2. When does rename/move preserve identity?
3. What produces conflict instead of forced matching?
4. What exact evidence states allow or forbid destructive reconcile?
5. What lifecycle separates Missing from Confirmed Removed?
6. What failures preserve previous canonical truth?
7. What canonical semantic events enter Change Journal?
8. How are concurrent/duplicate inputs made deterministic?

Only then may Gate 1C start.
