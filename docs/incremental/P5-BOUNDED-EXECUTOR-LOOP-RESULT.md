# Incremental P5 — Bounded Executor Loop Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #79 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P5-BOUNDED-EXECUTOR-LOOP-PROTOTYPE.md`
>
> Predecessor: P4 one-shot dirty executor — Issue #75 / PR #76 — ARCHITECT_ACCEPTED (merge `10668d6`)
>
> **MAX_ITEMS <= 5 · MAX_WALL_TIME <= 60s · SERIAL EXECUTION ONLY · NO PRODUCTION SCHEDULER · NO TICKER/CADENCE · NO CONTINUOUS DAEMON · NO API/CLI · NO MIGRATION**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Cycle API

Package `internal/runtime/incrementalexec` (`cycle.go`):

```go
type OneShotExecutor interface {
    ExecuteOne(ctx context.Context) (Result, error)
}

type CycleConfig struct {
    MaxItems    int
    MaxWallTime time.Duration
}

func (c CycleConfig) Validate() error

const (
    MaxCycleItems    = 5
    MaxCycleWallTime = 60 * time.Second
)

type StopReason string
const (
    StopNoEligibleWork      StopReason = "NO_ELIGIBLE_WORK"
    StopMaxItems            StopReason = "MAX_ITEMS"
    StopMaxWallTime         StopReason = "MAX_WALL_TIME"
    StopStaleSelection      StopReason = "STALE_SELECTION"
    StopInternalItemFailure StopReason = "INTERNAL_ITEM_FAILURE"
    StopSystemicError       StopReason = "SYSTEMIC_ERROR"
    StopContextCancelled    StopReason = "CONTEXT_CANCELLED"
)

type CycleResult struct {
    StartedAt, FinishedAt time.Time
    StopReason            StopReason
    Invocations           int
    SelectedItems         int
    Succeeded             int
    Failed                int
    Last                  Result
    InterruptedInFlight   bool
}

type CycleRunner struct{ one OneShotExecutor }
func NewCycleRunner(one OneShotExecutor) (*CycleRunner, error)
func (r *CycleRunner) Run(ctx context.Context, cfg CycleConfig) (CycleResult, error)
```

P5 **reuses** the accepted P4 `ExecuteOne` unchanged. It does not copy the
selector SQL, `ClaimWork`, typed scan classification, retry eligibility,
`ScanScope` invocation, or completion logic. `Last` retains the most recent
**selected** item (an empty no-work result never overwrites it).

## 2. Hard caps

```text
1 <= MaxItems    <= 5
0 <  MaxWallTime <= 60s
```

`CycleConfig.Validate` rejects `MaxItems` outside `[1,5]` and `MaxWallTime`
outside `(0,60s]`. An invalid config is rejected **before the first
`ExecuteOne`** and produces zero invocations. No zero/unbounded sentinel exists.

## 3. Stop matrix

| Situation | Counts | StopReason | Cycle error | Next item |
|---|---|---|---|---|
| `ErrNoEligibleWork` | — | `NO_ELIGIBLE_WORK` | nil | no (drained) |
| success (`err == nil`) | selected+1, succeeded+1 | continue | — | yes if budgets allow |
| `ErrScannerFailed` + durable class (TRANSIENT_PROVIDER / THROTTLED / AUTH_OR_PERMISSION / SCOPE_TOO_LARGE / INVALID_SCOPE / CONFIG_INVALID / ROOT_INACTIVE) | selected+1, failed+1 | continue | — | yes if budgets allow |
| `ErrScannerFailed` + `INTERNAL` | selected+1, failed+1 | `INTERNAL_ITEM_FAILURE` | non-nil | no |
| `ErrScannerFailed` + missing/unknown class | selected+1 | `SYSTEMIC_ERROR` | non-nil | no |
| `ErrStaleSelection` | selected+1 | `STALE_SELECTION` | non-nil | no (never reselects) |
| `ErrCompletionFailed` / unexpected select/claim/store/runtime error | selected preserved | `SYSTEMIC_ERROR` | non-nil | no |
| parent context cancelled | — | `CONTEXT_CANCELLED` | parent ctx error | no |
| cycle-owned wall deadline (before next item) | — | `MAX_WALL_TIME` | nil | no |
| cycle-owned wall deadline (during a claimed item) | — | `MAX_WALL_TIME` | nil | no |

Boundary precedence before every new item (deterministic):
`parent cancellation > selected_items >= MaxItems > cycle deadline`.

## 4. Count semantics

- `Invocations` — actual `ExecuteOne` calls made;
- `SelectedItems` — incremented when `Result.Selected == true` (success, classified
  failure, and stale selection all count);
- `Succeeded` — `ExecuteOne` returned nil;
- `Failed` — durably completed `ErrScannerFailed` items **with a class**;
- stale selection is selected but neither succeeded nor failed;
- completion/systemic uncertainty (including a classless scanner failure) is not a
  durably failed item.

Provider refreshes stay bounded by P4:
`refresh requests <= selected items <= MaxItems <= 5`.

## 5. Wall-time semantics

`Run` creates exactly one derived context at cycle start:

```text
cycle_ctx = context.WithTimeout(parent, MaxWallTime)
```

This is a one-cycle safety deadline, not a cadence/ticker. It cancels an
in-flight `ExecuteOne` when the budget expires. `P5` does **not** call
`RecoverStaleInflight`; a claimed item cancelled by the cycle deadline may remain
`IN_FLIGHT` and external P3 recovery owns the repair.

`InterruptedInFlight` is set **only** when the cycle deadline cancelled an
in-flight `ExecuteOne` whose `Result.ClaimedSignalSeq > 0` (a proven committed
claim). A selected-but-unclaimed item reports `false`; a wall budget that expires
*between* items also reports `false` (the previous item already completed).

## 6. Real PostgreSQL multi-item evidence

`TestP5CycleRealPGMultiItem` — real PostgreSQL + real P4 executor + real
`scan.Service.ScanScope` + httptest AList, three ACTIVE roots each with one
eligible PENDING item:

```text
Run(MaxItems=5)
  -> 4 ExecuteOne calls (3 items drained, 4th reports no work)
  -> Invocations=4, SelectedItems=3, Succeeded=3, Failed=0
  -> StopReason=NO_ELIGIBLE_WORK, nil error
  -> provider refresh count = 3 (<= selected)
  -> every Work VERIFIED
```

Serial processing is structural (one synchronous `ExecuteOne` at a time) and is
additionally asserted by `TestP5CycleSerialOnly` (max concurrent `ExecuteOne` ==
1).

## 7. Item-failure continuation evidence

`TestP5CycleRealPGItemLocalFailureContinues` — root A returns AList 403
(`AUTH_OR_PERMISSION` → `BLOCKED`), root B succeeds:

```text
Invocations=3, SelectedItems=2, Succeeded=1, Failed=1
StopReason=NO_ELIGIBLE_WORK, nil error
A: BLOCKED, B: VERIFIED, A never retried inline
```

`TestP5CycleInternalStopsImmediately` and
`TestP5CycleMissingFailureClassFailsClosed` cover the INTERNAL stop and the
classless fail-closed stop.

## 8. Stale / systemic stop evidence

`TestP5CycleStaleSelectionStops` — one invocation, `STALE_SELECTION`, non-nil
error, no same-cycle reselect.
`TestP5CycleCompletionFailureStops` — one invocation, `SYSTEMIC_ERROR`, non-nil
error.
`TestP5CycleUnexpectedErrorStops` — unexpected error → `SYSTEMIC_ERROR`, no
second item.

## 9. Wall-time interruption + recovery evidence

`TestP5CycleRealPGWallTimeInterruptsClaimedItem` — blocking scanner, real PG,
`MaxWallTime=50ms`:

```text
cycle deadline cancels the claimed item
  -> StopReason=MAX_WALL_TIME, nil cycle error, Invocations=1
  -> InterruptedInFlight=true (ClaimedSignalSeq > 0)
  -> Work remains IN_FLIGHT
  -> external Store.RecoverStaleInflight requeues it to PENDING
```

`TestP5CycleWallBudgetBeforeNextItem` proves the expired cycle context is checked
before the next `ExecuteOne` and reported as `MAX_WALL_TIME`, not as a systemic
Store/selector error. `TestP5CycleSelectedButUnclaimedNotInterrupted` proves
`InterruptedInFlight=false` when the claim never committed.
`TestP5CycleParentCancellation` / `TestP5CycleParentCancelledBeforeFirstItem`
prove parent cancellation is distinct and returns the parent context error.

`TestP5CycleRealPGOverdueRetryWaitNotPromoted` proves an overdue `RETRY_WAIT` row
is never auto-promoted: P5 executes only the eligible PENDING item and never
calls `RetryReady`.

`TestP5CycleRealPGNewSignalBoundedByMaxItems` proves a signal arriving during a
scan is executed as **new work** in a later iteration (`MaxItems=2` → exactly 2
invocations, final Work VERIFIED), not as an inline retry.

## 10. Changed production files

```text
internal/runtime/incrementalexec/cycle.go   (new)
```

New tests:

```text
internal/runtime/incrementalexec/cycle_test.go              (16)
internal/runtime/incrementalexec/cycle_integration_test.go  (5)
```

No `internal/store/postgres/**`, no `internal/runtime/scan/**`, no
`internal/collector/**`, no `internal/runtime/app/**`, no
`internal/runtime/worker/**`, no `cmd/**`, no `internal/transport/**`, no
`internal/query/**`, no `internal/kernel/**`, no `internal/domain/**`, no
migration, and no `go.mod`/`go.sum` change. P4 `executor.go` is unchanged.

## 11. Regression results

```text
gofmt -l <changed files>        clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

Existing P3/P4 tests remain green.

## 12. Boundary statement

P5 is an in-process finite draining primitive, not a scheduler. It adds no
polling cadence, ticker, timer-as-cadence, sleep/wait-until-due, continuous or
daemonized executor, background goroutine worker pool, unbounded loop, automatic
`RetryReady`/`ResumeSuspended`/`RepairBlocked`/`RecoverStaleInflight`,
`EmitDuePoll`/`MergeSignal` orchestration, Mutation Hint API, production sync CLI,
new migration, persistent cycle/lease table, HTTP API change, native
delta/provider cursor, direct 115 client, destructive removal, or Gate 5. P5 is
not wired into `worker`, `app`, any command, or any HTTP handler.

`FROZEN_CONTRACT_CHANGES: NONE`