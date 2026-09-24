# Incremental P6 — Scheduler Orchestration Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P5 Bounded Executor Loop Prototype — Issue #79 / PR #80 — **ARCHITECT_ACCEPTED**
>
> P5 merge: `88e3e8ffa9c791f0ae6b04529cf82a65a6431d1e`
>
> P5 exit decision: **AUTHORIZE_SCHEDULER_ORCHESTRATION_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P1 proved bounded due-scope polling feasibility.

P3 persisted due watch state and the atomic:

```text
due ScopeWatchState
  -> EmitDuePoll
  -> DirtyScopeWork
  -> watch schedule advance
```

transaction.

P4/P5 proved bounded durable execution:

```text
eligible DirtyScopeWork
  -> ExecuteOne
  -> bounded multi-item CycleRunner
```

P6 composes those accepted pieces for the first time.

Question:

> Can one manually-invoked finite orchestration cycle materialize a bounded snapshot of persisted due watches and then run one accepted bounded executor cycle, while remaining finite, restart-safe, and free of ticker/daemon behavior?

Desired shape:

```text
manual RunCycle
    ↓
capture observed_at once
    ↓
ListDueWatches(observed_at, bounded limit)
    ↓
EmitDuePoll once per selected watch
    ↓
P5 CycleRunner.Run
    ↓
return
```

P6 is **not** a production scheduler.

## 2. Authority boundary

### Authorized

- one manually-invoked orchestration primitive;
- one fixed due-watch snapshot time per cycle;
- bounded due-watch listing/materialization;
- existing `ListDueWatches`;
- existing atomic `EmitDuePoll`;
- existing P5 `CycleRunner`;
- one overall cycle wall-time budget;
- deterministic result/stop model;
- real PostgreSQL watch -> work -> execution integration tests;
- httptest AList/OpenList proof.

### Not authorized

- ticker;
- cron/cadence loop;
- sleep/wait-until-next-due;
- background goroutine scheduler;
- continuous daemon/service;
- concurrent orchestration cycles;
- worker pool / parallel watch emission;
- automatic `RetryReady`;
- automatic `ResumeSuspended`;
- automatic `RepairBlocked`;
- automatic `RecoverStaleInflight`;
- Mutation Hint API;
- production sync CLI;
- public HTTP scheduling API;
- native delta/provider cursor;
- direct 115 client;
- destructive delta/removal;
- migration;
- Query / Kernel / Journal / Canonical changes;
- Gate 5.

## 3. Hard prototype bounds

P6 must remain structurally finite.

```text
1 <= MaxDueWatchAttempts <= 5
1 <= MaxExecuteItems     <= 5
0 <  MaxWallTime         <= 60s
```

These are prototype safety caps, not production SLA or polling cadence.

Invalid configuration fails before:

- Store due-watch query;
- `EmitDuePoll`;
- P5 execution.

No zero/unbounded sentinel is allowed.

## 4. Preferred package/API

Preferred package:

```text
internal/runtime/incrementalorch/
```

Preferred conceptual API:

```go
type DueWatchStore interface {
    ListDueWatches(context.Context, time.Time, int) ([]state.ScopeWatchState, error)
    EmitDuePoll(context.Context, string, string, int64, time.Time) (state.DirtyScopeWork, error)
}

type ExecutorCycle interface {
    Run(context.Context, incrementalexec.CycleConfig) (incrementalexec.CycleResult, error)
}

type Config struct {
    MaxDueWatchAttempts int
    MaxExecuteItems     int
    MaxWallTime         time.Duration
}

type Runner struct {
    store DueWatchStore
    exec  ExecutorCycle
    now   func() time.Time
}

func (r *Runner) RunCycle(ctx context.Context, cfg Config) (Result, error)
```

Exact names may vary. Semantics may not.

P6 must not copy:

- watch due SQL;
- `EmitDuePoll` transaction logic;
- DirtyScopeWork merge semantics;
- P4 selector/claim/scan/completion;
- P5 item stop matrix.

## 5. Fixed observed_at snapshot

At cycle start, after config validation:

```text
observed_at = now()
```

That exact instant is used for:

- due-watch selection;
- every `EmitDuePoll(..., observed_at)` attempt in this cycle.

Do not refresh `now` per watch.

Reason:

- one cycle observes one deterministic due set;
- watches becoming due after `observed_at` wait until a later manual cycle;
- `next_due_at` advances consistently from the same scheduling observation;
- the due set cannot grow while the cycle is already draining it.

P5 execution may use its own accepted clock semantics.

## 6. Due-watch snapshot and backlog signal

P6 calls:

```text
ListDueWatches(observed_at, MaxDueWatchAttempts + 1)
```

The extra row is observation only.

Rules:

- attempt materialization for at most the first `MaxDueWatchAttempts` rows;
- `MoreDueWatches = true` iff an extra row was returned;
- do not materialize the extra row;
- do not page/requery;
- do not fill a stale/failed slot with another row beyond the first bounded snapshot.

Thus the hard bound is **materialization attempts**, not successful emissions.

Ordering remains Store-owned and frozen as:

```text
next_due_at ASC
root_id ASC
scope_key ASC
```

## 7. Materialization phase

For each selected due watch, serially call exactly once:

```text
EmitDuePoll(
  root_id,
  scope_key,
  expected_watch_version,
  observed_at,
)
```

### 7.1 Success

Count:

```text
DueAttempted++
DueEmitted++
```

The existing P3 transaction remains authoritative:

```text
Root -> Watch -> Work
merge POLL_SCHEDULE signal
advance watch schedule
commit atomically
```

P6 does not modify the returned Work.

### 7.2 Watch CAS conflict

If:

```text
errors.Is(err, postgres.ErrStateCASConflict)
```

then:

- count `DueAttempted++`;
- count `DueStale++`;
- emit no retry;
- do not re-read/re-list the watch;
- continue to the next watch already in the bounded snapshot.

The watch is reconsidered only by a future manual cycle.

This prevents a tight CAS loop.

### 7.3 Other materialization error

Any non-CAS `EmitDuePoll` error is fail-closed for this prototype.

Action:

- stop further watch materialization;
- do not start P5 execution;
- preserve counts for already committed emissions;
- return `MATERIALIZATION_ERROR` with non-nil error.

Do not parse error strings.

Examples include:

- Store/query failure;
- lifecycle race not represented as CAS;
- invalid persisted operational state;
- transaction/commit failure.

P6 does not attempt compensation/rollback across previously committed watches.

### 7.4 Partial materialization is durable

P6 does **not** create an outer transaction across multiple watches.

If:

```text
watch A committed
watch B committed
watch C fails
```

then A/B stay committed.

Result reports exactly what committed before the failure.

A later manual cycle re-evaluates persisted state.

## 8. Overall wall-time budget

P6 creates one derived orchestration context:

```text
orch_ctx = context.WithTimeout(parent, MaxWallTime)
```

This is a finite safety deadline, not scheduling cadence.

Boundary precedence before any new phase/operation:

```text
1. parent context cancelled/deadline
2. P6 wall deadline
3. operation
```

Parent cancellation wins when both are observable.

### 8.1 Deadline before/after materialization operation

If the P6 deadline is already expired before the next Store operation:

- stop `MAX_WALL_TIME`;
- nil orchestration error;
- do not start another Store call;
- do not start P5.

### 8.2 Deadline interrupts ListDueWatches

Because the query is read-only:

- if parent context is healthy and the operation returns a context cancellation caused by `orch_ctx`, stop `MAX_WALL_TIME`;
- nil orchestration error.

### 8.3 Deadline interrupts EmitDuePoll

`EmitDuePoll` is atomic per watch, but a caller may not always know whether a commit became visible when cancellation races commit completion.

Therefore if an `EmitDuePoll` returns a context cancellation attributable to the P6-owned deadline:

- stop `MAX_WALL_TIME`;
- nil orchestration error;
- set `MaterializationInterrupted = true`;
- do not retry the watch;
- do not start P5.

The next manual cycle must re-read persisted state.

This flag means the caller should not infer whether that final watch attempt committed solely from in-memory counts.

No automatic repair/recovery is allowed.

## 9. Execution phase

After the bounded materialization phase completes without a fatal error, P6 invokes P5 **exactly once**, even when:

```text
DueEmitted == 0
```

Reason:

eligible DirtyScopeWork may already exist from:

- operator/manual signals;
- prior cycle emissions;
- recovery performed externally;
- future hint producers.

P6 must not make execution depend on "new due signals were emitted this cycle".

Call:

```text
P5.Run(
  orch_ctx,
  CycleConfig{
    MaxItems:    MaxExecuteItems,
    MaxWallTime: MaxWallTime,
  },
)
```

The parent `orch_ctx` ensures the overall P6 deadline dominates any later P5 child deadline.

P6 does not copy or reinterpret P5 item-level continuation policy.

## 10. Translating P5 result to P6 result

P6 retains the complete nested P5 `CycleResult`.

### 10.1 P5 returns nil error

P6 returns:

```text
COMPLETED
nil orchestration error
```

regardless of whether the nested P5 stop was:

- NO_ELIGIBLE_WORK;
- MAX_ITEMS;
- MAX_WALL_TIME.

The nested P5 result remains authoritative for executor-side stopping.

If P6's own `orch_ctx` is already expired when P5 returns, P6 reports `MAX_WALL_TIME` instead of `COMPLETED`.

### 10.2 Parent cancellation during P5

If original parent `ctx.Err() != nil`:

- P6 stop = `CONTEXT_CANCELLED`;
- return the parent context error;
- retain P5 result;
- do not auto-recover.

### 10.3 P6-owned deadline during P5

If:

- parent context is still healthy;
- P5 error is a context cancellation/deadline error;
- `orch_ctx.Err() != nil`;

then:

- P6 stop = `MAX_WALL_TIME`;
- nil orchestration error;
- retain P5 result;
- expose nested `InterruptedInFlight`.

Do **not** convert an unrelated P5 systemic error merely because wall time also expired.

The actual returned error must carry a context cause.

### 10.4 Other P5 error

Return:

```text
EXECUTOR_ERROR
non-nil error
```

Retain the nested P5 result.

P6 does not retry P5 in the same orchestration cycle.

## 11. Result contract

Preferred:

```go
type StopReason string

const (
    StopCompleted
    StopMaxWallTime
    StopContextCancelled
    StopMaterializationError
    StopExecutorError
)

type Result struct {
    StartedAt  time.Time
    FinishedAt time.Time
    ObservedAt time.Time

    StopReason StopReason

    DueCandidates int
    DueAttempted  int
    DueEmitted    int
    DueStale      int
    MoreDueWatches bool

    MaterializationInterrupted bool

    ExecutorRan bool
    Executor    incrementalexec.CycleResult
}
```

Semantics:

- `DueCandidates` is the number returned by the bounded `limit+1` query, not total global backlog;
- `MoreDueWatches` means "at least one due row existed beyond this cycle's attempt budget";
- `DueAttempted <= MaxDueWatchAttempts <= 5`;
- `DueEmitted <= DueAttempted`;
- `DueStale <= DueAttempted`;
- `ExecutorRan` is true only if P5 was invoked.

No persistent P6 run-history table is authorized.

## 12. Existing Work state interactions

`EmitDuePoll` keeps the accepted P2/P3 merge behavior.

If the watch's Work is:

- `VERIFIED` -> opens a new PENDING epoch;
- `PENDING` -> coalesces POLL_SCHEDULE provenance;
- `IN_FLIGHT` -> puts the new signal in post-claim pending;
- `RETRY_WAIT` -> signal merges but state/backoff remains;
- `BLOCKED` -> signal merges but remains BLOCKED;
- `SUSPENDED` -> signal merges but remains SUSPENDED.

P6 does not override those rules.

A due watch therefore does **not** guarantee one immediately executable item.

## 13. Watch attribution

P4/P3 already update watch health only from the attempt's `claimed_source_set`.

P6 does not update watch success/failure counters directly.

Accepted path:

```text
EmitDuePoll
  -> pending source includes POLL_SCHEDULE
  -> P4 ClaimWork snapshots provenance
  -> P4 CompleteSuccess/Failure
  -> P3 watch health update
```

This preserves claim-scoped attribution.

## 14. No automatic recovery/repair

P6 does not call:

```text
RecoverStaleInflight
RetryReady
ResumeSuspended
RepairBlocked
MergeSignal
```

except `EmitDuePoll` internally performs its accepted signal merge.

P6 also does not inspect or mutate:

- RETRY_WAIT eligibility;
- BLOCKED repair policy;
- SUSPENDED lifecycle resumption.

Those remain external.

## 15. No concurrency

One `RunCycle` is strictly serial:

- one due query;
- one `EmitDuePoll` active at a time;
- then one P5 cycle.

P6 does not authorize concurrent invocation of the same Runner.

No mutex/lease is introduced in the prototype; caller/orchestrator must serialize calls.

Multi-process/HA scheduling remains deferred.

## 16. Expected implementation surface

Preferred production files:

```text
internal/runtime/incrementalorch/orchestrator.go
internal/runtime/incrementalorch/config.go
```

Tests:

```text
internal/runtime/incrementalorch/orchestrator_test.go
internal/runtime/incrementalorch/integration_test.go
```

Result report:

```text
docs/incremental/P6-SCHEDULER-ORCHESTRATION-RESULT.md
```

No production changes are expected in:

```text
internal/store/postgres/**
internal/runtime/incrementalexec/**
internal/runtime/scan/**
internal/collector/**
internal/runtime/app/**
internal/runtime/worker/**
cmd/**
internal/transport/**
internal/query/**
internal/kernel/**
internal/domain/**
internal/store/postgres/migrations/**
```

If P6 appears to require those changes, stop for Architect review.

## 17. Required tests

### 17.1 Configuration

Prove:

- due attempts 0 / 6 rejected;
- execute items 0 / 6 rejected;
- wall time <=0 / >60s rejected;
- boundaries 1/5/60s accepted;
- invalid config performs zero Store/P5 calls.

### 17.2 No due watches still drains existing work

Fake Store returns no due watches.

Fake/real P5 has existing eligible work.

Assert:

- P5 invoked exactly once;
- orchestration completes;
- execution result retained.

### 17.3 Due snapshot hard bound/backlog

Provide >5 due watches.

With `MaxDueWatchAttempts=5`:

- query uses limit 6;
- exactly first 5 are attempted;
- sixth is never emitted;
- `MoreDueWatches=true`;
- stale attempts still consume the five-attempt budget;
- no requery/paging.

### 17.4 Fixed observed_at

Use fake clock/Store.

Assert:

- ListDueWatches receives one captured time;
- every EmitDuePoll receives exactly that same time;
- `now()` is not called once per watch.

### 17.5 CAS stale skip

First selected watch -> `ErrStateCASConflict`.

Second -> success.

Assert:

- first attempted once only;
- DueStale=1;
- second still attempted;
- no retry/re-read/re-list.

### 17.6 Fatal materialization error

Watch A commits.

Watch B returns non-CAS error.

Assert:

- A remains durable;
- B stops cycle;
- P5 not invoked;
- `MATERIALIZATION_ERROR`;
- counts reflect A + attempted B;
- no compensation.

### 17.7 Overall wall budget before next operation

Use fake Store that consumes the deadline after one successful operation.

Assert:

- no next Store/P5 operation begins;
- `MAX_WALL_TIME`;
- nil orchestration error.

### 17.8 Wall budget interrupts materialization

Fake `EmitDuePoll` blocks until `orch_ctx` expires.

Assert:

- `MAX_WALL_TIME`;
- `MaterializationInterrupted=true`;
- P5 not invoked;
- no retry.

### 17.9 Parent cancellation

Cancel parent during due query/materialization and during P5.

Assert:

- `CONTEXT_CANCELLED`;
- parent context error returned;
- no later phase begins.

### 17.10 Real PG watch -> work -> P5 -> canonical proof

Use real PostgreSQL + real:

- `ListDueWatches`;
- `EmitDuePoll`;
- P5 CycleRunner;
- P4 Executor;
- `scan.Service.ScanScope`;
- httptest AList/OpenList.

Prepare a HOT due watch on an ACTIVE root.

Prove in one P6 RunCycle:

```text
watch due
  -> one POLL_SCHEDULE signal
  -> watch last_due_at / next_due_at advance
  -> Work claimed
  -> exactly one refresh=true request
  -> PARTIAL Snapshot admitted/applied
  -> Canonical resource visible
  -> Work VERIFIED
  -> watch success bookkeeping updated
```

Also prove:

- no removal evidence from scoped absence;
- no duplicate poll emission;
- provider request count <= selected P5 items.

### 17.11 Existing PENDING + due coalescing

Seed an existing PENDING manual/operator signal for the same scope plus due watch.

After materialization/execution, prove:

- signal_seq incremented once for poll;
- claimed provenance includes both outstanding sources as appropriate;
- exactly one execution can satisfy the coalesced epoch;
- no lost wakeup.

### 17.12 Due poll into RETRY_WAIT/BLOCKED

Seed due watches whose Work is already RETRY_WAIT/BLOCKED.

Assert:

- EmitDuePoll merges signal;
- state does not auto-promote;
- P5 does not execute those rows merely because a poll was emitted;
- independent eligible work can still execute.

### 17.13 P6 wall deadline during P5

Use real P5/P4 with a blocking scanner.

Assert:

- P6-owned deadline cancels nested P5;
- P6 reports `MAX_WALL_TIME` with nil orchestration error;
- nested P5 result is retained;
- nested `InterruptedInFlight=true` for proven claim;
- Work remains IN_FLIGHT;
- P6 does not recover it.

### 17.14 P5 systemic error propagation

Fake P5 returns non-context systemic error.

Assert:

- P6 `EXECUTOR_ERROR`;
- non-nil original/wrapped error;
- no second P5 run;
- no extra due materialization.

### 17.15 Regression

Required:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
```

P3/P4/P5 tests stay green.

## 18. Acceptance criteria

P6 passes only if:

1. one manual call is finite;
2. due-watch attempts are hard-capped at 5;
3. execution items remain hard-capped at 5;
4. total P6 wall budget is hard-capped at 60s;
5. due set uses one fixed `observed_at`;
6. no requery/paging/tight CAS retry occurs;
7. only existing `EmitDuePoll` materializes poll signals;
8. CAS-stale watch skips without retry;
9. non-CAS materialization failure stops before execution;
10. already-committed emissions are never compensated/rolled back by P6;
11. P5 runs once even when this cycle emitted zero new polls;
12. P5 item policy remains authoritative and unmodified;
13. P6 distinguishes parent cancellation from its own wall deadline;
14. deadline interruption does not trigger automatic recovery;
15. due poll into RETRY_WAIT/BLOCKED/SUSPENDED does not auto-promote;
16. real PG watch -> work -> P5 -> Canonical proof passes;
17. execution/materialization is serial;
18. no ticker/cadence/sleep/daemon/API/CLI/migration is added;
19. frozen Canonical/Query/Kernel contracts remain unchanged.

## 19. P6 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_MANUAL_INCREMENTAL_COMMAND_PROTOTYPE
AUTHORIZE_PRODUCTION_SCHEDULER_DESIGN
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 20. Current authorization

```text
P0 scoped refresh                     ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility      ARCHITECT_ACCEPTED
P2 durable scope-state design         ARCHITECT_ACCEPTED
P3 state persistence prototype        ARCHITECT_ACCEPTED
P4 one-shot dirty executor            ARCHITECT_ACCEPTED
P5 bounded executor loop              ARCHITECT_ACCEPTED
P6 scheduler orchestration            AUTHORIZED AFTER THIS PLAN MERGES

MaxDueWatchAttempts                   <= 5
MaxExecuteItems                       <= 5
MaxWallTime                           <= 60s

production polling scheduler          NOT AUTHORIZED
ticker/cadence daemon                 NOT AUTHORIZED
continuous executor service           NOT AUTHORIZED
background worker pool                NOT AUTHORIZED
Mutation Hint API                     NOT AUTHORIZED
production sync CLI                   NOT AUTHORIZED
native delta/provider cursor          NOT AUTHORIZED
direct 115 integration                NOT AUTHORIZED
destructive delta/removal             NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```
