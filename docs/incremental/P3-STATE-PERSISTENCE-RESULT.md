# Incremental P3 — State Persistence Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #72 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P3-STATE-PERSISTENCE-PROTOTYPE.md`
>
> Predecessor: P2 durable scope-state design — Issue #69 / PR #70 — ARCHITECT_ACCEPTED
>
> **NO PRODUCTION SCHEDULER · NO DIRTY EXECUTOR · NO API/CLI**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Migration / schema summary

New SQL-first migration (additive only, no Canonical table altered, no provider
trigger):

`internal/store/postgres/migrations/0005_incremental_scope_state.sql`

Creates exactly two operational tables referencing `index_root(root_id)`:

- `index_scope_watch_state` — recurring watch policy / cadence / next due.
  - PK `(root_id, scope_key)`; `version >= 1`; counters `>= 0`.
  - HOT/WARM require a positive `effective_interval_seconds` **and** non-NULL
    `next_due_at`; COLD/DISABLED require both NULL.
  - `source_set text[]` restricted to watch-policy provenance; scope-key CHECK.
  - Partial due index `(watch_state, next_due_at, root_id, scope_key)` for HOT/WARM.
- `index_dirty_scope_work` — durable coalesced verification intent.
  - PK `(root_id, scope_key)`; `signal_seq >= 1`; `version >= 1`; counters `>= 0`.
  - **P2 bucket invariants enforced in the DB**: `claimed_*` present as one group
    **iff** IN_FLIGHT; VERIFIED ⇒ claim absent + pending empty;
    PENDING/RETRY_WAIT/BLOCKED/SUSPENDED ⇒ claim absent + pending source non-empty;
    IN_FLIGHT ⇒ claimed source non-empty; `RETRY_WAIT` ⇒ `pending_not_before`
    non-NULL; empty pending ⇒ empty metadata; non-empty pending ⇒
    `pending_first_seen_at` non-NULL.
  - `claimed_signal_seq <= signal_seq`; `last_verified_signal_seq <= signal_seq`.
  - `source_set`/`reason_set` as `text[]` restricted to the frozen enums.
  - Eligible index on `(work_state, pending_not_before, pending_priority,
    pending_first_seen_at, root_id, scope_key)`; IN_FLIGHT recovery index.

No custom PostgreSQL ENUM types were added (constrained text/text[] only).

## 2. Store primitives implemented

Watch (`incremental_watch.go`):

- `CreateWatch`, `GetWatch`, `CASUpdateWatch(expectedVersion)`, `ListDueWatches(now, limit)`.
  Due listing returns only **ACTIVE-root HOT/WARM** not deferred, deterministic order.

Dirty work (`incremental_work.go`, `incremental_transitions.go`):

- `MergeSignal` — exactly one trigger per call; P2 merge rules; bounded retry on insert race.
- `EmitDuePoll(rootID, scopeKey, expectedWatchVersion, now)` — atomic due-poll transaction.
- `ClaimWork` — atomic pending→claimed snapshot.
- `CompleteSuccess(claimedSignalSeq)` — P2 success + partial-success watermark.
- `CompleteFailure(claimedSignalSeq, class, retryNotBefore)` — frozen failure mapping.
- `BudgetDefer(expectedVersion)` — PENDING-only eligibility move, no attempt/failure.
- `RetryReady`, `ResumeSuspended`, `RepairBlocked` — explicit state transitions.
- `GetWork`.

Recovery (`incremental_recovery.go`):

- `RecoverStaleInflight(rootID, now)` — re-coalesces stale IN_FLIGHT; deterministic/idempotent.

Operational-state error: **`ErrStateCASConflict`** (distinct from Canonical `ErrCASConflict`);
stale-claim error: `ErrStaleClaim`.

Provider-neutral types: `internal/incremental/state/{model,scope,validate}.go`
(WatchState, WorkState, Priority, TriggerSource, TriggerReason, ErrorClass,
ScopeWatchState, DirtyScopeWork, DirtySignal; scope-key validation; set
normalization = validate + de-duplicate + deterministic sort).

## 3. Changed production files

```text
internal/store/postgres/migrations/0005_incremental_scope_state.sql   (new)
internal/incremental/state/model.go                                   (new)
internal/incremental/state/scope.go                                   (new)
internal/incremental/state/validate.go                                (new)
internal/store/postgres/incremental_state.go                          (new)
internal/store/postgres/incremental_watch.go                          (new)
internal/store/postgres/incremental_work.go                           (new)
internal/store/postgres/incremental_transitions.go                    (new)
internal/store/postgres/incremental_recovery.go                       (new)
internal/store/postgres/incremental_test.go                           (new)
docs/incremental/P3-STATE-PERSISTENCE-RESULT.md                       (new)
```

**No** file under `cmd/`, `internal/runtime/app/`, `internal/runtime/worker/`,
`internal/runtime/scan/`, `internal/transport/`, `internal/query/`,
`internal/kernel/`, or `internal/domain/` was modified.

## 4. Real-PostgreSQL evidence (PostgreSQL 18, `testutil.Pool`)

P3 test files — **38 tests total, all PASS**:

| file | tests |
| --- | --- |
| `incremental_test.go` | 20 |
| `incremental_round1_test.go` | 5 |
| `incremental_round2_test.go` | 9 |
| `incremental_round3_test.go` | 3 |
| `incremental_rollback_internal_test.go` | 1 |

| Area | Evidence |
| --- | --- |
| Migration | empty-DB reproducible, idempotent rerun, pre-0005 upgrade recreates the two tables only |
| Watch CAS | create/read round-trip; stale CAS → `ErrStateCASConflict` with **no partial write** |
| Due selection | only ACTIVE HOT/WARM due + non-deferred; COLD/DISABLED/inactive/deferred excluded; deterministic limit/order |
| Signal merge | absent ⇒ PENDING seq=1; PENDING union + min eligibility; VERIFIED ⇒ clean new epoch; inactive root ⇒ SUSPENDED; invalid scope/source rejected |
| Concurrent merge | 25 goroutines ⇒ one physical row, `signal_seq == 25` (no lost increments) |
| Atomic due-poll | work merge + watch advance commit together; stale watch version ⇒ CAS conflict, no second signal; two concurrent emits from the same version ⇒ exactly one signal |
| Claim | pending→claimed atomic move; pending cleared; post-claim signal never modifies `claimed_*`; missing watch ⇒ rollback; inactive root ⇒ suspend |
| Success | VERIFIED path; signal-7 claim + signal-8 merge ⇒ PENDING keeping only signal-8 pending + watermark=7; stale claim rejected; watch attribution claim-scoped (hint-only success does not touch Watch; claimed POLL_SCHEDULE does) |
| Failure | transient ⇒ RETRY_WAIT + eligibility, provenance re-coalesced, oldest first_seen preserved, counters incremented; auth ⇒ BLOCKED; root-inactive ⇒ SUSPENDED with **no** provider-failure increment; retry backoff preserved across merges |
| Budget defer | PENDING stays PENDING, later eligibility, no attempt/failure/error mutation |
| Recovery | stale IN_FLIGHT ACTIVE ⇒ PENDING, non-ACTIVE ⇒ SUSPENDED; provenance re-coalesced; `signal_seq` preserved; crash not counted as failure; **second run is a no-op (idempotent)** |
| DB constraints | direct invalid SQL rejected: PENDING empty pending, IN_FLIGHT without claim, RETRY_WAIT without `pending_not_before`, trailing-slash scope key |

## 5. Concurrency / CAS / transaction evidence

- **CAS**: `CASUpdateWatch` and every optimistic transition use `WHERE ... version = expected`
  with `0 rows ⇒ ErrStateCASConflict`; stale CAS leaves zero partial write (verified).
- **Concurrency**: dirty-signal merge is serialized by `SELECT ... FOR UPDATE`; the
  atomic due-poll uses `SELECT ... WHERE version = $expected FOR UPDATE`, so two
  concurrent calls with the same watch version yield exactly one poll signal.
- **Atomicity / rollback**: due-poll and claim run inside one PostgreSQL transaction;
  the claim rollback path (missing watch) leaves the row `PENDING` with no partial move.
- **Watch attribution**: Watch counters/attempt fields change only when
  `POLL_SCHEDULE ∈ claimed_source_set`.

## 6. Regression

```text
gofmt                                 clean
go vet ./...                          clean
go test -p 1 ./...                    all packages PASS
```

Real PostgreSQL 18 (`localhost:55432`). No live 115/OpenList test was run (not required in P3).

## 7. Boundary statement

This prototype adds **only** operational-state persistence:

- **no** production scheduler / polling loop;
- **no** long-running dirty executor;
- **no** `ScanScope` call or background worker loop;
- **no** `serve`/CLI/HTTP surface, no Mutation Hint API;
- **no** native delta/provider cursor; **no** direct 115 client;
- **no** destructive removal;
- **no** Q1–Q9, Kernel, Journal, Canonical schema/semantics change;
- **no** multi-daemon HA / distributed leases / fencing.

`FROZEN_CONTRACT_CHANGES: NONE`.

## 8. P3 exit decision

Reserved for the Architect:

```text
STOP
AUTHORIZE_ONE_SHOT_DIRTY_EXECUTOR_PROTOTYPE
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```
## 9. Round 1 Architect review rework (2026-09-24)

PR #73 Round 1 = CHANGES REQUIRED. All 8 points addressed, still P3-scope only (no
scheduler / executor / API / CLI / `ScanScope` / provider):

1. **Frozen lock order Root → Watch → Work.** Every multi-row operational transaction acquires
   locks in that order: `MergeSignal`, `EmitDuePoll`, `ClaimWork`, `CompleteSuccess`,
   `CompleteFailure` lock the root row, then the watch row (if any), then the work row. This
   removes the `EmitDuePoll` (Watch→Work) vs `Claim`/`Complete` (Work→Watch) deadlock.
2. **No root-lifecycle TOCTOU.** Root lifecycle is read under the root `FOR UPDATE` lock in every
   state-changing transaction; `ResumeSuspended` is transactional too.
3. **Inactive-root claim preserves eligibility.** `ClaimWork` on a non-ACTIVE root moves the item
   to `SUSPENDED` **without clearing `pending_not_before`**.
4. **Recovery preserves the earliest eligibility.** New `claimed_not_before` snapshot (migration
   0005); recovery/failure re-coalesce uses
   `pending_not_before = min(claimed_not_before, pending_not_before)`, so a post-claim future
   `not_before` cannot delay an already-due claimed signal.
5. **DB fail-closed completeness.** `last_error_class` now has a closed-enum CHECK on both tables;
   `c_dsw_claim_group` requires a non-empty claimed reason set; new `c_dsw_pending_complete`
   rejects a source-only pending bucket (reason + priority required).
6. **Normalized Store reads.** Every read runs through `state.Normalize*` (validate + dedupe +
   deterministic sort), so reads return normalized sets, not raw DB array order.
7. **No tight retry.** `CompleteFailure` rejects `retryNotBefore <= now`.
8. **Atomic due-poll rollback proof.** A test-only seam (`emitDuePollAfterMergeHook`) injects a
   failure after the work merge; the internal test `TestP3EmitDuePollRollsBackBothHalves` proves
   neither the work merge nor the watch advance is visible.

New tests: `incremental_round1_test.go` (DB fail-closed, normalized reads, immediate-retry
rejection, recovery eligibility preservation, inactive-claim eligibility) and
`incremental_rollback_internal_test.go` (due-poll rollback). Full `go test -p 1 ./...` green,
`go vet` / `gofmt` clean, real PostgreSQL 18. `FROZEN_CONTRACT_CHANGES: NONE`.
## 10. Round 2 Architect review rework (2026-09-24)

PR #73 Round 2 = CHANGES REQUIRED. All 7 points addressed, P3-scope only:

1. **Recovery lock order fixed.** `RecoverStaleInflight` now locks the root **before** scanning/locking
   work rows (Root -> Work), removing the last reverse-order path.
2. **Lifecycle gate on every runnable transition.** `RetryReady`, `ResumeSuspended` and
   `RepairBlocked` now run in a transaction that locks the root, requires ACTIVE, and only then moves
   the item to PENDING (lock order Root -> Watch -> Work).
3. **Recovery NULL-eligibility fixed.** A claimed signal with no barrier (`claimed_not_before IS NULL`)
   stays immediately runnable after recovery; a post-claim future `not_before` can no longer delay it.
4. **Failure keeps the later barrier.** `CompleteFailure -> RETRY_WAIT` now uses
   `pending_not_before = max(pending_not_before, retryNotBefore)` (never earlier).
5. **DB watermark lower bound.** `last_verified_signal_seq` now has
   `>= 1 AND <= signal_seq` (`c_dsw_verified_signal_range`).
6. **`last_seen_at` monotonic.** Same-epoch merges use `max(last_seen_at, sig.SeenAt)` (never regress).
7. **Concurrency / upgrade evidence added** (new real-PG tests):
   `TestP3EmitDuePollVsClaimNoDeadlock`, `TestP3TransitionRootLifecycleRaceNoDeadlock`,
   `TestP3PreMigrationUpgradePreservesData`, `TestP3RunnableTransitionsSucceedOnActiveRoot`,
   `TestP3LifecycleGateOnRunnableTransitions`, `TestP3RecoveryNullClaimEligibilityNotDelayed`,
   `TestP3CompleteFailureKeepsLaterBarrier`, `TestP3LastSeenAtNeverRegresses`,
   `TestP3DBVerifiedWatermarkLowerBound`.

Full `go test -p 1 ./...` green; `go vet` / `gofmt` clean; real PostgreSQL 18.
`FROZEN_CONTRACT_CHANGES: NONE`.
## 11. Round 3 Architect review rework (2026-09-24)

PR #73 Round 3 = CHANGES REQUIRED. Two contract fixes + evidence closeout:

1. **New-epoch `last_seen_at`.** Same-epoch merges still use
   `max(last_seen_at, sig.SeenAt)` (never regress); a **VERIFIED → new-epoch** merge now sets
   `last_seen_at = sig.SeenAt` exactly — it no longer inherits the previous epoch's value.
   Test: `TestP3NewEpochLastSeenNotInherited`.
2. **`ClaimWork` stale-version CAS.** Signature is now
   `ClaimWork(ctx, rootID, scopeKey, expectedWorkVersion, now)`; if the row's version differs the
   call fails with `ErrStateCASConflict` and leaves **Work and Watch unmodified** — matching the
   next-stage executor flow `select work → version → claim`.
   Tests: `TestP3ClaimStaleVersionCAS`, `TestP3ClaimStaleVersionLeavesWatchUntouched`.
3. **Evidence counts.** P3 tests now total **38** (20 + 5 + 9 + 3 + 1), as recorded in §4.

Full `go test -p 1 ./...` green; `go vet` / `gofmt` clean; real PostgreSQL 18.
`FROZEN_CONTRACT_CHANGES: NONE`.