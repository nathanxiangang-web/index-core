# Gate 1C — Transaction Boundary

> Implementation-facing contract for the exact transaction / CAS / locking
> semantics of one per-root reconcile.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Baseline: remote `main` = `6a131f17657807d9aee2921be1f286ceaff784e4`.
> Depends on `GATE1C-POSTGRESQL-STORE.md` (deliverable A).

---

## 0. Scope and tag system

### 0.1 Scope

This document freezes the transaction semantics that make the accepted Gate 1B
Safe Reconcile / Input Ordering / idempotency semantics true in PostgreSQL. It is
the "B" deliverable of Issue #40.

Non-goals: schema definition (doc A), read API (doc C), Collector adapter
mapping (deliverable E), product code (DO NOT).

### 0.2 Tag system

Same as doc A: `DERIVED` / `PROPOSED` / `CANDIDATE` / `DEFERRED` / `REJECTED`,
with evidence tags `FACT` / `INFERENCE` / `UNKNOWN`.

### 0.3 The architectural target (restated from Issue #40)

```text
load canonical @ generation/order state
validate expected generation/admission order
apply canonical changes
append ordered journal events
advance generation
record applied snapshot/order token
commit atomically
```

> `DERIVED` "CAS/locking enforce ordering; database timing must not define it"
> (Issue #40). The ordering semantic is the Kernel-owned admission sequence
> (GATE1B-SAFE-RECONCILE IO1-IO7); CAS/locking are enforcement only (IO6).
> `FACT`.

---

## 1. Two serialized per-root stages

`PROPOSED` — the Worker proposes two stages, both serialized per root, matching
IO1 ("every accepted input enters a single serialized per-root admission point
before reconcile work can run").

```text
            per-root serialized domain
  ┌───────────────────────────────────────────────┐
  │  Stage 1 — Admission ingress (short tx)        │
  │    allocate admission_seq (IO1)                │
  │    record PENDING input                        │
  │    commit                                      │
  ├───────────────────────────────────────────────┤
  │  Stage 2 — Reconcile (one tx)                  │
  │    process PENDING inputs in admission order   │
  │    load → validate → apply → journal →         │
  │    advance generation → record applied         │
  │    commit atomically                           │
  └───────────────────────────────────────────────┘
```

### 1.1 Stage 1 — Admission ingress

| Step | Action | Schema | Tag |
|------|--------|--------|-----|
| A1 | Acquire per-root serialization | `index_root` row lock (`SELECT ... FOR UPDATE`) or `pg_advisory_xact_lock` | `PROPOSED` |
| A2 | Allocate `admission_seq` | `UPDATE index_root SET latest_admission_seq = latest_admission_seq + 1 WHERE root_id = :root RETURNING latest_admission_seq` | `DERIVED` (semantic) / `PROPOSED` (mechanism) |
| A3 | Record input | INSERT `index_admission(root_id, admission_seq, snapshot_id, status='PENDING')` | `PROPOSED` |
| A4 | Commit | releases the per-root lock | `PROPOSED` |

> `DERIVED` The sequence is assigned before any reconcile work and cannot be
> reordered by worker/thread scheduling or commit timing (IO1). It is NOT derived
> from provider wall-clock (IO7). `FACT`.
>
> `DERIVED` Two inputs for the same root with no intrinsic provider order take
> their serialized ingress order as the authoritative system order (IO1). `FACT`.

### 1.2 Stage 2 — Reconcile (single transaction)

| Step | Action | Tag |
|------|--------|-----|
| R1 | Acquire per-root serialization for the reconcile | `DERIVED` |
| R2 | Select the lowest `PENDING` `admission_seq` for the root (ordered) | `DERIVED` (IO5) |
| R3 | Load `index_root` (lifecycle, `current_generation`, `latest_admission_seq`) | `DERIVED` |
| R4 | Reject if `lifecycle_state = 'DELETED'` -> record `REJECTED`, no mutation | `DERIVED` |
| R5 | Idempotency check against `index_applied_snapshot` (IO3) | `DERIVED` |
| R6 | Stale/out-of-order check (IO2/IO4) | `DERIVED` |
| R7 | Load canonical inventory at `current_generation` | `DERIVED` |
| R8 | Compute transitions (Kernel pure function; not Store) | `DERIVED` |
| R9 | Apply canonical changes | `DERIVED` |
| R10 | Append ordered journal events (generation + `intra_generation_seq`) | `DERIVED` |
| R11 | Advance generation with CAS | `DERIVED` |
| R12 | Record `index_admission.status='APPLIED'` + `index_applied_snapshot` | `DERIVED` |
| R13 | Commit atomically | `DERIVED` |

---

## 2. CAS / locking semantics

### 2.1 Generation CAS

> `DERIVED` Generation is the optimistic concurrency token; `commit` succeeds only
> if the persisted generation still equals the value the reconcile loaded
> (GATE1A C1.3; GATE1B-SAFE-RECONCILE Sec 2.5). `FACT`.

Mechanism (`PROPOSED`):

```sql
UPDATE index_root
   SET current_generation = current_generation + 1
 WHERE root_id = :root
   AND current_generation = :expected_generation;
-- rowcount = 1 -> success; rowcount = 0 -> CAS_FAIL
```

### 2.2 Per-root serialization

| Option | Mechanism | Assessment |
|--------|-----------|------------|
| A | `SELECT ... FROM index_root WHERE root_id=:root FOR UPDATE` at tx start | `PROPOSED` (recommended): serializes same-root reconciles; different roots do not block (IO: "reconciles for different roots are independent"). |
| B | `pg_advisory_xact_lock(hash(root_id))` | `CANDIDATE`: avoids long row-lock on the hot `index_root` row; released at tx end. |
| C | Optimistic CAS only, no lock, retry on `CAS_FAIL` | `CANDIDATE`: simplest; relies on retry loop; may livelock under contention. |

> `DERIVED` Regardless of option, two racing reconciles for the same root cannot
> both commit (GATE1A C1.3). `FACT`.
>
> `DERIVED` Reconciles for *different* roots need not serialize (GATE1B-SAFE-RECONCILE
> Sec 2.6). `FACT`.

### 2.3 CAS is enforcement, not ordering

> `DERIVED` Generation CAS enforces IO2/IO4 but does NOT define the order. The
> authoritative order is the Kernel-owned `admission_seq` (IO6). `FACT`.
> `INFERENCE`: therefore the reconcile MUST process inputs in ascending
> `admission_seq`, and MUST NOT let a later-admitted input commit before an
> earlier-admitted pending input for the same root.

### 2.4 Lock scope

> `DERIVED` The lock is per root, held for the duration of the reconcile
> transaction, and does not block consumers (MVCC; GATE1A C2.2 "readers do not
> block the writer"). `FACT`.

---

## 3. Idempotency (Scenario 4 / IO3)

`DERIVED` semantic; `PROPOSED` mechanism.

| Step | Behavior |
|------|----------|
| Compute `snapshot_identity` | Collector-declared stable token scoped to root (doc A Sec 3.8). |
| Look up `index_applied_snapshot(root_id, snapshot_identity)` | If present AND `applied_generation = current_generation` -> NO-OP. |
| NO-OP | No canonical write, no journal event, no generation bump. Set `index_admission.status='NOOP'`. |
| Replay after canonical advanced | `applied_generation < current_generation` -> normal reconcile against the newer generation (NOT a no-op). |

> `DERIVED` "Same snapshot" = same snapshot identity AND same canonical generation
> observed at load (GATE1B-SAFE-RECONCILE Sec 2.4). `FACT`.

> `CANDIDATE` — the `snapshot_identity` key is not frozen by Gate 1B (R-LC-6).
> Escalated to Architect (doc A Sec 3.8, Sec 9).

---

## 4. Stale / out-of-order input (Scenario 6 / IO2, IO4)

`DERIVED` semantic; `PROPOSED` mechanism.

Definitions:

- `applied_max` = max `admission_seq` with `status='APPLIED'` for the root
  (equivalently a `latest_applied_admission_seq` high-water mark on `index_root`;
  `CANDIDATE`).
- `own_seq` = this input's `admission_seq`.

| Condition | Classification | Action |
|-----------|----------------|--------|
| `own_seq <= applied_max` AND snapshot is a duplicate of an applied input | `NOOP` (IO3) | no mutation |
| `own_seq <= applied_max` AND NOT a duplicate | `STALE_INPUT` (IO4) | no canonical mutation; record `index_reconcile_result.outcome='STALE_INPUT'` |
| `own_seq > applied_max` AND no lower `PENDING` exists | normal | process against current generation (IO5) |
| `own_seq > applied_max` AND a lower `PENDING` exists | ordering violation | MUST NOT commit before the lower input (IO5/IO6); wait/skip/reorder under the per-root lock |

> `DERIVED` An older input MUST NOT overwrite canonical state committed by a newer
> admitted input (IO2). `FACT`.
>
> `INFERENCE` Because Stage 1 assigns `admission_seq` under the same per-root
> serialization, ascending `admission_seq` processing (R2) makes IO5/IO6 hold by
> construction; the CAS in Sec 2.1 is a redundant safety net.

> `CANDIDATE` — retry vs abort policy on `CAS_FAIL` (Scenario 5) is configuration
> (GATE1B-SAFE-RECONCILE Sec 2.5). `FACT`.

---

## 5. Atomicity and rollback (INV-013)

> `DERIVED` Canonical changes + journal events + generation bump + admission/
> applied-state update commit all-or-nothing (GATE1A C2.2; GATE1B-SAFE-RECONCILE
> Sec 2.8). `FACT`.

| Rule | Statement | Tag |
|------|-----------|-----|
| T-AT1 | All Stage 2 writes occur in ONE transaction. | `DERIVED` |
| T-AT2 | On any error before commit, `ROLLBACK` leaves durable state unchanged. | `DERIVED` |
| T-AT3 | A failed commit leaves previous canonical truth intact (INV-013). | `DERIVED` |
| T-AT4 | No partial journal append, no partial generation bump is durable. | `DERIVED` |
| T-AT5 | The `index_admission` row for a failed reconcile is set to `REJECTED`/`FAILED` in a SEPARATE, later transaction (so the failure itself is recorded without violating atomicity). | `PROPOSED` |

> `INFERENCE` for T-AT5: recording the failure cannot be part of the rolled-back
> transaction; it must be a subsequent write. This is a Worker proposal.

---

## 6. Failure-mode mapping (Gate 1B Failure Model -> transaction behavior)

| Gate 1B scenario | Transaction behavior | Tag |
|------------------|----------------------|-----|
| 1 — Identity UNRESOLVED | no canonical mutation; conflict into `index_reconcile_result`; continue other entries; commit the rest | `DERIVED` |
| 2 — Snapshot PARTIAL | additive-only writes; MUST NOT touch `removal_evidence_state`/`missing_since`/`consecutive_complete_missing`; no `resource-removed`; commit | `DERIVED` |
| 3 — internal error | `ROLLBACK`; record failure (T-AT5) | `DERIVED` |
| 4 — same snapshot replay | NO-OP; no writes; no generation bump; `status='NOOP'` | `DERIVED` |
| 5 — stale generation | CAS returns 0 rows -> `ROLLBACK`; retry (reload+recompute) or abort per policy | `DERIVED` |
| 6 — concurrent same-root | per-root serialization + ascending `admission_seq`; exactly one commits per generation | `DERIVED` |
| 7 — commit failure | `ROLLBACK`; previous truth preserved | `DERIVED` |

> `DERIVED` Consolidated guarantee: any uncommitted reconcile MUST NOT destroy
> previous canonical truth (GATE1B-SAFE-RECONCILE Sec 2.8; INV-013). `FACT`.

---

## 7. Isolation level and MVCC

| Aspect | Recommendation | Tag |
|--------|----------------|-----|
| Isolation level | `READ COMMITTED` with explicit row locks / CAS; `SERIALIZABLE` is an alternative but heavier | `CANDIDATE` |
| Consumers | read committed snapshot at a generation; never blocked by the writer (MVCC) | `DERIVED` (GATE1A C2.2) |
| Canonical read during reconcile | read at `current_generation` loaded in R3; the CAS in R11 rejects if it changed | `DERIVED` |

> `CANDIDATE` — exact isolation level is a Store implementation choice as long as
> the IO1-IO7 semantics and INV-013 hold. `INFERENCE`.

---

## 8. Lock ordering / deadlock avoidance

| Rule | Statement | Tag |
|------|-----------|-----|
| L1 | Only one root lock is held per reconcile; no cross-root lock acquisition. | `PROPOSED` |
| L2 | Within a reconcile, lock the root row first, then touch child tables in a fixed order (canonical -> journal -> admission/applied). | `PROPOSED` |
| L3 | Different roots never share a lock -> no cross-root deadlock. | `INFERENCE` |

---

## 9. Consistency-check mapping (Issue #40)

| # | Check | Where | Status |
|---|-------|-------|--------|
| 2 | PARTIAL/STALE/SUSPICIOUS absence cannot advance removal state | Sec 6 (Scenario 2), Sec 1.2 R8/R9 | `COVERED` |
| 3 | Confirmed removal = tombstone + journal atomically | Sec 1.2 R9/R10/R13, Sec 5 | `COVERED` |
| 4 | Duplicate replay idempotent | Sec 3 | `COVERED` |
| 5 | Older admission sequence cannot commit over newer | Sec 4 | `COVERED` |
| 6 | MOVE/RENAME + UPDATE ordering preserved | Sec 1.2 R10 (`intra_generation_seq`) | `COVERED` |
| 7 | DELETED root cannot reconcile | Sec 1.2 R4 | `COVERED` |

---

## 10. Open items

| Item | Tag | Note |
|------|-----|------|
| Serialization mechanism (row lock vs advisory lock vs optimistic) | `CANDIDATE` | Sec 2.2. |
| `CAS_FAIL` retry vs abort | `CANDIDATE` | Sec 4. |
| Isolation level | `CANDIDATE` | Sec 7. |
| `latest_applied_admission_seq` high-water mark column | `CANDIDATE` | Sec 4. |
| Failure recording transaction (T-AT5) shape | `PROPOSED` | Sec 5. |
| Two-stage vs single-transaction admission+reconcile | `CANDIDATE` | Sec 1; both satisfy IO1-IO7, two-stage matches IO1 wording more literally. |

---

## 11. Golden cases (transaction-level)

| # | Case | Expected transaction behavior |
|---|------|-------------------------------|
| T1 | Two concurrent same-root reconciles | per-root lock serializes; ascending admission order; each sees the other's committed generation; no double commit. |
| T2 | CAS failure | 0-row UPDATE -> rollback -> retry/abort; no durable mutation. |
| T3 | Replay | `index_applied_snapshot` hit -> NO-OP; no generation bump. |
| T4 | Stale input | `own_seq <= applied_max` non-duplicate -> `STALE_INPUT`; no mutation. |
| T5 | Internal error mid-reconcile | rollback; `index_admission` set `FAILED` in a later tx. |
| T6 | Confirmed removal | tombstone UPDATE + `resource-removed` INSERT + generation CAS + applied record, one commit. |
| T7 | DELETED root | R4 rejects before any write. |

---

## 12. Self-check against Gate 1C constraints

| Constraint | Status |
|------------|--------|
| No CloudSite / UI / Scanner Resume / incremental | Met |
| No new gate | Met |
| No silent Gate 1B change | Met (every rule cites Gate 1B / Gate 1A) |
| Database timing does not define ordering | Met (Sec 2.3, IO1 admission sequence is authoritative) |
| No product code | Met |
| Did not merge own PR | Met |