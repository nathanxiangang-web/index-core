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
Gate 3 MVP Alpha authorized by Architect in Issue #47, accepted at PR #49
head `055402da83235e6dc5f88f45206378fb210a7672`, and merged to `main` at
`f7edc518dfc99a43cfc464e332e6fe3bfcda601c` (Issue #47 completed).
Gate 4 Reference Consumer Integration was Architect-accepted in Issue #50. Verification fixture PR #53 merged at `9bc98fb7759f16d5c6b772cf4442dea133e2012b`; Reference Web PR #2 merged at `8f7062216dc9924f64d9ae0367e504c279704857`; authoritative findings PR #52 merged at `9d23b24f0ed715fce6128c031da99f6e111257ed`.

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

**IndexCore Core Development — COMPLETE for accepted Alpha scope**

Operating mode: **Stable Alpha Foundation / Maintenance**

**Post-MVP Incremental D0 Change Discovery — COMPLETE / ARCHITECT_ACCEPTED.** **P0 Targeted Scoped Refresh — COMPLETE / ARCHITECT_ACCEPTED** (Issue #62, PR #64 merged at `a4b6ec83e637c25f26585f56c3fb906410fa4b9a`). The Architect has selected **P1 Adaptive Hot-Scope Polling Feasibility Prototype** as the next bounded step. Production incremental implementation remains **NOT AUTHORIZED**.

**Gate 4 — Reference Consumer Integration — CLOSED**

Gate 1C, Gate 2, Gate 3, and Gate 4 are **CLOSED / ARCHITECT_ACCEPTED**.

Gate 4 final accepted artifacts:

- IndexCore verification fixture PR #53 merged at `9bc98fb7759f16d5c6b772cf4442dea133e2012b`;
- Reference Web PR #2 merged at `8f7062216dc9924f64d9ae0367e504c279704857`;
- authoritative Gate-4 findings PR #52 merged at `9d23b24f0ed715fce6128c031da99f6e111257ed`;
- final rendered E2E evidence: `PASS=52 FAIL=0`;
- `INDEXCORE_FROZEN_CONTRACT_CHANGES: NONE`.

Gate 4 proved that a brand-new application can consume IndexCore through the
server-side read-only `/v1` Query Contract without depending on IndexCore
internals, PostgreSQL, AList/OpenList, rclone, or CloudSite architecture.

Current operator/integrator documentation starts at `README.md` and `docs/README.md`.
The accepted core should now be treated as a maintained infrastructure component,
not an open-ended feature-development branch.

CloudSite 1.0 remains **Legacy / Frozen Product**.

### Next blueprint phase

**Gate 5 — Future Product Architecture**

Status:

**NOT AUTHORIZED / ARCHITECT PLANNING REQUIRED**

There is no active Gate-5 execution issue.

Separately, IndexCore Issue #57 tracks the incremental architecture umbrella. Issue #59 completed D0 research. Issue #62 / PR #64 completed and accepted P0: known scope -> OpenList forced refresh -> PARTIAL additive-safe reconcile.

The next bounded IndexCore step is P1 **hot-scope polling feasibility**. P1 may use only a gated/test-only polling harness with in-memory prototype state and the accepted P0 `ScanScope` path. It does not authorize a production scheduler, persistent dirty-scope state, native delta, production sync, destructive behavior, or Gate 5.
Do not start a formal successor product, auth/user system, search/catalog,
preview/download product path, 115 integration, AI, or other deferred product
work until a new Architect plan explicitly authorizes it.

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

Deliberately deferred after Gate 3:

- direct CloudSite integration is no longer the Gate-4 plan; CloudSite 1.0 is legacy/frozen
- formal successor product architecture
- product UI / auth / account / tenant
- Scanner Resume, provider-native delta / true incremental
- destructive-safe provider COMPLETE without separately accepted positive
  completeness evidence
- multi-daemon HA / distributed leases

### Gate 4 — Reference Consumer Integration

Status: **ACCEPTED / CLOSED** (ARCHITECT FINAL ACCEPTANCE — Issue #50)

Evidence:

- `docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md` — authoritative findings;
- IndexCore verification fixture PR #53 → `9bc98fb7759f16d5c6b772cf4442dea133e2012b`;
- Reference Web PR #2 → `8f7062216dc9924f64d9ae0367e504c279704857`;
- IndexCore report PR #52 → `9d23b24f0ed715fce6128c031da99f6e111257ed`;
- real rendered Reference Web E2E: `PASS=52 FAIL=0`.

Accepted results:

- separate disposable Reference Web — PASS;
- server-side-only IndexCore access — PASS;
- Q1–Q9 coverage — PASS;
- hierarchy, ambiguity, active/removed, Journal and stale cursor behavior — PASS;
- retained DEPRECATED/DELETED partition navigation — PASS;
- IndexCore unavailable/restart behavior — PASS;
- zero direct PostgreSQL / IndexCore Go / provider / CloudSite coupling — PASS;
- no public IndexCore contract gap found.

`INDEXCORE_FROZEN_CONTRACT_CHANGES: NONE`

Gate 4 did **not** create CloudSite 2 and did not authorize the formal successor
product. The Reference Web remains a disposable validation artifact.

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

**CORE DEVELOPMENT STATUS — COMPLETE (ACCEPTED ALPHA SCOPE)**

**GATE 4 REFERENCE CONSUMER INTEGRATION — CLOSED**

Gate 4 has completed Architect Final Acceptance and all three accepted artifacts
are merged.

Current execution status:

```text
Gate 4: CLOSED
Post-MVP Incremental D0: COMPLETE / ARCHITECT_ACCEPTED (Issue #59 under #57)
Post-MVP Incremental P0: AUTHORIZED — PROTOTYPE ONLY (Issue #62)
Post-MVP Incremental production implementation: NOT AUTHORIZED
Gate 5: NOT AUTHORIZED
Active Worker task: Issue #62
```

Next action is Architect planning for Gate 5 only. No successor-product
implementation is authorized by this closeout.
