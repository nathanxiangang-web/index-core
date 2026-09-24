# Incremental P4 — One-shot Dirty Executor Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #75 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P4-ONE-SHOT-DIRTY-EXECUTOR-PROTOTYPE.md`
>
> Predecessor: P3 state persistence — Issue #72 / PR #73 — ARCHITECT_ACCEPTED
>
> **ZERO OR ONE ITEM PER INVOCATION · NO PRODUCTION SCHEDULER · NO CONTINUOUS EXECUTOR · NO API/CLI · NO MIGRATION**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Executor API

Package `internal/runtime/incrementalexec`:

```go
type WorkStore interface {
    NextEligiblePendingWork(ctx, now) (state.DirtyScopeWork, bool, error)
    ClaimWork(ctx, rootID, scopeKey string, expectedWorkVersion int64, now) (state.DirtyScopeWork, error)
    CompleteSuccess(ctx, rootID, scopeKey string, claimedSignalSeq int64, now) (state.DirtyScopeWork, error)
    CompleteFailure(ctx, rootID, scopeKey string, claimedSignalSeq int64, class state.ErrorClass, retryNotBefore *time.Time, now) (state.DirtyScopeWork, error)
    GetWork(ctx, rootID, scopeKey string) (state.DirtyScopeWork, error)
}

type ScopedScanner interface {
    ScanScope(ctx, rootID, scope string, maxEntries int) (scan.Result, error)
}

type Config struct {
    MaxEntriesPerScope int
    Retry              RetryPolicy
}

type RetryPolicy struct {
    TransientProvider time.Duration
    Throttled         time.Duration
    Internal          time.Duration
}

func New(store WorkStore, scanner ScopedScanner, cfg Config, now func() time.Time) (*Executor, error)
func (e *Executor) ExecuteOne(ctx context.Context) (Result, error)
```

`Result` exposes `Selected`, `RootID`, `ScopeKey`, `SelectedVersion`,
`ClaimedSignalSeq`, `SnapshotID`, `FinalWorkState`, `FailureClass`.

`New` fails closed when the store/scanner is missing or the config is invalid;
`ExecuteOne` re-validates before selection. `*postgres.Store` satisfies
`WorkStore` and `*scan.Service` satisfies `ScopedScanner`.

Stable executor errors: `ErrNoEligibleWork`, `ErrStaleSelection`,
`ErrScannerFailed`, `ErrCompletionFailed`; caller cancellation returns
`context.Canceled` / `context.DeadlineExceeded` unchanged.

## 2. Selection query and order

`Store.NextEligiblePendingWork(ctx, now) (state.DirtyScopeWork, bool, error)`
(`internal/store/postgres/incremental_selector.go`) is read-only: it neither
claims nor locks, and it returns the persisted `version`.

```sql
SELECT <workColumns>
  FROM index_dirty_scope_work
 WHERE (root_id, scope_key) = (
       SELECT w.root_id, w.scope_key
         FROM index_dirty_scope_work w
         JOIN index_root r ON r.root_id = w.root_id
        WHERE r.lifecycle_state = 'ACTIVE'
          AND w.work_state = 'PENDING'
          AND (w.pending_not_before IS NULL OR w.pending_not_before <= $1)
        ORDER BY
          CASE w.pending_priority
            WHEN 'URGENT' THEN 4 WHEN 'HIGH' THEN 3
            WHEN 'NORMAL' THEN 2 WHEN 'LOW'  THEN 1 ELSE 0
          END DESC,
          w.pending_first_seen_at ASC,
          w.root_id ASC,
          w.scope_key ASC
        LIMIT 1)
```

Eligibility is exactly `ACTIVE root + PENDING + (not_before NULL or <= now)`.
RETRY_WAIT / BLOCKED / SUSPENDED / VERIFIED are never selected or
auto-transitioned. The priority rank is explicit (text lexical order is not
authoritative).

## 3. ExecuteOne flow

```text
NextEligiblePendingWork(now)
  -> not found            -> ErrNoEligibleWork (no mutation, no provider call)
  -> ClaimWork(selected.version, now)
       -> ErrStateCASConflict -> ErrStaleSelection (no scan, no reselect)
  -> ScanScope(root, persisted scope_key, maxEntries)   EXACTLY ONCE
  -> success -> CompleteSuccess(claimed_signal_seq)
  -> failure -> typed class -> CompleteFailure(claimed_signal_seq, retry?)
```

The persisted normalized `scope_key` is passed unchanged; scope is never widened
or collapsed. There is no loop, sleep, second selection, or provider retry.

## 4. Typed failure contract (no string parsing)

`internal/runtime/scan/scoped_errors.go`:

```go
type ScopeFailureKind string
const (
    ScopeFailureTransientProvider
    ScopeFailureThrottled
    ScopeFailureAuthOrPermission
    ScopeFailureTooLarge
    ScopeFailureInvalidScope
    ScopeFailureRootInactive
    ScopeFailureConfigInvalid
    ScopeFailureInternal
)
type ScopeError struct { Kind ScopeFailureKind; Err error }
```

`internal/collector/alist/scoped_errors.go` provides the provider-level
`ScopedError`/`ScopedErrorKind` used by the AList scoped path.

Executor mapping (`incrementalexec.MapScopeFailure`, exhaustive; unknown kinds
fail closed as INTERNAL):

| scan kind | state.ErrorClass | P3 work state |
|---|---|---|
| TRANSIENT_PROVIDER | TRANSIENT_PROVIDER | RETRY_WAIT |
| THROTTLED | THROTTLED | RETRY_WAIT |
| AUTH_OR_PERMISSION | AUTH_OR_PERMISSION | BLOCKED |
| SCOPE_TOO_LARGE | SCOPE_TOO_LARGE | BLOCKED |
| INVALID_SCOPE | INVALID_SCOPE | BLOCKED |
| ROOT_INACTIVE | ROOT_INACTIVE | SUSPENDED |
| CONFIG_INVALID | CONFIG_INVALID | BLOCKED |
| INTERNAL | INTERNAL | RETRY_WAIT |

Provider classification (AList scoped refresh): 401/403 →
AUTH_OR_PERMISSION; 429 → THROTTLED; 5xx / transport / timeout /
malformed / total-count mismatch → TRANSIENT_PROVIDER; `total > max_entries` →
SCOPE_TOO_LARGE. Scan-layer classification: invalid/non-directory/ambiguous
scope → INVALID_SCOPE; missing adapter / unsupported collector kind / invalid
`max_entries` / missing reconcile policy → CONFIG_INVALID; DELETED root →
ROOT_INACTIVE; persistence/admission/reconcile → INTERNAL.

**Zero string matching / HTTP-code extraction from messages** in production code
and tests.

## 5. Retry policy

Retryable classes only (`TRANSIENT_PROVIDER`, `THROTTLED`, `INTERNAL`):

```text
retry_not_before = completion_now + configured_positive_delay
CompleteFailure once
```

Non-retryable classes pass `nil` retry eligibility. The executor never sleeps,
never calls `RetryReady`, and never invokes the provider again.

Test configuration:

```text
max_entries_per_scope = 1000
transient_provider_delay = 30s
throttled_delay          = 45s
internal_delay           = 60s
```

Config validation rejects `max_entries` outside `[0, 10000]` and any delay
`<= 0` before selection/claim.

## 6. Actual P0 integration evidence

`TestP4ExecuteOneActualP0Success` (real PostgreSQL + httptest AList + real
`scan.Service.ScanScope`):

```text
PENDING DirtyScopeWork
  -> ExecuteOne
  -> exactly one refresh=true /api/fs/list request
  -> PARTIAL Snapshot admitted/reconciled by the existing Kernel path
  -> Canonical /a.txt and /sub PRESENT
  -> DirtyScopeWork VERIFIED (claimed_signal_seq = 1)
```

A second scoped observation omitting the known child `/sub` produces **no
removal evidence** and does not remove `/sub` (`removal_evidence_state = NONE`,
`missing_since = NULL`, `consecutive_complete_missing = 0`).

**Provider request count: exactly 1 per invocation; 2 invocations produced
exactly 2 refresh requests.**

## 7. Signal-7 / signal-8 evidence

`TestP4ExecuteOneSignalDuringExecution` (real P3 persistence, blocking real
scanner):

```text
claim signal 7 -> block scanner -> merge signal 8 -> scanner succeeds
-> CompleteSuccess(7)
```

Asserted afterwards: Work is `PENDING`; `last_verified_signal_seq = 1`;
`signal_seq = 2`; pending provenance contains only the newer signal (8) and the
claimed signal provenance is not re-added.

`TestP4ExecuteOnePostClaimBarrierSurvives` proves a post-claim signal with a
future `not_before` survives a retryable failure, a `BLOCKED` failure, and a
`SUSPENDED` failure (the future barrier is preserved exactly).

## 8. Cancellation / crash recovery evidence

`TestP4ExecuteOneCancellationLeavesInflight`:

```text
claim commits -> caller context cancels -> ExecuteOne returns context.Canceled
-> no CompleteFailure is written
-> Work remains IN_FLIGHT with claimed_signal_seq set and no error class
-> explicit Store.RecoverStaleInflight requeues exactly 1 item to PENDING
```

`TestP4ExecuteOneCompletionFailure` injects a test-only completion failure after
a successful real scoped refresh: the provider refresh count stays 1, there is
no hidden retry, `ExecuteOne` returns `ErrCompletionFailed`, and the Work remains
`IN_FLIGHT`.

## 9. Stale selection

`TestP4ExecuteOneStaleSelectionNoScan` mutates the Work between selection and
claim (test-only `WorkStore` wrapper): `ClaimWork` returns
`ErrStateCASConflict`, the executor returns `ErrStaleSelection`, the scanner call
count is zero, and no other item is selected.

## 10. Changed production files

```text
internal/runtime/incrementalexec/config.go        (new)
internal/runtime/incrementalexec/errors.go        (new)
internal/runtime/incrementalexec/executor.go      (new)
internal/store/postgres/incremental_selector.go   (new)
internal/runtime/scan/scoped_errors.go            (new)
internal/runtime/scan/scoped.go                   (typed error wrapping only)
internal/collector/alist/scoped_errors.go         (new)
internal/collector/alist/scoped.go                (typed provider errors only)
```

New tests:

```text
internal/store/postgres/incremental_selector_test.go   (8)
internal/runtime/incrementalexec/executor_test.go      (9)
internal/runtime/incrementalexec/integration_test.go   (8)
internal/runtime/scan/scoped_errors_test.go            (5)
internal/collector/alist/scoped_errors_test.go         (5)
```

No migration, no `cmd/**`, no `internal/runtime/app/**`, no
`internal/runtime/worker/**`, no `internal/transport/**`, no `internal/query/**`,
no `internal/kernel/**`, no `internal/domain/**`, and no `go.mod`/`go.sum`
change. `internal/runtime/scan/scoped.go` only gained typed error
classification/propagation; the P0 collection/reconcile semantics of
`Service.ScanScope` and `Service.Scan` are unchanged.

## 11. Regression results

```text
gofmt -l <changed files>        clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

`internal/runtime/incrementalexec` = 17 tests; `internal/store/postgres`
selector = 8; typed classification = 10. Existing P3 tests remain green.

## 12. Boundary statement

P4 is a bounded one-shot library prototype. One `ExecuteOne` processes zero or
one item and calls the existing `scan.Service.ScanScope` at most once. It adds
no production polling scheduler, ticker/timer loop, continuous/daemonized
executor, automatic repeated selection, inline provider retry, automatic
`RetryReady`/`ResumeSuspended`/`RepairBlocked`, startup `RecoverStaleInflight`
wiring, Mutation Hint API, production sync CLI, native delta/provider cursor,
direct 115 integration, destructive removal, or new migration. The executor
package is not wired into `worker`, `app`, any command, or any HTTP handler.
P4 is intentionally at-least-once: the claim commits before provider I/O and
durable P3 recovery owns crash repair.

`FROZEN_CONTRACT_CHANGES: NONE`