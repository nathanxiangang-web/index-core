# Incremental P7 — Manual Incremental Command Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P6 Scheduler Orchestration Prototype — Issue #82 / PR #83 — **ARCHITECT_ACCEPTED**
>
> P6 merge: `3aa02bb9f6cf9c6de52ac5fb88310d17c735af12`
>
> P6 exit decision: **AUTHORIZE_MANUAL_INCREMENTAL_COMMAND_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P0–P6 now prove the full bounded incremental path as library/runtime primitives.

P7 answers one narrow operational question:

> Can an operator invoke the already accepted P6 finite orchestration exactly once from the existing `indexcore` binary, obtain deterministic structured output, and exit safely without creating a second writer, daemon, scheduler, or alternate execution path?

Desired shape:

```text
indexcore incremental run [flags]
        ↓
load existing runtime config
        ↓
validate schema compatibility
        ↓
acquire existing DB writer advisory lock
        ↓
construct existing Scan -> P4 -> P5 -> P6 chain
        ↓
P6 RunCycle exactly once
        ↓
emit one structured JSON result
        ↓
release writer lock
        ↓
exit
```

P7 is **not** a production scheduler and is **not** a new runtime daemon.

## 2. Command shape

Add one top-level command family to the existing binary:

```text
indexcore incremental run [flags]
```

Reserved but not implemented:

```text
indexcore incremental watch
indexcore incremental daemon
indexcore incremental recover
indexcore incremental hint
```

Unknown/missing incremental subcommands fail non-zero.

Do not create a second binary.

## 3. Reuse existing runtime composition

P7 must compose the accepted implementations:

```text
postgres.Store
    ↓
scan.Service
    ↓
incrementalexec.Executor        (P4)
    ↓
incrementalexec.CycleRunner     (P5)
    ↓
incrementalorch.Runner          (P6)
```

P7 must not copy or reimplement:

- due-watch selection;
- `EmitDuePoll`;
- dirty-work selection;
- ClaimWork;
- ScanScope;
- retry classification;
- P5 stop matrix;
- P6 materialization/translation logic.

The app/CLI layer only parses, validates, constructs, invokes once, renders, and exits.

## 4. Writer ownership is mandatory

P6 does not authorize concurrent orchestration cycles.

The manual command therefore **must acquire the existing PostgreSQL writer advisory lock** before calling P6:

```go
lock, err := store.AcquireWriterLock(ctx)
```

Rules:

- use the same lock/key as `serve`;
- do not introduce a second lock namespace;
- if `postgres.ErrWriterLockHeld`, fail immediately;
- do not wait/spin/retry for the lock;
- do not invoke P6;
- do not call the provider;
- do not mutate operational state.

Consequence:

> P7 cannot run concurrently with an active `indexcore serve` writer on the same database.

This is intentional for the prototype.

On command exit, release the writer lock using a fresh bounded background context so cancellation of the command context cannot strand the dedicated advisory-lock connection.

Preferred release budget: existing `cfg.ShutdownTimeout`.

## 5. Database/schema preflight

Before acquiring writer ownership and before any incremental mutation:

1. open PostgreSQL using existing runtime config;
2. ping;
3. call existing `postgres.SchemaStatus`;
4. require `Compatible()`;
5. fail closed on missing/future/incompatible schema;
6. never auto-migrate.

No migration is authorized in P7.

## 6. Command-local incremental flags

P7 reuses existing runtime config flags/environment for:

- `--database-url`;
- `--rclone-path`;
- `--rclone-config`;
- `--scan-timeout`;
- `--shutdown-timeout`;
- logging configuration.

Incremental budgets remain **command-local flags** in P7; do not add new global environment variables or persistent config fields.

Required flags/defaults:

```text
--max-due-watch-attempts   5
--max-execute-items        5
--max-wall-time            60s

--max-entries-per-scope    1000

--retry-transient-provider 30s
--retry-throttled          45s
--retry-internal           60s
```

Validation is delegated to the already accepted P4/P5/P6 config validators where possible.

The command must still reject invalid values before writer acquisition/provider work.

Prototype bounds remain:

```text
1 <= max_due_watch_attempts <= 5
1 <= max_execute_items     <= 5
0 <  max_wall_time         <= 60s

0 <= max_entries_per_scope <= 10000
retry delays > 0
```

Defaults are validated prototype defaults, **not production SLA/cadence/provider limits**.

## 7. Runtime construction

Preferred app-layer sequence:

```text
load + parse flags
  ↓
validate base runtime config
  ↓
validate P4/P6 configs
  ↓
open Store / schema compatibility
  ↓
acquire writer lock
  ↓
scanner := scan.New(...)
  ↓
one := incrementalexec.New(store, scanner, P4 config, nil)
  ↓
cycle := incrementalexec.NewCycleRunner(one)
  ↓
orch := incrementalorch.NewRunner(store, cycle, nil)
  ↓
orch.RunCycle(ctx, P6 config) exactly once
```

No hidden goroutine/ticker/sleep is authorized.

## 8. Signal / cancellation behavior

The existing top-level command context already derives from SIGINT/SIGTERM.

P7 must pass that context unchanged into P6.

Frozen behavior remains:

- operator cancellation -> P6/P5 cancellation semantics;
- a claimed work item may remain IN_FLIGHT;
- P7 does **not** call `RecoverStaleInflight`;
- P7 does **not** call `RetryReady`;
- P7 does **not** call `ResumeSuspended`;
- P7 does **not** call `RepairBlocked`;
- command exits after the single P6 result/error.

No "cleanup" may silently change the accepted durable state machine.

## 9. Structured stdout contract

The command emits exactly one JSON object to stdout **after P6 returns**, including when P6 returns a result plus a non-nil runtime error.

Logs remain on stderr through the existing logger.

Do not print secrets, database DSN, AList credentials/tokens, adapter JSON, or provider auth material.

Preferred stable JSON shape:

```json
{
  "command": "incremental run",
  "stop_reason": "COMPLETED",
  "started_at": "2026-09-25T00:00:00Z",
  "finished_at": "2026-09-25T00:00:01Z",
  "observed_at": "2026-09-25T00:00:00Z",
  "due": {
    "candidates": 1,
    "attempted": 1,
    "emitted": 1,
    "stale": 0,
    "more_due_watches": false,
    "materialization_interrupted": false
  },
  "executor": {
    "ran": true,
    "stop_reason": "NO_ELIGIBLE_WORK",
    "invocations": 1,
    "selected_items": 1,
    "succeeded": 1,
    "failed": 0,
    "interrupted_in_flight": false,
    "last": {
      "selected": true,
      "root_id": "...",
      "scope_key": "/",
      "snapshot_id": "...",
      "final_work_state": "VERIFIED",
      "failure_class": null
    }
  }
}
```

Exact internal DTO names may differ.

Requirements:

- snake_case JSON;
- RFC3339/RFC3339Nano timestamps;
- nested P6/P5 semantics preserved;
- if executor did not run, `executor.ran=false`;
- if no item was selected, `last` may be null/omitted;
- error text itself stays on stderr and is not required in the stable JSON object;
- no raw Go struct field names as operator contract.

## 10. Output on errors

### 10.1 Preflight errors

For errors before P6 starts, such as:

- invalid flags/config;
- database open/ping failure;
- incompatible schema;
- writer lock held;
- construction failure;

no P6 result exists.

P7 may emit no JSON result; return the error and use the existing stderr/error path.

### 10.2 P6 returns Result + error

If P6 ran and returns a populated Result plus a non-nil error:

1. render the structured JSON result to stdout;
2. return the error to the top-level command;
3. top-level main prints the error to stderr;
4. exit non-zero.

This preserves the partial/durable operational evidence while still signaling command failure.

## 11. Exit-code contract

P7 does not introduce a new multi-code CLI framework.

Reuse the existing process contract:

```text
exit 0 -> command returned nil
exit 1 -> command returned non-nil error
```

Therefore:

- P6 `COMPLETED` -> 0;
- P6-owned normal bounded `MAX_WALL_TIME` -> 0 because P6 returns nil error;
- parent cancellation -> 1;
- materialization error -> 1;
- executor/systemic error -> 1;
- writer lock held -> 1;
- invalid config/schema -> 1.

Do not convert P6 stop reasons into a second independent exit-code policy.

## 12. No implicit recovery or retry promotion

P7 must not call:

```text
RecoverStaleInflight
RetryReady
ResumeSuspended
RepairBlocked
MergeSignal
EmitDuePoll directly
```

P7 only calls P6.

No `--recover`, `--retry-ready`, `--repair`, or equivalent hidden flags in this phase.

## 13. Command help/usage

Update top-level usage:

```text
incremental    run one bounded manual incremental orchestration cycle
```

Document:

```text
indexcore incremental run [flags]
```

The help/documentation must state:

- one-shot/manual only;
- active `serve` writer causes lock failure;
- hard bounds 5/5/60s;
- no scheduler/ticker/background mode;
- normal `MAX_WALL_TIME` can exit 0 with stop_reason `MAX_WALL_TIME`.

## 14. Production change surface

Expected production changes:

```text
cmd/indexcore/main.go
internal/runtime/app/incremental.go
```

Small shared app helpers are allowed only if they reduce duplication without changing existing command semantics.

Documentation:

```text
docs/CLI.md
README.md
docs/incremental/P7-MANUAL-INCREMENTAL-COMMAND-RESULT.md
```

Not expected / not authorized:

```text
internal/store/postgres/**
internal/runtime/incrementalorch/**
internal/runtime/incrementalexec/**
internal/runtime/scan/**
internal/collector/**
internal/kernel/**
internal/query/**
internal/domain/**
internal/transport/**
internal/store/postgres/migrations/**
```

If implementation requires changes there, stop for Architect review.

## 15. Required tests

### 15.1 Dispatch/help

Prove:

- `indexcore incremental` without subcommand fails;
- unknown incremental subcommand fails;
- `incremental run` is dispatched exactly once;
- top-level help lists `incremental`.

### 15.2 Flag/default validation

Prove defaults:

```text
5 / 5 / 60s / 1000 / 30s / 45s / 60s
```

Prove invalid:

- due attempts 0/6;
- execute items 0/6;
- wall <=0/>60s;
- entries >10000 or otherwise invalid P4 config;
- non-positive retry delays.

Invalid config performs no DB/provider/orchestration work.

### 15.3 Schema fail-closed

Real PostgreSQL:

- incompatible/missing P7-required schema fails before P6;
- no auto-migration;
- no due materialization.

### 15.4 Writer lock exclusion

Real PostgreSQL:

1. acquire existing writer lock in test;
2. invoke P7 command;
3. command fails with `ErrWriterLockHeld` in error chain;
4. zero P6/provider work;
5. release first lock;
6. command can then proceed.

This proves P7 cannot run concurrently with `serve`.

### 15.5 Writer lock release on success/error/cancellation

Prove after:

- successful P6 result;
- P6 runtime error;
- parent cancellation;

the manual command releases writer ownership and a subsequent lock acquisition succeeds.

Use a fresh bounded background context for release.

### 15.6 Structured JSON

Prove:

- exactly one JSON object on stdout after P6 execution;
- snake_case shape;
- P6/P5 counts/stop reasons retained;
- selected last item fields retained;
- no secret-bearing runtime configuration appears.

### 15.7 Result + error ordering

Fake or controlled P6 seam:

- returns populated Result + error;
- JSON is written;
- app returns the original/wrapped error;
- command therefore exits non-zero at top-level.

### 15.8 Real PostgreSQL end-to-end manual command

Real PostgreSQL + httptest AList/OpenList:

Prepare one ACTIVE root + due HOT watch.

Invoke the real P7 app command path.

Prove:

```text
CLI flags
  -> writer lock
  -> P6
  -> due poll
  -> P5/P4
  -> ScanScope
  -> exactly one refresh=true request
  -> Canonical resource visible
  -> Work VERIFIED
  -> JSON result
  -> writer lock released
```

No direct call to lower-level executor from the test as a substitute for the command path.

### 15.9 No-due/preexisting-work path

Through P7 command:

- no due watches;
- preexisting eligible PENDING work;
- P6 still executes it;
- JSON says `due.emitted=0` and executor selected/succeeded as expected.

### 15.10 Normal bounded timeout

Through command/P6:

- P6-owned wall timeout;
- JSON stop_reason `MAX_WALL_TIME`;
- app returns nil;
- process semantics are exit 0;
- if nested claim was interrupted, JSON exposes `interrupted_in_flight=true`;
- no auto-recovery.

### 15.11 Error mapping

Prove non-nil return for:

- parent cancellation;
- materialization error;
- executor/systemic error;
- writer-lock conflict;
- invalid schema/config.

Do not invent additional exit-code classes.

### 15.12 Regression

Required:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
```

Existing P3/P4/P5/P6 tests remain green.

## 16. Result report

Worker must create:

```text
docs/incremental/P7-MANUAL-INCREMENTAL-COMMAND-RESULT.md
```

Report:

- exact CLI syntax;
- flags/defaults;
- writer-lock behavior;
- schema preflight;
- construction path;
- JSON contract;
- exit semantics;
- real-PG end-to-end evidence;
- provider request count;
- lock-release evidence;
- changed production files;
- regression results;
- explicit no-daemon/no-scheduler statement;
- `FROZEN_CONTRACT_CHANGES: NONE`.

## 17. Acceptance criteria

P7 passes only if:

1. one command invocation calls P6 at most once;
2. no second binary is introduced;
3. existing writer advisory lock is required;
4. active serve/writer excludes the command;
5. incompatible schema fails before mutation;
6. no auto-migration occurs;
7. P4/P5/P6 semantics are reused, not copied;
8. all budgets remain within accepted hard caps;
9. one structured JSON result is emitted after P6 execution;
10. Result+error preserves JSON evidence and non-zero exit;
11. normal P6 MAX_WALL_TIME remains a zero-exit bounded result;
12. cancellation/error releases writer ownership;
13. no automatic recovery/repair/retry promotion occurs;
14. real PG command -> P6 -> Canonical path passes;
15. no ticker/sleep/daemon/background mode is added;
16. no HTTP/API/migration/new Canonical write lane is introduced.

## 18. P7 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_PRODUCTION_SCHEDULER_DESIGN
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
P5 bounded executor loop              ARCHITECT_ACCEPTED
P6 scheduler orchestration            ARCHITECT_ACCEPTED
P7 manual incremental command         AUTHORIZED AFTER THIS PLAN MERGES

production polling scheduler          NOT AUTHORIZED
ticker/cadence daemon                 NOT AUTHORIZED
continuous executor service           NOT AUTHORIZED
Mutation Hint API                     NOT AUTHORIZED
native delta/provider cursor          NOT AUTHORIZED
direct 115 integration                NOT AUTHORIZED
destructive delta/removal             NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```
