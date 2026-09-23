# Gate 1C — Transaction Boundary

> Implementation-facing contract for the exact transaction / CAS / locking
> semantics of one per-root reconcile.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **PARTIAL_FOR_ARCH_REVIEW** — reworked per PR #43 Architect review (round 2); A/B/C are NOT FROZEN.
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
  │    process the head-of-line input only         │
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
| R2 | Select the ABSOLUTE lowest non-terminal `admission_seq` (`head_seq` = min `status='PENDING'`). ONLY this head-of-line input may be processed. Reclaim it (SAME `admission_seq`) only if its claim is free or its lease expired. No higher sequence may ever be selected. | `DERIVED` (IO5/IO6; PR #43 review round 2) |
| R3 | Load `index_root` (lifecycle, `current_generation`, `latest_admission_seq`) | `DERIVED` |
| R4 | Reject if `lifecycle_state = 'DELETED'` -> record `REJECTED`, no mutation | `DERIVED` |
| R5 | Idempotency check against `index_applied_snapshot` (IO3; see Sec 3) | `DERIVED` |
| R6 | Stale/out-of-order check (IO2/IO4) | `DERIVED` |
| R7 | Load canonical inventory at `current_generation` | `DERIVED` |
| R8 | Compute transitions (Kernel pure function; not Store) | `DERIVED` |
| R9 | Apply canonical changes | `DERIVED` |
| R10 | Append ordered journal events (generation + `intra_generation_seq`) | `DERIVED` |
| R11 | **Conditional** generation validation: MUTATION path CAS `expected_generation = G` and advance `G -> G+1`; ZERO-mutation normal reconcile keeps `G` but MUST still verify persisted `current_generation == expected_generation` before recording (compare-and-check / no-op CAS, or an equivalent row-lock/version check held through commit), so a stale computation is never accepted | `DERIVED` (PR #43 final verification) |
| R12 | Set `index_admission.status='APPLIED'` + APPEND an `index_applied_snapshot` row using the ACTUAL post-application generation (`G+1` on mutation, `G` on zero mutation) | `DERIVED` (PR #43 final verification) |
| R13 | Commit atomically | `DERIVED` |

> `DERIVED` (Architect, PR #43 review #1) — journal events are ordered per root by
> `event_seq`; the global `event_id` is opaque identity only and is NOT a
> commit-order cursor (doc A Sec 3.9). Under concurrent roots, allocation order is
> not commit-visibility order. Cross-root consumers track a **per-root cursor
> vector** `{root_id: event_seq}`, and an all-roots read defines NO canonical
> global order. `FACT`.

> `DERIVED` (Architect, PR #43 review round 2) — the correctness rule for R2 is the
> MINIMUM non-terminal `admission_seq` (`head_seq`), NOT "the lowest claimable
> `PENDING`". A head that is mid-flight (claimed, lease not expired) is still
> non-terminal and MUST block every higher input. `FACT`.

### 1.3 Crash recovery and absolute head-of-line (PR #43 review #2 / round 2)

> `DERIVED` (Architect) — process death after Stage 1 MUST NOT head-of-line block
> a root forever, a claimed-but-not-committed input must be reclaimable, AND a
> higher sequence must never leapfrog a lower non-terminal one. The schema is
> `index_admission.status='PENDING'` plus
> `claimed_by`/`claimed_at`/`lease_expires_at` (doc A `C-A4`/`C-A5`). `FACT`.

| # | Rule | Tag |
|---|------|-----|
| RC1 | The ONLY claimable input is the head-of-line `head_seq` (min non-terminal `admission_seq`), and only when `claimed_by IS NULL` or `lease_expires_at < now()`. A higher sequence is NEVER claimable while `head_seq` exists. | `DERIVED` (PR #43 review round 2) |
| RC2 | A reclaim keeps the SAME `admission_seq`; sequences are never re-numbered or reassigned to other inputs. | `DERIVED` |
| RC3 | Claiming sets `claimed_by`/`claimed_at`/`lease_expires_at` in a short transaction before reconcile work; Stage 2 then runs normally. | `PROPOSED` |
| RC4 | A lower sequence actively being processed (claimed, lease NOT expired) STILL blocks all higher sequences. There is no leapfrog, ever. | `DERIVED` (PR #43 review round 2) |
| RC5 | If the head-of-line input is found already superseded by a committed higher input, it becomes `STALE_INPUT` per IO4 and MUST NOT overwrite canonical state. | `DERIVED` |
| RC6 | A rolled-back reconcile sets `status='FAILED'` in a separate later transaction (T-AT5); `FAILED` is terminal and lets the NEXT sequence advance (the root is not blocked). | `PROPOSED` |
| RC7 | A higher sequence advances ONLY after `head_seq` reaches a terminal status (`APPLIED`/`NOOP`/`REJECTED`/`STALE_INPUT`/`FAILED`). | `DERIVED` |

> `DERIVED` `FAILED` is distinct from `PENDING`: a `FAILED` input is not
> auto-retried by reclaim unless an explicit retry policy re-admits it under the
> SAME `admission_seq`. `INFERENCE`.

---

## 2. CAS / locking semantics

### 2.1 Generation CAS (conditional advance)

> `DERIVED` Generation is the optimistic concurrency token; a commit succeeds only
> if the persisted generation still equals the value the reconcile loaded
> (GATE1A C1.3; GATE1B-SAFE-RECONCILE Sec 2.5). `FACT`.
>
> `DERIVED` (Architect, PR #43 final verification) — generation advances ONLY when
> the reconcile actually mutates canonical state (Gate 1B frozen). A zero-mutation
> normal reconcile keeps `current_generation`, but MUST still validate the same
> concurrency token before recording the application, so a result computed against
> superseded truth is never accepted. `FACT`.

Mechanism (`PROPOSED`):

MUTATION path — CAS and advance:

```sql
UPDATE index_root
   SET current_generation = current_generation + 1
 WHERE root_id = :root
   AND current_generation = :expected_generation;
-- rowcount = 1 -> success; rowcount = 0 -> CAS_FAIL
```

ZERO-mutation normal-reconcile path — NO advance, but the SAME token check MUST
hold and MUST remain concurrency-safe. A compare-and-check / no-op CAS, or an
equivalent row-lock / version check held through commit, is acceptable:

```sql
SELECT 1 FROM index_root
 WHERE root_id = :root
   AND current_generation = :expected_generation
 FOR UPDATE;
-- no matching row -> the loaded truth was superseded; ROLLBACK and re-load
-- (match -> proceed to record the application at the UNCHANGED generation)
```

Under the optimistic option C in Sec 2.2 (no lock), the zero-mutation path MUST
still perform an equivalent compare-and-check (e.g. a no-op CAS on a version
column) so a stale computation cannot be accepted.

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
> earlier-admitted non-terminal input for the same root.

### 2.4 Lock scope

> `DERIVED` The lock is per root, held for the duration of the reconcile
> transaction, and does not block consumers (MVCC; GATE1A C2.2 "readers do not
> block the writer"). `FACT`.

---

## 3. Idempotency (Scenario 4 / IO3)

`DERIVED` semantic; `PROPOSED` mechanism.

`index_applied_snapshot` is an **append-only application history** (doc A
`C-AS1`..`C-AS3`), not a single latest row.

| Step | Behavior |
|------|----------|
| Compute `snapshot_identity` | Frozen provider-neutral contract (doc A Sec 3.8): the tuple (`kind`, `namespace`, `version`, `value`) from a stable adapter/native revision token OR a versioned deterministic digest. No wall-clock/admission/DB timing. |
| Look up `index_applied_snapshot` by identity tuple | If a row exists with the SAME identity AND `applied_generation = current_generation` -> NO-OP. |
| Identity matched but canonical advanced | If matching rows exist but ALL have `applied_generation < current_generation` -> NORMAL reconcile against the newer generation (NOT a no-op). On success APPEND a row whose `applied_generation` is the POST-application generation: the NEW generation if the reconcile actually mutated canonical state, else the UNCHANGED `current_generation`. |
| No matching identity | Normal reconcile; on success APPEND a row whose `applied_generation` is the POST-application generation (the new generation if it mutated, else the unchanged `current_generation`). |
| NO-OP | No canonical write, no journal event, no generation bump. Set `index_admission.status='NOOP'`. |
| History | Rows are NEVER UPDATEd to a newer generation; each application appends a new row (no upsert-latest). |

> `DERIVED` "Same snapshot" = same snapshot identity AND same canonical generation
> observed at load (GATE1B-SAFE-RECONCILE Sec 2.4). `FACT`.

> `DERIVED` (Architect, PR #43 review #3 / round 2) — the identity is
> (`kind`, `namespace`, `version`, `value`); all four enter uniqueness (doc A
> `C-AS1`). Identical observed content at a different `observed_at` yields the
> SAME identity. Adapter E maps concrete sources later. `FACT`.

> `DERIVED` (Architect, PR #43 review round 2 / round 3) — identity match alone
> does NOT imply NO-OP. If canonical advanced while the same content was
> re-collected, reconcile again at the newer generation and append history
> (doc A G14). But a re-reconcile produces a NEW generation ONLY if it actually
> mutates canonical state (Gate 1B frozen); a ZERO-mutation re-reconcile keeps the
> generation, yet still records the application at the unchanged `current_generation`
> so the NEXT identical collection is a NO-OP (doc A G16, `C-AS3`). `FACT`.

---

## 4. Stale / out-of-order input (Scenario 6 / IO2, IO4)

`DERIVED` semantic; `PROPOSED` mechanism.

Definitions:

- `applied_max` = max `admission_seq` with `status='APPLIED'` for the root
  (equivalently a `latest_applied_admission_seq` high-water mark on `index_root`;
  `CANDIDATE`).
- `head_seq` = min `admission_seq` with `status='PENDING'` (non-terminal) for the
  root (doc A `C-A5`).
- `own_seq` = this input's `admission_seq`.

| Condition | Classification | Action |
|-----------|----------------|--------|
| `own_seq <= applied_max` AND snapshot is a duplicate of an applied input | `NOOP` (IO3) | no mutation |
| `own_seq <= applied_max` AND NOT a duplicate | `STALE_INPUT` (IO4) | no canonical mutation; record `index_reconcile_result.outcome='STALE_INPUT'` |
| `own_seq = head_seq` AND snapshot a duplicate at the SAME generation | `NOOP` (IO3) | no mutation; `status='NOOP'`; head becomes terminal, releasing the next sequence |
| `own_seq = head_seq` AND not a duplicate | normal | process against current generation (IO5); on success `status='APPLIED'` |
| `own_seq > head_seq` (ANY lower non-terminal exists, free OR actively claimed with unexpired lease) | ordering / absolute FIFO | This input MUST NOT be claimed, processed, or committed. The lower `head_seq` must first reach a terminal status (reclaim it per Sec 1.3, or if it is `FAILED`/`STALE_INPUT`, then proceed). No leapfrog (IO5/IO6, RC1/RC4). |
| a lower input stranded `PENDING` after process death, a newer input present | recovery / IO4 | the newer input MUST NOT overwrite canonical state on behalf of the older; reclaim the older head with the SAME `admission_seq`, or mark it `STALE_INPUT` (Sec 1.3 RC5, T-AT7). |

> `DERIVED` An older input MUST NOT overwrite canonical state committed by a newer
> admitted input (IO2). `FACT`.
>
> `INFERENCE` Because Stage 1 assigns `admission_seq` under the same per-root
> serialization, processing only `head_seq` (R2) makes IO5/IO6 hold by
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
| T-AT5 | The `index_admission` row for a failed reconcile is set to `FAILED` (terminal) in a SEPARATE, later transaction, so the failure is recorded without violating atomicity. This matches doc A `index_admission.status` and the Section 6/11 state machine. | `PROPOSED` |
| T-AT6 | A `PENDING` input stranded by process death is reclaimed via lease expiry / claim release with the SAME `admission_seq` (Sec 1.3); the root is never head-of-line blocked forever. | `DERIVED` (PR #43 review #2) |
| T-AT7 | If an older `admission_seq` is stranded while a newer one is processed, the newer MUST NOT silently write over it: the older is reclaimed (T-AT6) or classified `STALE_INPUT` per IO4. | `DERIVED` |
| T-AT8 | Only `head_seq` (min non-terminal `admission_seq`) may be claimed/processed/committed; higher sequences wait even while the head is mid-flight. | `DERIVED` (PR #43 review round 2) |

> `INFERENCE` for T-AT5: recording the failure cannot be part of the rolled-back
> transaction; it must be a subsequent write. This is a Worker proposal.

---

## 6. Failure-mode mapping (Gate 1B Failure Model -> transaction behavior)

| Gate 1B scenario | Transaction behavior | Tag |
|------------------|----------------------|-----|
| 1 — Identity UNRESOLVED | no canonical mutation; conflict into `index_reconcile_result`; continue other entries; commit the rest | `DERIVED` |
| 2 — Snapshot PARTIAL | additive-only writes; MUST NOT touch `removal_evidence_state`/`missing_since`/`consecutive_complete_missing`; no `resource-removed`; commit | `DERIVED` |
| 3 — internal error | `ROLLBACK`; set `index_admission.status='FAILED'` in a later tx (T-AT5); no durable canonical mutation | `DERIVED` |
| 4 — same snapshot replay | NO-OP only if the same identity was already applied at the SAME `current_generation`; if canonical advanced, reconcile again and append history (Sec 3). A ZERO-mutation re-reconcile does NOT bump the generation but still records the application at the unchanged `current_generation` (so the next identical collection is a NO-OP) | `DERIVED` (PR #43 review round 3) |
| 5 — stale generation | CAS returns 0 rows -> `ROLLBACK`; retry (reload+recompute) or abort per policy | `DERIVED` |
| 6 — concurrent same-root | per-root serialization + absolute ascending `admission_seq` (`head_seq` only); exactly one commits per generation; no leapfrog over an in-flight head | `DERIVED` (PR #43 review round 2) |
| 7 — commit failure | `ROLLBACK`; previous truth preserved | `DERIVED` |
| 8 — worker crash after admission | input durably `PENDING`; reclaimed by lease expiry with the SAME `admission_seq` (Sec 1.3, T-AT6); a higher input still waits (absolute FIFO) | `DERIVED` |

> `DERIVED` Consolidated guarantee: any uncommitted reconcile MUST NOT destroy
> previous canonical truth (GATE1B-SAFE-RECONCILE Sec 2.8; INV-013). `FACT`.

---

## 7. Isolation level and MVCC

| Aspect | Recommendation | Tag |
|--------|----------------|-----|
| Isolation level | `READ COMMITTED` with explicit row locks / CAS; `SERIALIZABLE` is an alternative but heavier | `CANDIDATE` |
| Consumers | read committed snapshot at a generation; never blocked by the writer (MVCC) | `DERIVED` (GATE1A C2.2) |
| Canonical read during reconcile | read at `current_generation` loaded in R3; the generation validation in R11 (CAS on mutation; compare-and-check on zero mutation) rejects if it changed | `DERIVED` (PR #43 final verification) |

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
| 5a | Absolute per-root FIFO (head-of-line, no leapfrog) | Sec 1.2 R2, Sec 1.3 RC1-RC7, Sec 4 | `COVERED` |
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
| Failure recording transaction shape | `PROPOSED` (defined) | T-AT5: a later tx sets terminal `FAILED`. Sec 5. |
| Admission lease duration / reclaim trigger | `CANDIDATE` | Correctness fixed by Sec 1.3 / doc A `C-A4`/`C-A5`; exact timing is operational config. |
| Idempotency application-history retention / pruning | `CANDIDATE` | History rows retained for audit/replay; pruning policy is operational. Sec 3. |
| Two-stage vs single-transaction admission+reconcile | `CANDIDATE` | Sec 1; both satisfy IO1-IO7, two-stage matches IO1 wording more literally. |

---

## 11. Golden cases (transaction-level)

| # | Case | Expected transaction behavior |
|---|------|-------------------------------|
| T1 | Two concurrent same-root reconciles | per-root lock serializes; absolute ascending admission order; each sees the other's committed generation; no double commit. |
| T2 | CAS failure | 0-row UPDATE -> rollback -> retry/abort; no durable mutation. |
| T3 | Replay at the same generation | identity match AND `applied_generation = current_generation` -> NO-OP; no generation bump. |
| T4 | Stale input | `own_seq <= applied_max` non-duplicate -> `STALE_INPUT`; no mutation. |
| T5 | Internal error mid-reconcile | rollback; `index_admission` set `FAILED` in a later tx. |
| T6 | Confirmed removal | tombstone UPDATE + `resource-removed` INSERT + generation CAS + applied record, one commit. |
| T7 | DELETED root | R4 rejects before any write. |
| T8 | Worker crash leaves the lowest input `PENDING` | No process holds the claim; a later worker reclaims with the SAME `admission_seq` (Sec 1.3) and completes the reconcile; the root is never permanently blocked. |
| T9 | Two different roots reconcile concurrently | Both commit independently; no global ordering token is consulted; consumers use a per-root cursor vector (doc A Sec 3.9). |
| T10 | Older `PENDING` stranded while a newer input arrives | The newer MUST NOT commit over the older; the older is reclaimed (T-AT6) or becomes `STALE_INPUT` (IO4, T-AT7). |
| T11 | A lower `admission_seq` is mid-flight (claimed, lease unexpired); a higher input is present | The higher input is NOT selected/claimable; R2 selects only `head_seq`. No leapfrog (RC1/RC4, T-AT8). |
| T12 | Same `snapshot_identity` re-collected after canonical advanced G -> G+1 | NOT a NO-OP: reconcile against G+1. If it MUTATES canonical state, bump G+1 -> G+2 and APPEND a row (`applied_generation = G+2`); if it causes NO mutation, keep G+1 and still APPEND a row (`applied_generation = G+1`, post-application generation). Either way the earlier row is retained and never UPDATEd (doc A G14, G16). |
| T13 | All-roots journal read request | Per-root ordering is guaranteed; no cross-root canonical order is asserted or relied upon (doc A Sec 3.9, doc C Sec 5). |
| T14 | Root transitions to `DELETED` while child resources are still `PRESENT` | ROOT-level tombstone only: `index_root.lifecycle_state='DELETED'` (at most a `root-deleted` journal event). Child resources keep their last committed presence (some remain `PRESENT`); NO bulk `resource_presence='REMOVED'` and NO `resource-removed` events. Further reconcile of that root is rejected by R4 (doc A G17, `C-R4`). |

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
