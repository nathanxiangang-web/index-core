# Incremental P4 — One-shot Dirty Executor Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P3 State Persistence Prototype — Issue #72 / PR #73 — **ARCHITECT_ACCEPTED**
>
> P3 merge: `c7c7091412a92c66e35d4d70f65a326ddc82a132`
>
> P3 exit decision: **AUTHORIZE_ONE_SHOT_DIRTY_EXECUTOR_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P0 proved a safe targeted scoped refresh path.

P2 froze durable watch/work semantics.

P3 persisted those semantics in PostgreSQL.

P4 answers exactly one remaining integration question:

> Can one eligible persisted `DirtyScopeWork` be selected, version-claimed, executed through the existing P0 `scan.Service.ScanScope` path, and completed back into the P3 state machine without adding a scheduler, a second Canonical writer, or provider-specific orchestration?

The desired one-shot path is:

```text
eligible PENDING DirtyScopeWork
        ↓
deterministic read-only selection
        ↓
ClaimWork(expected version)
        ↓
exactly one existing ScanScope call
        ↓
existing P0 draft/admission/Kernel/reconcile path
        ↓
CompleteSuccess OR CompleteFailure
```

P4 is an **execution-path prototype**, not production scheduling.

## 2. P4 authority boundary

### Authorized

- one read-only deterministic Store selector for one eligible `PENDING` work item;
- a new one-shot runtime executor package;
- exactly one persisted Work claim per successful invocation;
- exactly one call into the accepted `scan.Service.ScanScope` path;
- success/failure completion through existing P3 Store primitives;
- deterministic retry-eligibility calculation for retryable failure classes;
- a narrow typed error contract for P0 scoped refresh so executor classification never parses strings;
- real PostgreSQL integration tests;
- `httptest` AList/OpenList protocol fixtures;
- result/evidence documentation.

### Not authorized

- production polling scheduler;
- ticker/timer loop;
- daemonized continuous dirty executor;
- goroutine worker pool for DirtyScopeWork;
- automatic repeated selection after one item completes;
- automatic provider retry in one invocation;
- automatic `RetryReady`, `ResumeSuspended`, or `RepairBlocked`;
- automatic startup `RecoverStaleInflight` wiring;
- Mutation Hint HTTP/API ingress;
- production sync CLI;
- changes to public HTTP `/v1`;
- provider-native delta/cursor;
- direct 115 client;
- destructive delta/removal;
- new Canonical write lane;
- Gate 5.

One call to the P4 executor may process **zero or one** work item. Never more.

## 3. No new persistence schema

P4 adds **no migration**.

P4 must use the accepted P3 tables and Store state machine exactly as merged:

```text
index_scope_watch_state
index_dirty_scope_work
```

No `0006` migration is authorized.

If implementation believes a schema change is required, stop and return to Architect review.

## 4. Runtime package ownership

Preferred new package:

```text
internal/runtime/incrementalexec/
    executor.go
    config.go
    errors.go
    executor_test.go
    integration_test.go
```

This package is the orchestration boundary.

It may depend on:

- `internal/incremental/state`;
- `internal/store/postgres`;
- `internal/runtime/scan`.

It must **not** be added to:

- `internal/runtime/worker.Worker.Run`;
- `internal/runtime/app` serve startup;
- any command;
- any HTTP handler.

P4 therefore produces executable library code with no production loop/entrypoint.

## 5. Deterministic eligible-work selection

P3 deliberately did not implement a scheduler. P4 may add one read-only Store primitive:

```text
NextEligiblePendingWork(ctx, now) (work, found, error)
```

Equivalent naming is acceptable.

It selects **PENDING only**.

Eligibility:

```text
root.lifecycle_state = ACTIVE
work_state = PENDING
pending_not_before IS NULL OR pending_not_before <= now
pending bucket is valid by P3 constraints
```

It must not auto-transition:

- RETRY_WAIT;
- BLOCKED;
- SUSPENDED;
- VERIFIED.

Those states remain governed by the explicit P3 primitives.

### 5.1 Deterministic ordering

For multiple eligible PENDING rows, select exactly one using:

```text
priority:
  URGENT
  HIGH
  NORMAL
  LOW

then:
  pending_first_seen_at ASC
  root_id ASC
  scope_key ASC
```

The SQL must use an explicit priority rank. Text lexical order is not authoritative.

This ordering is **operational selection only**. It creates no Canonical cross-root ordering.

### 5.2 Selection is intentionally read-only

The selection query does not claim or lock the item for execution.

It returns the P3 `version`.

The following claim must call:

```text
ClaimWork(root_id, scope_key, expectedWorkVersion, now)
```

If another mutation wins between selection and claim:

```text
ErrStateCASConflict
```

is returned and P4 stops.

The same invocation does **not** silently reselect another item.

This proves the exact future scheduler boundary:

```text
read candidate -> versioned claim
```

without implementing a scheduler loop.

## 6. One-shot executor API

Preferred conceptual API:

```go
type Executor struct {
    store      *postgres.Store
    scanner    ScopedScanner
    maxEntries int
    retry      RetryPolicy
    now        func() time.Time
}

func (e *Executor) ExecuteOne(ctx context.Context) (Result, error)
```

Exact names may vary; semantics may not.

### 6.1 ScopedScanner seam

The executor must call the existing P0 path, but tests need deterministic control.

Use a small interface implemented by `*scan.Service`, equivalent to:

```go
type ScopedScanner interface {
    ScanScope(
        context.Context,
        string, // root_id
        string, // scope_key
        int,    // max entries
    ) (scan.Result, error)
}
```

Do not copy/reimplement P0 reconcile logic inside the executor.

### 6.2 Explicit configuration

P4 executor configuration contains:

- `max_entries_per_scope`;
- retry delay for `TRANSIENT_PROVIDER`;
- retry delay for `THROTTLED`;
- retry delay for `INTERNAL`;
- injectable clock / `now func` for deterministic tests.

Rules:

- no hidden scheduler cadence;
- no sleep;
- all retry delays must be strictly positive;
- `max_entries_per_scope` must be within the accepted P0 hard cap;
- P4 recommended fixture value remains 1000, matching P1 feasibility defaults, but this is not a new frozen provider SLA.

Invalid executor configuration fails before selection/claim.

## 7. Exact ExecuteOne state flow

### 7.1 No eligible item

If no PENDING eligible row exists:

- no state mutation;
- no provider call;
- return a stable `ErrNoEligibleWork` or equivalent explicit result.

This is not an operational failure.

### 7.2 Selection stale before claim

If selection returns version `v`, but `ClaimWork(..., v, now)` returns `ErrStateCASConflict`:

- no scan;
- no second selection;
- return a stable stale-selection error or the Store CAS error.

### 7.3 Successful claim

After ClaimWork succeeds:

- retain `claimed_signal_seq` from the claimed row;
- pass **exactly the claimed row's `scope_key`** to `ScanScope`;
- call `ScanScope` at most once.

No provider retry occurs inside `ExecuteOne`.

### 7.4 Scan success

A nil `ScanScope` error means the existing P0 path completed fresh scoped coverage plus admission/application.

Then:

```text
CompleteSuccess(
    root_id,
    scope_key,
    claimed_signal_seq,
    completion_now,
)
```

The accepted P3 behavior decides:

- no post-claim signal -> VERIFIED;
- newer post-claim signal -> PENDING with only newer pending provenance.

Executor must not override this result.

### 7.5 Scan failure

A scan failure is classified into exactly one P3 `state.ErrorClass`.

Then:

```text
CompleteFailure(...)
```

is called once.

For:

```text
TRANSIENT_PROVIDER
THROTTLED
INTERNAL
```

the executor supplies:

```text
retry_not_before = completion_now + configured delay
```

For:

```text
AUTH_OR_PERMISSION
SCOPE_TOO_LARGE
INVALID_SCOPE
CONFIG_INVALID
ROOT_INACTIVE
```

the executor supplies no retry timestamp.

P3 continues to own the state mapping:

```text
TRANSIENT_PROVIDER / THROTTLED / INTERNAL -> RETRY_WAIT
AUTH / TOO_LARGE / INVALID_SCOPE / CONFIG_INVALID -> BLOCKED
ROOT_INACTIVE -> SUSPENDED
```

The executor does not reproduce the P3 state machine.

## 8. Interruption / crash semantics

The claim commits **before** provider work starts.

This means P4 is intentionally **at-least-once**, not exactly-once.

Crash windows:

```text
claim committed
   ↓
process exits before ScanScope
   -> IN_FLIGHT remains

ScanScope/Kernel succeeds
   ↓
process exits before CompleteSuccess
   -> IN_FLIGHT remains
```

The accepted recovery is P3 `RecoverStaleInflight`.

A later recovery may requeue and re-execute the scope.

This is safe only because execution reuses:

- P0 additive-safe PARTIAL semantics;
- existing admission/FIFO/Kernel rules;
- no destructive absence authority.

P4 must not invent a distributed transaction spanning provider I/O + Canonical reconcile + operational Work completion.

### 8.1 Context cancellation

If the caller's P4 execution context is cancelled or its deadline expires **after claim**:

- do not invent a provider failure class;
- do not call `CompleteFailure` merely because the caller is shutting down;
- return the context error;
- leave the item `IN_FLIGHT`;
- P3 recovery owns the next step.

If the lower scoped provider operation times out while the outer executor context remains valid, it is a provider failure and may classify as `TRANSIENT_PROVIDER`.

## 9. Typed scoped-refresh failure contract

P4 must **not** use:

- substring matching;
- message parsing;
- HTTP-code extraction from formatted error strings.

A narrow typed contract is authorized for P0 `ScanScope`.

Preferred provider-neutral shape in `internal/runtime/scan`:

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

type ScopeError struct {
    Kind ScopeFailureKind
    Err  error
}
```

Exact naming may vary.

The executor performs an exhaustive typed mapping:

```text
scan TRANSIENT_PROVIDER -> state.TRANSIENT_PROVIDER
scan THROTTLED          -> state.THROTTLED
scan AUTH/PERMISSION    -> state.AUTH_OR_PERMISSION
scan TOO_LARGE          -> state.SCOPE_TOO_LARGE
scan INVALID_SCOPE      -> state.INVALID_SCOPE
scan ROOT_INACTIVE      -> state.ROOT_INACTIVE
scan CONFIG_INVALID     -> state.CONFIG_INVALID
scan INTERNAL           -> state.INTERNAL
```

Unknown typed kinds fail closed as `INTERNAL`.

### 9.1 AList/OpenList scoped classification

A minimal typed error surface inside the AList scoped-refresh path is authorized if necessary.

Required classifications:

```text
API 401 / 403
    -> AUTH_OR_PERMISSION

API 429
    -> THROTTLED

API 5xx
transport failure
provider-side timeout
malformed/truncated provider response
total/count mismatch
    -> TRANSIENT_PROVIDER

total > max_entries
    -> SCOPE_TOO_LARGE
```

P0's one-request/direct-children/PARTIAL semantics do not change.

### 9.2 Scan-layer classifications

The runtime scoped service must classify its own failures without provider strings.

At minimum:

```text
invalid root-relative scope
nonexistent/ambiguous/non-directory scoped parent
    -> INVALID_SCOPE

unsupported collector kind
malformed adapter config
missing reconcile config
invalid P4 max_entries config
    -> CONFIG_INVALID

root rejected because lifecycle does not allow execution
    -> ROOT_INACTIVE

persistence/admission/reconcile failure not attributable above
    -> INTERNAL
```

If `ScanScope` returns an error after the root became non-runnable and the failure can be positively attributed to that lifecycle condition, classify `ROOT_INACTIVE`.

Do not globally change the generic full-scan `Service.Scan()` behavior.

## 10. P0 / Canonical boundary

P4 does not ingest provider data itself.

It calls:

```text
scan.Service.ScanScope
```

which continues to own:

```text
AList/OpenList refresh=true
    ↓
RawScan PARTIAL
    ↓
DRAFT Snapshot
    ↓
SubmitAndAdmitSnapshot
    ↓
Coordinator / Kernel
    ↓
Canonical Inventory + Journal
```

Therefore P4 must never directly write:

- Snapshot;
- Snapshot entries;
- CanonicalResource;
- generation;
- Journal;
- removal evidence;
- admission rows.

No second Canonical write path is permitted.

## 11. Scope semantics

P4 passes the persisted P3 `scope_key` directly to `ScanScope`.

P3 already guarantees normalized root-relative scope keys:

```text
/
 /a
 /a/b
```

P4 does not widen scope.

Coverage remains:

```text
EXACT_DIRECT_CHILDREN
recursive = false
```

A dirty `/a` does not imply `/a/b`.

## 12. One-shot selection is not a scheduler

The following are explicitly prohibited inside `ExecuteOne`:

```text
for { ... }
ticker
time.Sleep
recursive ExecuteOne
retry same provider call
select another Work after CAS conflict
select another Work after completion
automatic RetryReady
automatic ResumeSuspended
automatic RepairBlocked
automatic EmitDuePoll
```

The function returns after zero or one selected item.

A future scheduler may call this primitive repeatedly, but P4 does not authorize that caller.

## 13. Result and error contract

P4 result should make durable state transitions observable for tests/operators without exposing a new public API.

Preferred fields:

```text
selected
root_id
scope_key
selected_version
claimed_signal_seq
snapshot_id        # if ScanScope created one
final_work_state   # if completion committed
failure_class      # if scan failed and failure completion committed
```

Stable executor-level errors should distinguish:

- no eligible work;
- stale selection / claim CAS conflict;
- scanner failure whose Work failure transition committed;
- completion persistence failure;
- caller context cancellation.

A scan failure whose `CompleteFailure` succeeds should still return a non-nil execution error containing the typed class and original cause.

## 14. Completion failure semantics

If `ScanScope` returns success but `CompleteSuccess` fails:

- do not call `ScanScope` again;
- do not blindly call `CompleteSuccess` in a tight loop;
- return the completion error;
- the Work may remain IN_FLIGHT or commit outcome may be uncertain;
- later P3 recovery/reconciliation owns repair.

Likewise, if scan failure occurs but `CompleteFailure` cannot commit:

- do not retry provider I/O;
- return a completion/persistence error;
- leave repair to durable recovery.

This keeps the cross-system contract at-least-once and avoids pretending PostgreSQL + provider I/O form one transaction.

## 15. Retry policy boundary

P4 only computes a future timestamp.

It does **not** sleep until that time and does not make RETRY_WAIT runnable.

Example configuration:

```text
transient_provider_delay > 0
throttled_delay          > 0
internal_delay           > 0
```

Tests use small deterministic durations.

Production cadence/backoff tuning remains a later scheduler/policy decision.

## 16. Required test matrix

### 16.1 Store selection — real PostgreSQL

Prove:

- no eligible PENDING -> found=false;
- future `pending_not_before` excluded;
- inactive-root PENDING excluded;
- RETRY_WAIT/BLOCKED/SUSPENDED/VERIFIED excluded;
- priority order URGENT > HIGH > NORMAL > LOW;
- equal priority uses oldest `pending_first_seen_at`;
- final tie uses root_id / scope_key deterministically;
- selection is read-only;
- returned version can be passed to `ClaimWork`.

### 16.2 One-shot cardinality

With two eligible items:

- one `ExecuteOne` invocation claims/scans/completes at most one;
- the second remains untouched;
- no hidden loop;
- no provider retry.

### 16.3 Actual P0 success integration

Use:

- real PostgreSQL;
- `httptest` AList/OpenList endpoint;
- real `scan.Service.ScanScope`.

Prove:

```text
DirtyScopeWork PENDING
    ↓
ExecuteOne
    ↓
IN_FLIGHT
    ↓
one refresh=true request
    ↓
PARTIAL Snapshot admitted/applied
    ↓
Canonical resource visible
    ↓
DirtyScopeWork VERIFIED
```

Also verify:

- no removal evidence is created from missing children;
- one invocation produces exactly one scoped refresh request.

No live 115 account is required.

### 16.4 Signal during execution

Use a blocking/fake `ScopedScanner`:

1. executor claims signal 7;
2. scanner blocks;
3. merge signal 8;
4. scanner succeeds;
5. executor calls CompleteSuccess(7).

Assert:

- Work returns PENDING;
- signal 8 remains pending;
- claimed signal 7 watermark is recorded;
- signal 8 provenance is not cleared.

### 16.5 Typed failure mapping

At minimum prove:

- AList 403 -> AUTH_OR_PERMISSION -> BLOCKED;
- AList 429 -> THROTTLED -> RETRY_WAIT with configured future eligibility;
- directory overflow -> SCOPE_TOO_LARGE -> BLOCKED;
- provider 5xx/transport timeout -> TRANSIENT_PROVIDER -> RETRY_WAIT;
- invalid/non-directory scope -> INVALID_SCOPE -> BLOCKED;
- invalid adapter configuration -> CONFIG_INVALID -> BLOCKED;
- positively identified inactive-root failure -> ROOT_INACTIVE -> SUSPENDED;
- unknown internal scan failure -> INTERNAL -> RETRY_WAIT.

No error-string matching is accepted.

### 16.6 Post-claim barrier preservation

Through the executor path, prove a post-claim signal with future `not_before` still survives:

- retryable failure;
- BLOCKED failure;
- SUSPENDED failure.

This is a regression over P3 Round 2/Round 4 fixes.

### 16.7 Stale selection

Prove:

1. selector returns work version `v`;
2. another signal or defer changes it to `v+1`;
3. claim with `v` fails;
4. scanner call count remains zero;
5. executor does not reselect another item.

A narrow test hook between select and claim is allowed only if necessary and must remain unexported/test-only; prefer testing the Store selector + ClaimWork boundary directly when sufficient.

### 16.8 Cancellation / crash behavior

With a blocking scanner:

- claim commits;
- caller context cancels;
- executor returns context error;
- no CompleteFailure is written merely for shutdown;
- Work remains IN_FLIGHT;
- explicit P3 `RecoverStaleInflight` requeues it.

### 16.9 Completion failure

Inject/fake a completion Store failure after a successful scanner result.

Prove:

- scanner is not called twice;
- no hidden retry loop;
- executor returns the persistence/completion error.

Do not add production failpoints unless unavoidable; prefer a narrow Store interface only if it does not duplicate P3 semantics.

### 16.10 Regression

Required:

```text
gofmt
go vet ./...
go test -p1 ./...
```

Real PostgreSQL is mandatory where Store/transaction behavior matters.

## 17. Implementation surface

Expected production changes:

```text
internal/runtime/incrementalexec/**
internal/store/postgres/incremental_*.go        # selector only / necessary P3 API use
internal/runtime/scan/scoped*.go                # typed scoped error wrapping only
internal/collector/alist/scoped*.go             # typed scoped provider errors only if needed
```

Expected tests/docs:

```text
internal/runtime/incrementalexec/*_test.go
internal/store/postgres/incremental_*_test.go
internal/runtime/scan/scoped*_test.go
internal/collector/alist/scoped*_test.go
docs/incremental/P4-ONE-SHOT-EXECUTOR-RESULT.md
```

Not authorized production changes:

```text
cmd/**
internal/runtime/app/**
internal/runtime/worker/**
internal/transport/**
internal/query/**
internal/kernel/**
internal/domain/**
internal/store/postgres/migrations/**
```

Existing `internal/runtime/scan/scoped.go` may change only to introduce/propagate typed failure classification; P0 collection/reconcile semantics must remain identical.

If a required production change falls outside this surface, stop for Architect review.

## 18. Required result report

Worker must create:

```text
docs/incremental/P4-ONE-SHOT-EXECUTOR-RESULT.md
```

Report:

- exact executor API;
- exact selection query/order;
- typed error-class mapping;
- retry-delay configuration used in tests;
- actual P0 integration evidence;
- signal-7/signal-8 integration evidence;
- cancellation/recovery evidence;
- changed production files;
- provider request count for P0 integration;
- test commands/results;
- boundary statement;
- `FROZEN_CONTRACT_CHANGES: NONE`.

## 19. Acceptance criteria

P4 passes only if:

1. one invocation processes zero or one item;
2. selection is deterministic and read-only;
3. stale selection is rejected by P3 ClaimWork CAS;
4. executor calls existing ScanScope, never reimplements Canonical ingestion;
5. one invocation performs at most one scoped provider refresh;
6. success completes through P3 CompleteSuccess;
7. failure completes through P3 CompleteFailure;
8. failure classification is typed — no string parsing;
9. retry classes write future retry eligibility but never retry inline;
10. context cancellation after claim leaves recoverable IN_FLIGHT;
11. signal arriving during ScanScope is preserved after earlier success/failure;
12. no removal authority is introduced;
13. real-PG and actual P0 integration tests pass;
14. P3 Store tests remain green;
15. Q1–Q9, Kernel, Journal, Canonical semantics remain unchanged;
16. no scheduler/ticker/daemon/API/CLI is introduced.

## 20. P4 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_BOUNDED_EXECUTOR_LOOP_PROTOTYPE
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 21. Current authorization

```text
P0 scoped refresh                     ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility      ARCHITECT_ACCEPTED
P2 durable scope-state design         ARCHITECT_ACCEPTED
P3 state persistence prototype        ARCHITECT_ACCEPTED
P4 one-shot dirty executor            AUTHORIZED AFTER THIS PLAN MERGES

new migration                         NOT AUTHORIZED
production polling scheduler          NOT AUTHORIZED
continuous executor daemon            NOT AUTHORIZED
Mutation Hint API                     NOT AUTHORIZED
production sync CLI                   NOT AUTHORIZED
native delta/provider cursor          NOT AUTHORIZED
direct 115 integration                NOT AUTHORIZED
destructive delta/removal             NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```
