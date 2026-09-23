# Index Core — Project State

> Concise current-state truth for recovery.

## Last updated

2026-09-23

## Main branch

Always verify the current remote `main` before execution.

Latest accepted architecture milestone:

`33e2e63440eb87afae7a572e22e296b3ee2c701b`

## Execution model

```text
ChatGPT Architect
       ↓
Codex Executor
       ↓
branch + docs/code/tests
       ↓
Pull Request
       ↓
ChatGPT Architect Review
       ↓
main
```

The previous GLM Foreman + Worker A/B/C/D topology is retired.

The experimental subagent topology is retired.

## Current phase

**Gate 1C — Persistence / Query / Collector Boundary**

Status:

**READY TO START**

Active execution issue:

**#40 — [CODEX][GATE-1C] Persistence, Query & Collector Contracts**

No CloudSite integration or product MVP implementation is authorized yet.

## Accepted architecture

### Gate 1A — Responsibility Boundary & Contract Skeleton

Status: **ACCEPTED**

Accepted:
- Collector / Kernel / Store / Consumer boundaries
- Canonical Inventory as the single resource truth
- Collector supplies normalized observations/evidence only
- Kernel owns identity/completeness/reconcile/journal semantics
- Store persists behind a Store Interface
- Consumer is read-only against canonical truth

### Gate 1B — Core Semantics

Status: **ACCEPTED**

Accepted commit:

`33e2e63440eb87afae7a572e22e296b3ee2c701b`

Accepted contracts:

- `docs/architecture/GATE1B-DOMAIN-MODEL.md`
- `docs/architecture/GATE1B-SNAPSHOT-COMPLETENESS.md`
- `docs/architecture/GATE1B-SAFE-RECONCILE.md`
- `docs/architecture/GATE1B-ADVERSARIAL-CASES.md`

Key frozen semantics:

- hash is evidence/fingerprint, not canonical identity
- provider IDs require stability qualification before strong use
- path/size alone cannot prove identity
- incomplete coverage cannot create canonical absence/removal evidence
- freshness and failure visibility are provider-neutral evidence
- significant shrink is non-destructive until independently corroborated
- Kernel never re-traverses Provider for completeness/removal validation
- CanonicalResource separates ResourcePresence from RemovalEvidenceState
- confirmed removal is a logical REMOVED tombstone
- Canonical Change Journal is append-only and canonical-wins on disagreement
- MOVE/RENAME + UPDATE has deterministic same-generation event ordering
- same-root input admission is serialized before reconcile work
- duplicate/out-of-order inputs cannot overwrite newer canonical truth
- roots are disjoint partitions; root_id is never reused
- DELETED roots are logical tombstones retained for history/audit

## Current Gate 1C scope

Freeze implementation-facing architecture required by Gate 2 PoC:

1. PostgreSQL Store realization
2. exact transaction boundary / locking / CAS semantics
3. minimal read-only Query Contract
4. Canonical Change Journal persistence / sequence semantics
5. Collector Adapter Contract
6. Architect-reviewed initial Collector recommendation / ADR

## Gate 1C must preserve

- Domain != PostgreSQL schema/ORM
- failed commit preserves prior canonical truth
- canonical + journal + generation + input-order state commit atomically
- incomplete inputs cannot advance removal state
- append-only journal
- root_id/resource_id immutability
- consumer write prohibition
- Collector replaceability

## Explicitly deferred after Gate 1

### Gate 2 PoC
- Snapshot -> PostgreSQL Inventory
- v1/v2 reconcile validation
- first selected Collector adapter validation

### Post-MVP Scanner Resume
- durable scanner
- checkpoint/resume
- bounded concurrency

### Post-MVP Incremental
- provider-native delta
- provider cursor
- dirty scope / true incremental

## Implementation status

**NO PRODUCT MVP CODE**

Gate 2 remains blocked until Gate 1C is ChatGPT Architect-accepted.
