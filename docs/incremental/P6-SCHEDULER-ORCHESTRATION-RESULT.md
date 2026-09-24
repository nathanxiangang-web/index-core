# Incremental P6 — Scheduler Orchestration Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #82 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P6-SCHEDULER-ORCHESTRATION-PROTOTYPE.md`
>
> Planning PR: #81 · Predecessor: P5 bounded executor loop — Issue #79 / PR #80 — ARCHITECT_ACCEPTED (merge `88e3e8f`)
>
> **MANUAL FINITE CYCLE ONLY · MAX_DUE_WATCH_ATTEMPTS <= 5 · MAX_EXECUTE_ITEMS <= 5 · MAX_WALL_TIME <= 60s · SERIAL ONLY · NO PRODUCTION SCHEDULER · NO TICKER/CADENCE · NO CONTINUOUS DAEMON · NO AUTO RECOVERY · NO API/CLI · NO MIGRATION**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Orchestration API

Package `internal/runtime/incrementalorch`:

```go
type DueWatchStore interface {
    ListDueWatches(ctx context.Context, now time.Time, limit int) ([]state.ScopeWatchState, error)
    EmitDuePoll(ctx context.Context, rootID, scopeKey string, expectedWatchVersion int64, now time.Time) (state.DirtyScopeWork, error)
}

type ExecutorCycle interface {
    Run(ctx context.Context, cfg incrementalexec.CycleConfig) (incrementalexec.CycleResult, error)
}

type Config struct {
    MaxDueWatchAttempts int
    MaxExecuteItems     int
    MaxWallTime         time.Duration
}

type StopReason string
const (
    StopCompleted            StopReason = "COMPLETED"
    StopMaxWallTime          StopReason = "MAX_WALL_TIME"
    StopContextCancelled     StopReason = "CONTEXT_CANCELLED"
    StopMaterializationError StopReason = "MATERIALIZATION_ERROR"
    StopExecutorError        StopReason = "EXECUTOR_ERROR"
)

type Result struct {
    StartedAt, FinishedAt, ObservedAt time.Time
    StopReason                        StopReason
    DueCandidates, DueAttempted       int
    DueEmitted, DueStale              int
    MoreDueWatches                    bool
    MaterializationInterrupted        bool
    ExecutorRan                       bool
    Executor                          incrementalexec.CycleResult
}

func NewRunner(store DueWatchStore, exec ExecutorCycle, now func() time.Time) (*Runner, error)
func (r *Runner) RunCycle(ctx context.Context, cfg Config) (Result, error)
```

P6 **reuses** the accepted P3 `ListDueWatches` / atomic `EmitDuePoll` and the
accepted P5 `CycleRunner`. It does not copy the watch due SQL, the EmitDuePoll
transaction, DirtyScopeWork merge semantics, P4 execution, or the P5 item stop
matrix. `*postgres.Store` satisfies `DueWatchStore`; `*incrementalexec.CycleRunner`
satisfies `ExecutorCycle`.

## 2. Hard bounds

```text
1 <= MaxDueWatchAttempts <= 5
1 <= MaxExecuteItems     <= 5
0 <  MaxWallTime         <= 60s
```

Invalid config fails before the due-watch query, `EmitDuePoll`, and P5 (zero
Store/P5 work). No zero/unbounded sentinel exists. These are prototype safety
caps, not production SLA or polling cadence.

## 3. Fixed observed_at

After validation the runner captures `observed_at = now()` exactly once and uses
that instant for the due query and for every `EmitDuePoll` attempt in the cycle.
`now()` is not refreshed per watch. `ListDueWatches(observed_at, MaxDueWatchAttempts+1)`;
only the first `MaxDueWatchAttempts` rows are attempted and the optional extra row
is observation only.

## 4. Due counts / backlog

- `DueCandidates` — size returned by the bounded `limit+1` query, not global backlog;
- `DueAttempted <= MaxDueWatchAttempts <= 5` — attempts, not successful emissions;
- `DueEmitted <= DueAttempted`;
- `DueStale <= DueAttempted`;
- `MoreDueWatches` — at least one due row existed beyond this cycle's attempt budget;
- a stale CAS attempt consumes budget and no replacement row is pulled in;
- no requery / paging.

Ordering remains Store-owned: `next_due_at ASC, root_id ASC, scope_key ASC`.

## 5. Materialization stop matrix

| Situation | Counts | StopReason | Cycle error | Next |
|---|---|---|---|---|
| `EmitDuePoll` success | attempted+1, emitted+1 | continue | — | next snapshot row |
| `ErrStateCASConflict` | attempted+1, stale+1 | continue | — | next snapshot row (no retry/re-read/re-list) |
| any other `EmitDuePoll` error | attempted+1 | `MATERIALIZATION_ERROR` | non-nil | stop; **no P5** |
| P6 deadline interrupts `EmitDuePoll` | attempted+1 | `MAX_WALL_TIME` | nil | stop; `MaterializationInterrupted=true`; no retry; no P5 |

Partial materialization is durable: P6 creates no outer transaction across
watches, so already-committed emissions stay committed and are never compensated.
Error classification is structural (`errors.Is(..., postgres.ErrStateCASConflict)`,
context cause); no error strings are parsed.

## 6. Overall wall-time

One derived context `orch_ctx = context.WithTimeout(parent, MaxWallTime)`.
Boundary precedence before every new phase/operation:
`parent cancellation > P6 wall deadline > operation`. When the wall budget is
already exhausted, no further Store or P5 call starts.

- read-only `ListDueWatches` cancelled by `orch_ctx` → `MAX_WALL_TIME`, nil error;
- `EmitDuePoll` interrupted by `orch_ctx` → `MAX_WALL_TIME`, nil error,
  `MaterializationInterrupted=true`, no retry, no P5; the next manual cycle must
  re-read persisted state (the caller must not infer the final watch's commit
  outcome from in-memory counters).

`orch_ctx` is passed as the parent of the nested P5 cycle so the overall deadline
dominates any later P5 child deadline.

## 7. Execution phase and P5 translation

P5 runs **exactly once** after materialization completes, even when
`DueEmitted == 0`, so preexisting eligible PENDING work can drain. It is invoked
with `CycleConfig{MaxItems: MaxExecuteItems, MaxWallTime: MaxWallTime}`.

| P5 outcome | P6 translation |
|---|---|
| nil error | `COMPLETED` (nil error) — but **parent cancellation is checked first**; an already-expired `orch_ctx` yields `MAX_WALL_TIME` |
| parent cancellation | `CONTEXT_CANCELLED` + parent context error |
| P6-owned deadline + context-cause error | `MAX_WALL_TIME` + nil error; nested result retained incl. `InterruptedInFlight` |
| unrelated systemic error | `EXECUTOR_ERROR` + non-nil error |

The full nested P5 `CycleResult` is retained. An unrelated P5 error is never
hidden merely because wall time also expired: only an error carrying the context
cause is treated as the deadline. P5 is never run a second time.

Parent cancellation is evaluated before the P6-owned deadline on **every**
translation path — including a nil P5 return — because `orch_ctx` is a child of
the parent context: without that ordering a caller cancellation racing a nil P5
return would be misreported as budget exhaustion.

## 8. Real PostgreSQL evidence

`TestP6RealPGWatchToCanonical` — real PostgreSQL + real `ListDueWatches` /
`EmitDuePoll` + real P5 `CycleRunner` + real P4 executor + real
`scan.Service.ScanScope` + httptest AList, one HOT due watch on an ACTIVE root:

```text
watch due
  -> one POLL_SCHEDULE signal
  -> watch last_due_at == observed_at, next_due_at advanced
  -> Work claimed -> exactly one refresh=true request
  -> PARTIAL Snapshot admitted/applied -> Canonical /a.txt PRESENT
  -> Work VERIFIED -> watch success bookkeeping
  -> DueEmitted=1, DueStale=0, StopReason=COMPLETED
```

Provider refresh count = 1 for the one-watch path. The test also asserts the full
watch execution attribution — `last_attempt_started_at == observed_at`,
`last_attempt_finished_at >= observed_at`, `last_success_at == observed_at`,
`consecutive_failures == 0`, `last_error_class == nil` — proving
`POLL_SCHEDULE -> ClaimWork -> CompleteSuccess -> watch health` actually ran, and
`Work.last_verified_signal_seq >= 1` proving the poll signal survived into
claim-scoped verification.

Additional real-PG evidence:

- `TestP6RealPGNoDueWatchDrainsExistingPending` — no due watch still runs P5 once
  and drains a preexisting PENDING item (one refresh, Canonical visible).
- `TestP6RealPGWallDeadlineDuringNestedP5Claim` — the P6 deadline cancels a nested
  claimed item: `MAX_WALL_TIME`, nil error, nested `InterruptedInFlight=true`,
  Work stays `IN_FLIGHT`, external `RecoverStaleInflight` requeues it (P6 never
  recovers).
- `TestP6RealPGDuePollIntoRetryWaitNotPromoted` — a poll merges into a RETRY_WAIT
  row without promoting it, while independent PENDING work still executes.
- `TestP6RealPGDuePollIntoBlockedNotPromoted` — the same for BLOCKED: the poll
  merges, the row stays BLOCKED with an unchanged attempt count, and an
  independent PENDING row still executes in the same cycle.
- `TestP6RealPGScopedAbsenceCreatesNoRemovalEvidence` — a later PARTIAL scoped
  refresh that omits a known canonical child `/old.txt` leaves `/old.txt`
  PRESENT with `removal_evidence_state=NONE`, `missing_since=NULL`, and an
  unchanged complete-missing count (true scoped absence, not a resource that the
  provider still returns).
- `TestP6RealPGExistingPendingCoalescing` — an existing PENDING epoch plus a poll
  coalesces to exactly one extra `signal_seq` and is satisfied by exactly one
  execution (no lost wakeup).

## 9. Unit evidence

`orchestrator_test.go` proves config bounds (zero Store/P5 work on invalid config),
no-due-work still runs P5, the due snapshot hard bound (`limit=attempts+1`, only
five attempts, `MoreDueWatches=true`, no sixth emission), one fixed `observed_at`
across query and every `EmitDuePoll`, CAS-stale skip + continue, stale consumes
budget with no replacement, fatal materialization preserves committed work and
skips P5, wall budget before the next operation, wall budget interrupting
`EmitDuePoll`, parent cancellation during materialization and during execution,
executor systemic error, nested P5 deadline, nil-P5-error-but-expired-own-deadline,
a nil P5 return with parent cancellation (`CONTEXT_CANCELLED`, not
`MAX_WALL_TIME`), bounded nested P5 config, and strict serial execution (max
concurrent = 1).

## 10. Changed production files

```text
internal/runtime/incrementalorch/config.go        (new)
internal/runtime/incrementalorch/orchestrator.go  (new)
```

New tests:

```text
internal/runtime/incrementalorch/orchestrator_test.go  (18)
internal/runtime/incrementalorch/integration_test.go   (7)
```

No `internal/store/postgres/**`, no `internal/runtime/incrementalexec/**`, no
`internal/runtime/scan/**`, no `internal/collector/**`, no
`internal/runtime/app/**`, no `internal/runtime/worker/**`, no `cmd/**`, no
`internal/transport/**`, no `internal/query/**`, no `internal/kernel/**`, no
`internal/domain/**`, no migration, and no `go.mod`/`go.sum` change.

## 11. Regression results

```text
gofmt -l <Go files>             clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

P6 tests = **25** (unit 18 + integration 7). Existing P3/P4/P5 tests remain green.

## 12. Boundary statement

P6 is an in-process finite, manually-invoked orchestration primitive, not a
scheduler. It adds no ticker, cron/cadence loop, sleep/wait-until-due, background
goroutine scheduler, continuous daemon/service, concurrent orchestration cycles,
worker pool/parallel watch emission, automatic
`RetryReady`/`ResumeSuspended`/`RepairBlocked`/`RecoverStaleInflight`, Mutation
Hint API, production sync CLI, public HTTP scheduling API, native delta/provider
cursor, direct 115 client, destructive delta/removal, new migration, persistent
P6 run-history table, API/CLI change, or Gate 5. P6 is not wired into `worker`,
`app`, any command, or any HTTP handler. `RunCycle` is strictly serial and P6 does
not authorize concurrent invocation of the same `Runner`; a future
scheduler/orchestrator must serialize calls. P6 does not override the P2/P3 merge
behavior (VERIFIED opens a new PENDING epoch; PENDING coalesces poll provenance;
IN_FLIGHT takes post-claim pending; RETRY_WAIT/BLOCKED/SUSPENDED merge without
promotion).

`FROZEN_CONTRACT_CHANGES: NONE`
## 13. Round 1 rework (Issue #82 review)

One correctness blocker + three evidence blockers + one evidence closeout from
the P6 Round 1 review were fixed strictly inside
`internal/runtime/incrementalorch/**`, the P6 tests, and this result document:

1. **Parent-cancellation precedence after a nil P5 return.** The execution phase
   now checks `ctx.Err()` **before** `orchCtx.Err()` in the nil-error branch, so a
   caller cancellation racing a nil P5 return is `CONTEXT_CANCELLED` with the
   parent error instead of `MAX_WALL_TIME`. Proved by
   `TestP6NilP5ErrorWithParentCancellationIsContextCancelled`.
2. **Watch success-bookkeeping evidence.** `TestP6RealPGWatchToCanonical` now
   asserts `last_attempt_started_at`, `last_attempt_finished_at`,
   `last_success_at`, `consecutive_failures == 0`, `last_error_class == nil`, and
   `Work.last_verified_signal_seq >= 1`, proving the full
   `POLL_SCHEDULE -> ClaimWork -> CompleteSuccess` attribution path and that the
   poll signal survives into claim-scoped verification.
3. **BLOCKED no-auto-promotion evidence.**
   `TestP6RealPGDuePollIntoBlockedNotPromoted` proves a poll merges into a BLOCKED
   row without promoting it or executing it (attempt count unchanged) while
   independent PENDING work still executes.
4. **Actual scoped-absence evidence.**
   `TestP6RealPGScopedAbsenceCreatesNoRemovalEvidence` replaces the
   present-resource check with a real absence: a later PARTIAL scoped refresh
   omits a previously canonical `/old.txt`, which stays PRESENT with
   `removal_evidence_state=NONE`, `missing_since=NULL`, and an unchanged
   complete-missing count.

`FROZEN_CONTRACT_CHANGES: NONE`