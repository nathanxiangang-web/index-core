# Index Core — Project State

> Concise current-state truth for recovery.

## Last updated

2026-09-24

## Main branch

Always verify the current remote `main` before execution.

Latest accepted architecture milestone:

Gate 1B accepted at `9291ac0706af6d584684498d75b714c408b9ffbe`.
Gate 1C accepted at PR #43 head `7a3b32f` (ARCHITECT FINAL ACCEPTANCE — Gate 1C
CLOSED).
Gate 2 PoC accepted at PR #46 head `846a270` (ARCHITECT FINAL ACCEPTANCE —
Gate 2 PoC) and merged to `main` at `410050d0b084d999063cde1a4be8d2051fbcb88f`.
Gate 3 MVP Alpha authorized by Architect in Issue #47, then accepted at PR #49
head `055402da83235e6dc5f88f45206378fb210a7672` (ARCHITECT FINAL ACCEPTANCE —
Gate 3 MVP Alpha).

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

**Gate 3 — MVP Alpha / Standalone Runtime & Scale**

Gate 1C is **CLOSED**. Gate 2 PoC is **CLOSED** and merged to `main` at
`410050d0b084d999063cde1a4be8d2051fbcb88f`. Issue #44 is completed.

Gate 3 MVP Alpha is **ARCHITECT ACCEPTED** at PR #49 head
`055402da83235e6dc5f88f45206378fb210a7672` (ARCHITECT FINAL ACCEPTANCE — Gate 3
MVP Alpha).

Status:

**ARCHITECT ACCEPTED / READY_TO_MERGE**

Active execution issue:

**#47 — [CODEX][GATE-3] Standalone Alpha Runtime & Scale**

Executor branch:

**`alpha/gate3-runtime`**.

Architect-locked Gate-3 runtime shape:

- one standalone Go binary: `indexcore`;
- explicit `migrate`, `serve`, root administration, and `scan --root` CLI paths;
- PostgreSQL 18.x + pgx/v5 remain the Store realization;
- single active write-orchestration daemon per database for Gate 3;
- bounded concurrency across different roots inside the daemon;
- standard-library `net/http` read-only `/v1` Query transport;
- unauthenticated Alpha HTTP binds to loopback by default;
- rclone remains external and additive-safe only;
- Gate-3 final acceptance also requires a real AList/OpenList Collector integration
  validation, still behind the Collector boundary and initially additive-safe;
- >=20,000-resource real-PostgreSQL scale validation is mandatory;
- CloudSite integration remains Gate 4.

Still deferred: product UI, auth/account/tenant system, Scanner Resume/provider
traversal checkpoints, native delta/true incremental, destructive-safe provider
COMPLETE without positive evidence, multi-daemon HA/distributed leases, Redis/Kafka,
Search/Catalog/AI/downloader features.

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

### Gate 2 — Index Core PoC

Status: **ACCEPTED** (ARCHITECT FINAL ACCEPTANCE — Gate 2 PoC, PR #46, final
verification head `846a270`)

Evidence:

- `docs/gate2/GATE2-POC-VERIFICATION-REPORT.md` (fixture matrix, failure paths,
  CANDIDATE choices: "chosen for PoC ≠ newly frozen architecture")
- Go 1.27.1 + PostgreSQL 18.6 + pgx/v5; safe `Coordinator.ProcessSnapshot` /
  `ProcessHead`; real rclone v1.75.1 local-backend process-boundary run

Accepted area results:

- PostgreSQL Store — PASS
- Transaction Boundary — PASS
- Kernel Evaluation — PASS
- Safe Reconcile — PASS
- Change Journal / J6 — PASS
- Query Contract — PASS
- V1/V2 Fixture PoC — PASS
- rclone additive-only — PASS

`FROZEN_CONTRACT_CHANGES: NONE`

Deliberately NOT supported by this PoC (deferred):

- destructive-safe provider COMPLETE (rclone skip evidence stays UNKNOWN)
- CloudSite integration, UI, Scanner Resume, native delta / true incremental

### Gate 3 — MVP Alpha / Standalone Runtime & Scale

Status: **ACCEPTED** (ARCHITECT FINAL ACCEPTANCE — Gate 3 MVP Alpha, PR #49,
final verification head `055402da83235e6dc5f88f45206378fb210a7672`)

Evidence:

- `docs/gate3/GATE3-ALPHA-VERIFICATION-REPORT.md` (authoritative Round 5 status,
  20k full-runtime-ingestion baseline)
- Go 1.27.1 + PostgreSQL 18.6 + pgx/v5; real rclone v1.75.1; real `xhofe/alist`
  source (3 entries / 3 HTTP-visible resources); read-only HTTP `/v1`;
  clean-volume Compose smoke

Accepted area results:

- RUNTIME — PASS
- CONFIG_STARTUP — PASS
- ROOT_ADMIN — PASS
- WORKER_RECOVERY — PASS
- RCLONE_SCAN_PATH — PASS
- ALIST_OPENLIST_REAL_SOURCE — PASS
- QUERY_HTTP_V1 — PASS
- OBSERVABILITY — PASS
- SCALE_20K — PASS
- PACKAGING — PASS
- E2E_ALPHA — PASS
- GATE2_REGRESSION — PASS

`FROZEN_CONTRACT_CHANGES: NONE`

The 20k Stage-1 admission stays O(1) (~1.5–2.6 ms) with entry writes outside the
root lock, so the runtime transaction boundary is accepted.

Deliberately NOT authorized (deferred):

- CloudSite integration — Gate 4, **NOT yet authorized**
- product UI / auth / account / tenant
- Scanner Resume, provider-native delta / true incremental
- destructive-safe provider COMPLETE without separately accepted positive
  completeness evidence
- multi-daemon HA / distributed leases

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

**GATE 3 MVP ALPHA ARCHITECT ACCEPTED — READY_TO_MERGE**

Gate 3 MVP Alpha is accepted at PR #49 head
`055402da83235e6dc5f88f45206378fb210a7672`. After the administrative closeout
diff is verified, PR #49 may merge to `main` and Issue #47 closes.

Gate 4 (CloudSite integration and the blueprint's next stage) is **NOT yet
authorized**; no CloudSite work begins until the Architect opens it.
