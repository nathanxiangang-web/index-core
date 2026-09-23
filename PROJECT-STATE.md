# Index Core — Project State

> Concise current-state truth for recovery.

## Last updated

2026-09-24

## Main branch

Always verify the current remote `main` before execution.

Latest accepted architecture milestone:

Gate 1B accepted at `9291ac0706af6d584684498d75b714c408b9ffbe`.
Gate 1C accepted at PR #43 head `7a3b32f` (ARCHITECT FINAL ACCEPTANCE — Gate 1C
CLOSED); PR #43 is authorized to merge to `main`.

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

**Gate 2 — PoC**

Gate 1C is **CLOSED** (ARCHITECT FINAL ACCEPTANCE on PR #43); PR #43 is
authorized to merge to `main`, and Issue #40 closes on that merge.

Status:

**IN PROGRESS (Gate 2 PoC)**

Active execution issue:

**#44 — [CODEX][GATE-2] Index Core PoC**

Branch:

**`poc/gate2-indexcore`** (from `main` @ `1a420df`)

Technology stack (Architect-locked via PR #45 / Issue #44):

**Go 1.27.x + PostgreSQL 18 + pgx/v5** — Go Modules, SQL-first migrations,
real-PostgreSQL integration tests, `log/slog`, env/flag config; no ORM, no
Gin/Fiber/Echo, no Redis/Kafka, no DI framework.

No CloudSite integration or UI is authorized yet.

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

Accepted core-semantics commit:

`33e2e63440eb87afae7a572e22e296b3ee2c701b`

Pre-code hardening commit:

`9291ac0706af6d584684498d75b714c408b9ffbe`

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

### Gate 1C — Persistence / Query / Collector Boundary

Status: **ACCEPTED** (ARCHITECT FINAL ACCEPTANCE — Gate 1C CLOSED, PR #43,
final verification head `7a3b32f`)

Accepted contracts (`docs/architecture/`, all **FROZEN**):

- `GATE1C-POSTGRESQL-STORE.md` (A — PostgreSQL Store Contract)
- `GATE1C-TRANSACTION-BOUNDARY.md` (B — Transaction Boundary)
- `GATE1C-QUERY-CONTRACT.md` (C — Query Contract)
- `GATE1C-JOURNAL-PERSISTENCE.md` (D — Canonical Change Journal Persistence)
- `GATE1C-COLLECTOR-ADAPTER-CONTRACT.md` (E — Collector Adapter Contract)

Accepted decisions (`docs/decisions/`, both **ACCEPTED**):

- `ADR-001-COLLECTOR-BOUNDARY.md` — initial Collector: rclone, additive-only
  Gate 2 role; NOT destructive-safe COMPLETE; COMPLETE/removal validated with
  controlled Snapshot V1/V2 fixtures.
- `ADR-002-POSTGRESQL-STORE.md` — PostgreSQL as the Store realization behind the
  Store Interface.

Key frozen Gate 1C semantics:

- per-root `event_seq` is the authoritative Journal cursor; no cross-root canonical order
- absolute per-root FIFO admission; generation advances only on real canonical mutation
- structured `snapshot_identity` (kind/namespace/version/value); append-only application history
- final IO3 identity is finalized after Kernel evaluation and includes Kernel-derived decision evidence (e.g. `scope_shrink_corroboration`)
- `scope_shrink_corroboration` is set once by the Kernel on `SUBMITTED -> EVALUATED`, then immutable
- J6 Journal repair may append a corrective event with no canonical mutation / no generation advance, and remains permitted on a `DELETED` root
- `completeness_flag` is a non-authoritative Collector hint; Kernel `acceptance_state` is authoritative
- rclone RC cannot establish confirmed-no-skips; `skipped_scopes` stays UNKNOWN (never `[]`) without a positive completeness signal

## Gate 1C scope (CLOSED)

All six Gate 1C deliverables are delivered, Architect-accepted, and frozen:

1. PostgreSQL Store realization — FROZEN (A)
2. exact transaction boundary / locking / CAS semantics — FROZEN (B)
3. minimal read-only Query Contract — FROZEN (C)
4. Canonical Change Journal persistence / sequence semantics — FROZEN (D)
5. Collector Adapter Contract — FROZEN (E)
6. Architect-reviewed initial Collector recommendation / ADR — ACCEPTED (ADR-001)

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

### Gate 2 PoC — AUTHORIZED by Gate 1C closure
- Snapshot -> PostgreSQL Inventory
- controlled Snapshot V1/V2 fixtures for COMPLETE/removal Kernel semantics
- first selected Collector adapter validation (rclone, additive-only)
- destructive COMPLETE deferred until a provider proves positive completeness evidence

### Post-MVP Scanner Resume
- durable scanner
- checkpoint/resume
- bounded concurrency

### Post-MVP Incremental
- provider-native delta
- provider cursor
- dirty scope / true incremental

## Implementation status

**NO PRODUCT MVP CODE YET**

Gate 1C is ChatGPT Architect-accepted (PR #43, Gate 1C CLOSED); **Gate 2 PoC is
unblocked**.
