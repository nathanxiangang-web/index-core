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
`WorkStore` and `*scan.Service` satisfies `ScopedScanner`. The row committed by
`CompleteFailure` is used directly for `FinalWorkState`; the executor never
re-reads mutable Work, so a concurrent later transition cannot rewrite this
invocation's result.

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
  -> ctx.Err() != nil     -> return ctx error (NO completion; Work stays IN_FLIGHT)
  -> scan error           -> typed class -> CompleteFailure(claimed_signal_seq, retry?)
  -> APPLIED / NOOP       -> CompleteSuccess(claimed_signal_seq)
  -> other/zero outcome   -> INTERNAL -> CompleteFailure(claimed_signal_seq, retry)
```

The outer context is checked immediately after the single `ScanScope` call,
**before either success or failure completion**: a cancellation that becomes
visible after the provider/Kernel work succeeded still leaves the Work
`IN_FLIGHT` for explicit P3 recovery instead of committing a result.

A nil `ScanScope` error is not by itself proof of verification: the executor
accepts only the applying terminal outcomes `APPLIED` / `NOOP`. `STALE_INPUT`,
`REJECTED`, the zero value and any unknown outcome fail closed and are completed
through the P3 failure path as `INTERNAL`, so an unapplied/stale observation can
never clear the claimed dirty work.

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

Provider classification covers **both** the scoped list call and the
username/password login call (the scoped path uses a typed `loginScoped`, never
the untyped generic `Adapter.login`): 401/403 → AUTH_OR_PERMISSION; 429 →
THROTTLED; 5xx / transport / timeout / malformed body / `total != len(content)`
on a short page / successful login with no token → TRANSIENT_PROVIDER;
`total > max_entries` → SCOPE_TOO_LARGE.

**Persisted base URL validation (before any provider I/O):** the scoped adapter
requires an absolute `http`/`https` URL with a non-empty host. Malformed syntax
(`://bad`), non-HTTP(S) schemes (`ftp://…`), and relative-only values
(`/relative-only`) are `CONFIG_INVALID` — a permanent adapter configuration
defect, never a retryable provider failure. Legitimate path-prefixed
deployments remain valid.

**Scoped response structure (fail closed):** the provider must explicitly
return both `content` (present, non-null; an empty array is a legal empty
directory) and `total` (present, `>= 0`). `data=null`, `data={}`, a missing
`total`, a missing/null `content`, or a negative `total` are
`TRANSIENT_PROVIDER`; Go zero-value field completion must never fabricate a
valid empty directory. On a non-overflowing page, `total == len(content)` is
then enforced, while a legitimate `total > per_page` truncation remains an
overflow.

Scan-layer classification: invalid/non-directory/ambiguous canonical parent →
INVALID_SCOPE; missing adapter binding, unsupported collector kind, invalid
`max_entries`, invalid configured provider root path (`..` component), or
missing/invalid reconcile policy → CONFIG_INVALID; **any non-ACTIVE root
lifecycle (NEW / DEPRECATED / DELETED) → ROOT_INACTIVE** — the scoped actuator
re-reads the root and fails closed, closing the `ACTIVE at claim → demoted
before ScanScope` race; PostgreSQL/query/storage failures and
persistence/admission/reconcile failures → INTERNAL. `GetAdapterConfig` /
`GetRootPolicy` distinguish `ErrNotFound` (permanent config defect) from any
other error (transient storage failure), so a transient DB failure can never
permanently BLOCK an item.

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

`TestP4ExecuteOnePostScanCancellation` proves the exact post-scan window: the
scanner returns a successful, applying result, cancellation becomes observable
before completion, `ExecuteOne` returns `context.Canceled`, neither
`CompleteSuccess` nor `CompleteFailure` is written, the Work stays `IN_FLIGHT`,
and explicit `RecoverStaleInflight` requeues it.

`TestP4ExecuteOneNonVerifyingOutcome` proves a nil scanner error with a
`STALE_INPUT` outcome never succeeds: it is completed as `INTERNAL` →
`RETRY_WAIT` with a future eligibility, and the claimed dirty epoch is not
cleared as verified.

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
internal/runtime/incrementalexec/executor_test.go      (13)
internal/runtime/incrementalexec/integration_test.go   (12)
internal/runtime/scan/scoped_errors_test.go            (12)
internal/collector/alist/scoped_errors_test.go         (12)
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

`internal/runtime/incrementalexec` = 25 tests (unit 13 + integration 12);
`internal/store/postgres` selector = 8; typed classification = 24 (scan 12 +
alist 12). Total new P4 tests = **57**. Existing P3 tests remain green.

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
## 13. Round 1 rework (Issue #75 review)

Four correctness blockers + one closeout from the P4 Round 1 review were fixed,
strictly inside the already authorized executor / scoped-error / tests /
result-doc surface:

1. **Typed classification completeness.**
   - the scoped username/password login uses a typed `loginScoped` instead of the
     untyped generic `Adapter.login`: login 401/403 → AUTH_OR_PERMISSION, 429 →
     THROTTLED, transport / 5xx / malformed / no token → TRANSIENT_PROVIDER.
     Proved end-to-end through `scan.Service` and the executor: login 403 →
     `AUTH_OR_PERMISSION` → `BLOCKED` with zero list calls.
   - an empty configured `base_url` → CONFIG_INVALID (validated before provider
     I/O), never TRANSIENT_PROVIDER.
   - a bad configured provider root path (e.g. a `..` component) → CONFIG_INVALID,
     not INVALID_SCOPE.
   - `GetAdapterConfig` / `GetRootPolicy` now distinguish `ErrNotFound`
     (CONFIG_INVALID) from any other error (INTERNAL), and
     `requirePresentDirectory` returns INTERNAL for storage failures and
     INVALID_SCOPE only for scope semantics.
2. **Malformed provider payload.** `code=200,data=null` unmarshals into a nil
   `*listData` and is rejected as TRANSIENT_PROVIDER instead of being accepted as
   a successful empty directory.
3. **Outcome verification.** `ExecuteOne` accepts only the applying outcomes
   `APPLIED` / `NOOP`. `STALE_INPUT`, `REJECTED`, the zero value and unknown
   outcomes are completed as `INTERNAL` → `RETRY_WAIT` through P3, so a
   nil-error non-success can never clear the claimed dirty work as verified.
4. **Post-scan cancellation window.** the outer context is checked immediately
   after the single `ScanScope` call, before either completion; cancellation
   leaves recoverable `IN_FLIGHT` even when the scan itself succeeded.
5. **Deterministic failure result.** `FinalWorkState` is taken from the row
   committed by `CompleteFailure`; `GetWork` was removed from the executor's
   `WorkStore` interface.

New tests added in this rework: `TestP4ExecuteOneNonVerifyingOutcome`,
`TestP4ExecuteOnePostScanCancellation`, `TestP4ExecuteOneLoginPermissionIsAuth`,
`TestP4NonVerifyingOutcomeDoesNotSucceed`, `TestP4AppliedAndNoopAreAccepted`,
`TestP4PostScanCancellationLeavesInflight`,
`TestP4FailureFinalStateFromCompletionRow`,
`TestScopedError{EmptyBaseURLIsConfigInvalid,NullListPayloadIsTransient,LoginClassification,LoginTransportIsTransient,LoginNoTokenIsTransient}`,
and
`TestScanScopeTyped{RootPathIsConfigInvalid,NullPayloadIsTransient,DBFailureIsInternal,LoginPermissionIsAuth}`.

`FROZEN_CONTRACT_CHANGES: NONE`
## 14. Round 2 rework (Issue #75 review)

Three final blockers + one closeout from the P4 Round 2 review were fixed,
strictly inside the authorized P4 scoped adapter / scan / executor tests /
result-doc surface:

1. **Persisted base URL validation.** `validateScopedBaseURL` now requires an
   absolute `http`/`https` URL with a non-empty host and rejects syntactically
   invalid values **before any provider I/O**; `://bad`, `ftp://example.com`,
   `/relative-only` and `http://` are `CONFIG_INVALID` (never
   `TRANSIENT_PROVIDER`), with zero provider requests. Path-prefixed deployments
   (`http://host/alist`) stay valid. Proved by
   `TestScopedErrorInvalidBaseURLIsConfigInvalid` and
   `TestScanScopeTypedInvalidBaseURLIsConfigInvalid`.
2. **Scoped response structure fail-closed.** The scoped decoder now unmarshals
   into `*scopedListData{Content *[]item; Total *int}`: `data` must be non-null,
   `content` must be present and non-null, and `total` must be present and
   `>= 0`; a missing/null field is `TRANSIENT_PROVIDER` instead of a fabricated
   empty directory. `total == len(content)` is enforced only on a
   non-overflowing page, so a legitimate `total > per_page` truncation remains
   `SCOPE_TOO_LARGE`. Proved by
   `TestScopedErrorIncompleteListPayloadIsTransient`,
   `TestScopedErrorExplicitEmptyDirectoryIsValid` and
   `TestScanScopeTypedIncompletePayloadIsTransient`.
3. **Non-ACTIVE scoped lifecycle gate.** `ScanScope` now fails closed with
   `ROOT_INACTIVE` for **any** non-ACTIVE root lifecycle (NEW / DEPRECATED /
   DELETED), closing the `ACTIVE at claim → demoted before ScanScope` race that
   selector/ClaimWork alone cannot cover. Generic `Service.Scan()` semantics and
   the absence of a long-held lifecycle lock are unchanged. Proved by
   `TestScanScopeTypedNonActiveLifecycleIsRootInactive` (NEW + DEPRECATED, zero
   provider requests) and the real-PostgreSQL executor race test
   `TestP4ExecuteOneLifecycleDemotedAfterClaim` (root demoted right after claim
   → `ROOT_INACTIVE` → `SUSPENDED`, zero provider requests).
4. **Evidence closeout.** The PR body and this report both state the current P4
   test count (57).

`FROZEN_CONTRACT_CHANGES: NONE`