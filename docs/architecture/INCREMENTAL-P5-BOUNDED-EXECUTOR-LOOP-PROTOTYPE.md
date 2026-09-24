# Incremental P5 — Bounded Executor Loop Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P4 One-shot Dirty Executor Prototype — Issue #75 / PR #76 — **ARCHITECT_ACCEPTED**
>
> P4 merge: `10668d6203ad86cad3eee074df19a1ee62dea7f7`
>
> P4 exit decision: **AUTHORIZE_BOUNDED_EXECUTOR_LOOP_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P4 proved one persisted dirty-work item can safely travel through:

```text
select
  -> ClaimWork(expected version)
  -> exactly one ScanScope
  -> CompleteSuccess / CompleteFailure
  -> return
```

P5 answers the next orchestration question:

> Can IndexCore safely drain a small finite set of currently eligible DirtyScopeWork items by repeatedly invoking the already accepted P4 one-shot primitive, while remaining strictly bounded by item count and wall time and without becoming a scheduler/daemon?

P5 does **not** redesign `ExecuteOne`.

The desired shape is:

```text
cycle start
  ↓
ExecuteOne
  ↓
evaluate stop policy
  ↓
ExecuteOne
  ↓
evaluate stop policy
  ↓
...
  ↓
finite stop
  ↓
return CycleResult
```

## 2. P5 authority boundary

### Authorized

- one bounded cycle runner around the accepted P4 `ExecuteOne`;
- explicit `max_items` budget;
- explicit `max_wall_time` budget;
- finite stop-reason/result model;
- one derived cycle deadline/context;
- repeated P4 `ExecuteOne` calls only;
- real PostgreSQL multi-item tests;
- httptest AList/OpenList multi-item integration;
- result/evidence documentation.

### Not authorized

- polling cadence;
- ticker;
- sleep/wait-until-due;
- timer-driven recurring scheduling;
- daemonized/continuous execution;
- background goroutine worker pool;
- unbounded loop;
- automatic retry of one item;
- inline provider retry;
- automatic `RetryReady`;
- automatic `ResumeSuspended`;
- automatic `RepairBlocked`;
- automatic startup `RecoverStaleInflight`;
- `EmitDuePoll` orchestration;
- Mutation Hint API;
- production sync CLI;
- public HTTP API changes;
- native delta/provider cursor;
- direct 115 client;
- destructive delta/removal;
- new migration;
- Q1-Q9 / Kernel / Journal / Canonical changes;
- Gate 5.

P5 is an in-process **finite draining primitive**, not a scheduler.

## 3. Reuse P4 — no duplicated execution path

P5 must call the existing accepted one-shot API.

Preferred seam:

```go
type OneShotExecutor interface {
    ExecuteOne(context.Context) (incrementalexec.Result, error)
}
```

Preferred API:

```go
type CycleConfig struct {
    MaxItems    int
    MaxWallTime time.Duration
}

type CycleRunner struct {
    one OneShotExecutor
}

func (r *CycleRunner) Run(ctx context.Context, cfg CycleConfig) (CycleResult, error)
```

Equivalent naming is acceptable.

P5 must **not** copy:

- selector SQL;
- ClaimWork;
- typed scan classification;
- retry eligibility;
- ScanScope invocation;
- completion logic.

Those remain owned by P4/P3/P0.

## 4. Prototype hard safety caps

P5 must be structurally bounded even when the caller supplies a bad configuration.

Prototype hard caps:

```text
1 <= max_items <= 5
0 < max_wall_time <= 60s
```

Why these values:

- P1 already validated a small hot-scope set with `max_scopes_per_cycle = 5`;
- P1 already used a `max_cycle_wall_time = 60s` safety budget;
- P5 is proving bounded orchestration, not production throughput.

These are **prototype safety caps**, not production defaults, SLA, cadence, or provider limits.

Invalid config fails before the first `ExecuteOne`.

No zero/unbounded sentinel is allowed.

## 5. Cycle context / wall-time budget

P5 must enforce `MaxWallTime` across an in-flight item, not merely check elapsed time between items.

At cycle start:

```text
cycle_ctx = child context with deadline:
    min(parent deadline, cycle_start + MaxWallTime)
```

A standard `context.WithTimeout` / `WithDeadline` is allowed.

This deadline is a one-cycle safety budget. It is **not** a ticker/cadence/scheduler.

### 5.1 Budget expiry during an item

If the P5-owned wall-time deadline expires while P4 `ExecuteOne` is in progress:

- P4 receives context cancellation;
- P4 frozen semantics leave a claimed item `IN_FLIGHT` if cancellation occurs after claim;
- P5 returns stop reason `MAX_WALL_TIME`;
- P5 does **not** call `RecoverStaleInflight`;
- P5 does not start another item.

This preserves P4 at-least-once/crash semantics.

### 5.2 Parent cancellation

If the caller's parent context is cancelled/deadline-exceeded:

- stop immediately;
- return the parent context error;
- stop reason is `CONTEXT_CANCELLED`;
- no recovery is performed.

The result should distinguish parent cancellation from P5-owned wall-budget exhaustion.

## 6. Item budget

`MaxItems` counts selected item attempts, not provider HTTP requests.

An item counts toward the cycle budget when P4 returns `Result.Selected = true`.

This includes:

- success;
- classified scan failure;
- stale selection.

However stale selection stops the cycle immediately (§8), so it cannot create a tight contention loop.

Before each new `ExecuteOne` call, the cycle applies this deterministic boundary precedence:

```text
1. parent context cancelled/deadline -> CONTEXT_CANCELLED
2. selected_items >= MaxItems        -> MAX_ITEMS
3. cycle-owned deadline expired      -> MAX_WALL_TIME
4. otherwise call ExecuteOne
```

The cycle-owned deadline **must be checked before calling ExecuteOne**. Once the wall budget is already exhausted, P5 must not enter another selector/Store call and accidentally report the expired context as a systemic Store error.

Thus P5 never performs an extra selection after either finite budget is consumed.

Provider request count remains bounded by the accepted P4 invariant:

```text
provider scoped refreshes <= selected_items <= MaxItems <= 5
```

Some selected items may make zero provider requests (e.g. stale claim or root demotion).

## 7. Cycle result contract

Preferred result:

```go
type StopReason string

const (
    StopNoEligibleWork
    StopMaxItems
    StopMaxWallTime
    StopStaleSelection
    StopInternalItemFailure
    StopSystemicError
    StopContextCancelled
)

type CycleResult struct {
    StartedAt  time.Time
    FinishedAt time.Time

    StopReason StopReason

    Invocations   int
    SelectedItems int
    Succeeded     int
    Failed        int

    Last incrementalexec.Result

    InterruptedInFlight bool
}
```

Exact field names may differ.

Requirements:

- stable stop reason;
- counts must be deterministic;
- last one-shot result retained when available;
- wall/caller cancellation after a selected claim should be observable as potentially interrupted `IN_FLIGHT`;
- do not expose a new public HTTP contract.

P5 does not need persistent cycle history.

## 8. Stop / continue matrix

The cycle must use the existing P4 stable errors.

### 8.1 No eligible work

```text
errors.Is(err, ErrNoEligibleWork)
```

Action:

- stop immediately;
- `StopReason = NO_ELIGIBLE_WORK`;
- normal cycle completion;
- return nil cycle error;
- do not sleep for future `pending_not_before`.

This is the normal "drained for now" outcome.

### 8.2 Successful item

```text
err == nil
```

Action:

- increment selected/success counters;
- if MaxItems reached, stop;
- otherwise invoke P4 again.

If P4 success leaves the same scope `PENDING` because a newer signal arrived, it may be selected again by a later iteration under the normal selector ordering. That is a **new outstanding signal**, not an inline retry. The global MaxItems bound still applies.

### 8.3 Classified item-local scanner failure

```text
errors.Is(err, ErrScannerFailed)
```

The P4 failure transition has already committed.

Increment selected/failed counters.

Continue to another eligible item only when `Result.FailureClass` is present and is one of these durable classes:

```text
TRANSIENT_PROVIDER
THROTTLED
AUTH_OR_PERMISSION
SCOPE_TOO_LARGE
INVALID_SCOPE
CONFIG_INVALID
ROOT_INACTIVE
```

Any `ErrScannerFailed` with a missing/unknown `FailureClass` fails closed as `SYSTEMIC_ERROR` and stops the cycle.

Rationale:

- the failed item has moved to RETRY_WAIT / BLOCKED / SUSPENDED;
- it is no longer immediately eligible;
- P5 may safely drain independent ready work;
- bounded MaxItems prevents provider/error amplification.

### 8.4 INTERNAL item failure

If `ErrScannerFailed` has:

```text
FailureClass == INTERNAL
```

Action:

- count the failed item;
- stop immediately;
- `StopReason = INTERNAL_ITEM_FAILURE`;
- return a non-nil cycle error wrapping the P4 item error.

Rationale:

`INTERNAL` may represent storage/admission/reconcile/systemic failure. P5 must not hammer multiple scopes through a potentially broken subsystem.

No automatic `RetryReady`.

### 8.5 Stale selection

```text
errors.Is(err, ErrStaleSelection)
```

Action:

- if P4 result reports Selected, count it as selected;
- stop immediately;
- `StopReason = STALE_SELECTION`;
- return the stale-selection error;
- do not call `ExecuteOne` again in the same cycle.

This preserves the P4 rule:

> stale selection causes no reselect in the same execution attempt.

P5 must not turn repeated one-shot calls into a tight CAS retry loop.

### 8.6 Completion failure / storage/systemic error

For:

```text
ErrCompletionFailed
unexpected Store/claim/select error
config/runtime error
```

Action:

- stop immediately;
- `StopReason = SYSTEMIC_ERROR`;
- return non-nil error;
- no further item.

If P4 result reports a selected item, preserve that in CycleResult.

### 8.7 Context cancellation / cycle deadline

After `ExecuteOne` returns a context error:

1. if the **parent context** is cancelled/deadline-exceeded:
   - `CONTEXT_CANCELLED`;
   - return parent context error.

2. else if the **cycle-owned deadline** expired:
   - `MAX_WALL_TIME`;
   - normal bounded stop;
   - return nil cycle error;
   - if the last P4 result had `Selected=true`, set `InterruptedInFlight=true`.

No next iteration.

## 9. Retry/repair states remain external

P5 does **not** transition:

```text
RETRY_WAIT -> PENDING
SUSPENDED -> PENDING
BLOCKED -> PENDING
IN_FLIGHT crash recovery -> PENDING
```

Existing P3 methods remain explicit/manual in P5.

P5 also does not call:

```text
EmitDuePoll
MergeSignal
RecoverStaleInflight
RetryReady
ResumeSuspended
RepairBlocked
```

except tests may call those primitives externally to prepare/assert state.

This keeps P5 purely as a finite consumer of already-eligible PENDING work.

## 10. No waiting

P5 must not wait for work to become eligible.

Forbidden:

```text
time.Sleep
time.Ticker
time.Timer used as cadence/wait-for-next-work
poll-until-due
wait until pending_not_before
recursive Run
goroutine worker pool
```

The cycle deadline implementation may internally use the Go context timer mechanism; that is a safety deadline, not scheduling.

When `ErrNoEligibleWork` occurs, return immediately.

## 11. Single-threaded cycle

P5 cycle concurrency is exactly one active `ExecuteOne` at a time.

No parallel scope execution is authorized.

Reason:

- same-root ordering already exists in P0/Kernel;
- cross-root parallelism is a separate performance/safety decision;
- P5 is proving finite orchestration, not throughput.

A future phase may evaluate concurrency separately.

## 12. Interaction with Watch / polling

P5 does not consume `ScopeWatchState` directly.

The cycle does not:

- find due watches;
- call `EmitDuePoll`;
- advance watch schedules;
- create `POLL_SCHEDULE` signals.

If a P3/P1 component has already created eligible DirtyScopeWork, P5 may execute it like any other work.

This preserves separation:

```text
producer/scheduler side:
    Watch / Hint / Operator -> DirtyScopeWork

executor side:
    P5 bounded cycle -> P4 ExecuteOne -> P0 ScanScope
```

## 13. No new persistence schema

P5 adds no migration and no persistent cycle table.

Do not add:

- cycle leases;
- worker ownership rows;
- scheduler heartbeat;
- run history table;
- concurrency semaphore table.

If implementation believes persistent orchestration state is necessary, stop for Architect review.

## 14. Expected implementation surface

Preferred:

```text
internal/runtime/incrementalexec/
    cycle.go
    cycle_test.go
    cycle_integration_test.go
```

Small additions to existing P4 executor types are allowed only when necessary for stable error/result inspection.

No expected changes to PostgreSQL Store production code.

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
internal/collector/**
internal/runtime/scan/**
```

P5 should sit **above** the already accepted P4 one-shot primitive.

## 15. Required tests

### 15.1 Configuration bounds

Prove:

- MaxItems 0 rejected;
- MaxItems >5 rejected;
- MaxWallTime <=0 rejected;
- MaxWallTime >60s rejected;
- valid boundary values 1/5 and small/60s accepted;
- invalid config invokes no P4 item.

### 15.2 No work

Fake one-shot:

- first call -> ErrNoEligibleWork;
- exactly one invocation;
- stop NO_ELIGIBLE_WORK;
- selected=0;
- no sleep/retry;
- nil cycle error.

### 15.3 MaxItems hard stop

Fake one-shot always succeeds.

For MaxItems=N:

- exactly N ExecuteOne calls;
- selected=N;
- succeeded=N;
- stop MAX_ITEMS;
- no N+1 selector call.

Test N=1 and N=5.

### 15.4 Multi-item real PostgreSQL/P4 integration

Use real PostgreSQL plus the real accepted P4 executor.

Prepare at least three eligible PENDING items.

Prove:

- one Run cycle processes them serially;
- deterministic selector order remains P4/Store-owned;
- each selected item uses one P4 execution;
- completed Work rows have expected final states;
- provider scoped-refresh request count never exceeds selected items;
- no hidden parallelism.

At least one case should use real `scan.Service.ScanScope` + httptest AList/OpenList.

No live 115 account is required.

### 15.5 Item-local provider failure continues

Prepare:

- item A -> typed 403/AUTH or 429/THROTTLED failure;
- item B -> success.

Assert:

- A durably becomes BLOCKED or RETRY_WAIT;
- cycle continues;
- B executes;
- counts show one failed + one succeeded;
- no inline retry of A;
- normal stop is NO_ELIGIBLE_WORK or MAX_ITEMS depending config.

### 15.6 INTERNAL stops cycle

Fake or real classified INTERNAL result:

- first item durably completes INTERNAL -> RETRY_WAIT;
- cycle stops immediately;
- second eligible item remains untouched;
- StopReason INTERNAL_ITEM_FAILURE;
- non-nil cycle error.

### 15.7 Stale selection stops cycle

One-shot returns ErrStaleSelection on first selected candidate.

Assert:

- exactly one one-shot invocation;
- no second candidate;
- StopReason STALE_SELECTION;
- non-nil stale-selection error.

### 15.8 Completion/systemic failure stops cycle

Inject P4 ErrCompletionFailed.

Assert:

- stop immediately;
- no second item;
- StopReason SYSTEMIC_ERROR;
- non-nil error.

Also test an unexpected selector/claim/store error through a fake OneShot, and an `ErrScannerFailed` whose result has no failure class: both must fail closed as SYSTEMIC_ERROR.

### 15.9 Wall-time budget — before next item

Use a controlled fake one-shot whose first execution returns only after the cycle-owned deadline has expired (it may intentionally ignore the context for this boundary test).

Assert:

- the runner checks the expired cycle context before the next ExecuteOne call;
- no second item/selector invocation starts;
- stop MAX_WALL_TIME;
- the expired context is not misreported as a systemic Store/selector error.

Production still must enforce a real derived context deadline for in-flight work.

### 15.10 Wall-time budget — during an item

Use a blocking one-shot/P4 scanner.

Prove:

- cycle deadline cancels the active call;
- no next item starts;
- StopReason MAX_WALL_TIME;
- cycle returns as a normal bounded stop;
- `InterruptedInFlight=true` when the item had been selected/claimed;
- real-PG Work remains IN_FLIGHT;
- explicit external P3 `RecoverStaleInflight` requeues it.

Do not auto-recover in Run.

### 15.11 Parent cancellation

Parent context cancels while item runs.

Assert:

- StopReason CONTEXT_CANCELLED;
- returned error matches parent context;
- no next item;
- no automatic recovery.

### 15.12 RetryWait is not auto-promoted

Prepare:

- one RETRY_WAIT whose not_before is already in the past;
- one eligible PENDING item.

Run cycle.

Assert:

- PENDING item may execute;
- RETRY_WAIT remains RETRY_WAIT;
- P5 never calls `RetryReady`.

This is intentional for P5.

### 15.13 New signal after success

Optional but recommended regression:

- signal A is claimed;
- signal B arrives during ScanScope;
- P4 success leaves scope PENDING;
- if still highest eligible item, a later P5 iteration may execute the new epoch;
- total executions remain bounded by MaxItems.

This proves P5 repeats P4 for **new work**, not inline retry.

### 15.14 Regression

Required:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
```

Existing P3/P4 tests must remain green.

## 16. Result report

Worker must create:

```text
docs/incremental/P5-BOUNDED-EXECUTOR-LOOP-RESULT.md
```

Report:

- exact Run/Cycle API;
- config/hard caps;
- stop matrix;
- count semantics;
- wall-deadline semantics;
- real-PG multi-item evidence;
- provider request count;
- item-failure continuation evidence;
- stale/systemic stop evidence;
- wall-time interruption + recovery evidence;
- changed production files;
- test commands/results;
- explicit no-scheduler/no-daemon statement;
- `FROZEN_CONTRACT_CHANGES: NONE`.

## 17. Acceptance criteria

P5 passes only if:

1. cycle is finite by both item count and wall time;
2. MaxItems hard cap is 5;
3. MaxWallTime hard cap is 60s;
4. cycle reuses P4 ExecuteOne rather than reimplementing it;
5. no work returns immediately without wait;
6. stale selection never causes same-cycle reselect;
7. INTERNAL/systemic failures stop the cycle;
8. item-local durable provider/config/scope failures may continue to other ready work;
9. wall-time budget interrupts an in-flight item through context and leaves P4/P3 recovery semantics intact;
10. parent cancellation is distinguished from cycle-budget exhaustion;
11. RETRY_WAIT/BLOCKED/SUSPENDED are not auto-promoted;
12. no provider item is retried inline;
13. execution is serial, not parallel;
14. provider request count remains bounded by selected items <=5;
15. real PostgreSQL multi-item proof passes;
16. P0/P3/P4 contracts remain unchanged;
17. stop-reason precedence is deterministic: parent cancellation > max-items > cycle wall budget before each new item;
18. no scheduler/ticker/cadence/daemon/API/CLI/migration is introduced.

## 18. P5 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_SCHEDULER_ORCHESTRATION_PROTOTYPE
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 19. Current authorization

```text
P0 scoped refresh                     ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility      ARCHITECT_ACCEPTED
P2 durable scope-state design         ARCHITECT_ACCEPTED
P3 state persistence prototype        ARCHITECT_ACCEPTED
P4 one-shot dirty executor            ARCHITECT_ACCEPTED
P5 bounded executor loop              AUTHORIZED AFTER THIS PLAN MERGES

max items per cycle                   <= 5
max cycle wall time                   <= 60s

production polling scheduler          NOT AUTHORIZED
ticker/cadence                        NOT AUTHORIZED
continuous executor daemon            NOT AUTHORIZED
Mutation Hint API                     NOT AUTHORIZED
production sync CLI                   NOT AUTHORIZED
native delta/provider cursor          NOT AUTHORIZED
direct 115 integration                NOT AUTHORIZED
destructive delta/removal             NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```
