# Incremental P7 — Manual Incremental Command Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #85 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P7-MANUAL-INCREMENTAL-COMMAND-PROTOTYPE.md`
>
> Planning PR: #84 · Predecessor: P6 scheduler orchestration — Issue #82 / PR #83 — ARCHITECT_ACCEPTED (merge `3aa02bb`)
>
> **ONE-SHOT COMMAND ONLY · REUSES EXISTING WRITER LOCK · CALLS P6 EXACTLY ONCE · JSON STDOUT · NO PRODUCTION SCHEDULER · NO TICKER/CADENCE · NO BACKGROUND/DAEMON MODE · NO AUTO RECOVERY · NO HTTP WRITE API · NO MIGRATION**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. CLI syntax

```text
indexcore incremental run [flags]
```

- `indexcore incremental` (no subcommand) fails;
- unknown/reserved subcommands (`watch`, `daemon`, `recover`, `hint`) fail;
- `run` is dispatched exactly once;
- no second binary is introduced.

`indexcore`'s top-level help lists `incremental  run one bounded manual
incremental orchestration cycle`.

## 2. Command-local flags and defaults

```text
--max-due-watch-attempts   5      bound 1..5
--max-execute-items        5      bound 1..5
--max-wall-time            60s    bound (0,60s]
--max-entries-per-scope    1000   bound 0..10000
--retry-transient-provider 30s    bound >0
--retry-throttled          45s    bound >0
--retry-internal           60s    bound >0
```

Command-local only: no new persistent/global config field or environment
variable is added. Existing runtime flags/env (`--database-url`, `--rclone-path`,
`--rclone-config`, `--scan-timeout`, `--shutdown-timeout`, logging) are reused.

Invalid P4/P5/P6 budgets are rejected **before** opening the database, acquiring
writer ownership, constructing the chain, or touching the provider (validated via
the accepted `incrementalexec.Config.Validate` and
`incrementalorch.Config.Validate`).

## 3. Runtime order

```text
parse + validate flags
  -> validate existing config
  -> validate P4/P6 configs
  -> open DB + ping
  -> SchemaStatus + require Compatible()
  -> AcquireWriterLock
  -> scan.New
  -> incrementalexec.New (P4)
  -> incrementalexec.NewCycleRunner (P5)
  -> incrementalorch.NewRunner (P6)
  -> P6 RunCycle exactly once
  -> render JSON result (P6 ran)
  -> return P6 error if any
  -> release writer lock
```

`*postgres.Store`, `*scan.Service`, `*incrementalexec.Executor`,
`*incrementalexec.CycleRunner`, and `*incrementalorch.Runner` are reused; no
selector SQL, `EmitDuePoll`, `ClaimWork`, `ScanScope`, P5 loop, or P6
orchestration logic is copied.

## 4. Schema preflight

Before writer ownership and before any incremental mutation the command opens
PostgreSQL from existing runtime config, pings, calls `postgres.SchemaStatus`, and
requires `Compatible()`. A missing/future/incompatible schema fails closed
(`postgres.SchemaError`). There is **no auto-migrate**.

## 5. Writer-lock behavior

The command uses the existing `Store.AcquireWriterLock` and the existing lock key
(same as `serve`):

```go
lock, err := st.AcquireWriterLock(ctx)
```

- `postgres.ErrWriterLockHeld` fails immediately — no wait/spin/sleep/retry;
- no P6/provider work and no operational mutation on conflict;
- an active `indexcore serve` writer on the same database therefore excludes the
  command;
- the lock is released on a fresh bounded background context
  (`context.WithTimeout(context.Background(), cfg.ShutdownTimeout)`), so a
  cancelled command context cannot strand the dedicated advisory-lock connection.

The schema preflight runs **before** lock acquisition, so an incompatible schema
is reported as a schema error (not a lock error).

## 6. JSON contract

After the single P6 cycle the command emits exactly one snake_case JSON object to
stdout (RFC3339Nano timestamps):

```json
{
  "command": "incremental run",
  "stop_reason": "COMPLETED",
  "started_at": "2026-09-25T00:00:00Z",
  "finished_at": "2026-09-25T00:00:01Z",
  "observed_at": "2026-09-25T00:00:00Z",
  "due": {
    "candidates": 1, "attempted": 1, "emitted": 1, "stale": 0,
    "more_due_watches": false, "materialization_interrupted": false
  },
  "executor": {
    "ran": true, "stop_reason": "NO_ELIGIBLE_WORK",
    "invocations": 1, "selected_items": 1, "succeeded": 1, "failed": 0,
    "interrupted_in_flight": false,
    "last": {
      "selected": true, "root_id": "...", "scope_key": "/",
      "snapshot_id": "...", "final_work_state": "VERIFIED",
      "failure_class": null
    }
  }
}
```

`executor.last` is `null` when no item was selected. The JSON never contains the
database DSN, provider username/password/token, adapter config, or secret
environment values. Logs and errors stay on stderr.

## 7. Result + error ordering

If P6 ran and returns a populated `Result` plus a non-nil error, the command:

1. writes the structured JSON result to stdout;
2. returns the original/wrapped error;
3. `main` prints it to stderr and exits non-zero.

Preflight failures before P6 (invalid flags/config, DB open/ping failure,
incompatible schema, writer-lock held, construction failure) return only an error
with the existing stderr path and no JSON result.

## 8. Exit semantics

The existing process contract is reused — no new numeric exit-code classes:

```text
nil app error     -> exit 0
non-nil app error -> exit 1
```

Therefore: `COMPLETED` → 0; normal bounded P6 `MAX_WALL_TIME` → 0 (P6 returns nil
error); parent cancellation → 1; `MATERIALIZATION_ERROR` → 1; `EXECUTOR_ERROR` →
1; writer-lock conflict → 1; invalid config/schema → 1.

## 9. Real PostgreSQL evidence

`TestP7RealPGEndToEnd` — real PostgreSQL + httptest AList through the **real app
command path** (`app.RunIncremental`, not a lower-level executor call), one ACTIVE
root + due HOT watch:

```text
CLI flags
  -> writer lock
  -> P6
  -> due poll
  -> P5/P4
  -> ScanScope
  -> exactly one refresh=true request
  -> Canonical /a.txt PRESENT
  -> Work VERIFIED
  -> JSON result (stop_reason=COMPLETED, due.emitted=1, executor.succeeded=1)
  -> writer lock released
```

`TestP7IncrementalNoDueDrainsPending` — no due watches; a preexisting eligible
PENDING item still drains through P6 (`due.emitted=0`, `executor.succeeded=1`,
Canonical visible, Work VERIFIED).

**Provider request count = exactly 1** for the one-watch end-to-end path.

## 10. Lock-release evidence

`TestP7IncrementalReleasesWriterLock` proves a subsequent lock acquisition
succeeds after each of:

- a successful P6 result;
- a P6 runtime error;
- parent cancellation during the cycle (release uses a fresh bounded background
  context).

`TestP7IncrementalWriterLockExclusion` proves an externally held writer lock makes
the command fail with `postgres.ErrWriterLockHeld`, with **zero** orchestration
calls, and that the command proceeds once the lock is released.

## 11. Other tests

- `TestP7TopLevelHelpListsIncremental` / `TestP7DispatchMissingAndUnknownIncrementalSubcommand`
  / `TestP7DispatchRunSubcommandFailsOnConfigNotUnknown` (dispatch/help,
  cmd layer);
- `TestP7IncrementalDispatch`, `TestP7IncrementalInvalidConfigPerformsNoWork`
  (defaults/bounds, zero DB/provider/orchestration work on invalid config);
- `TestP7IncrementalDefaults` (exact 5/5/60s/1000/30s/45s/60s defaults observed
  by the P6/P4 configs);
- `TestP7IncrementalSchemaFailClosed` (missing schema fails before P6/lock);
- `TestP7IncrementalJSONContractAndResultErrorOrdering` (one JSON object,
  snake_case shape, nested P6/P5 evidence retained, no DSN/secret, non-nil error
  still returned);
- `TestP7IncrementalNormalTimeoutIsZeroExit` (MAX_WALL_TIME result, nil error,
  `interrupted_in_flight` exposed);
- `TestP7IncrementalErrorMapping` (parent cancellation / materialization /
  executor errors all surface non-nil).

Test seams (app layer only): `runIncrementalCycle` (P6 construction/invocation)
and `incrementalStdout` (JSON writer). Production uses the real chain and
`os.Stdout`.

## 12. Changed files

```text
cmd/indexcore/main.go                                (incremental dispatch + usage)
internal/runtime/app/incremental.go                  (new: command implementation)
cmd/indexcore/main_test.go                           (new: 3 tests)
internal/runtime/app/incremental_test.go             (new: 11 tests)
docs/CLI.md                                          (incremental section)
README.md                                            (manual cycle usage)
docs/incremental/P7-MANUAL-INCREMENTAL-COMMAND-RESULT.md (new)
```

No `internal/store/postgres/**`, no `internal/runtime/incrementalorch/**`, no
`internal/runtime/incrementalexec/**`, no `internal/runtime/scan/**`, no
`internal/collector/**`, no `internal/kernel/**`, no `internal/query/**`, no
`internal/domain/**`, no `internal/transport/**`, no migration, and no
`go.mod`/`go.sum` change.

## 13. Regression results

```text
gofmt -l <Go files>             clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

P7 tests = **14** (cmd 3 + app 11). Existing P3/P4/P5/P6 tests remain green.

## 14. Boundary statement

P7 adds one manual, one-shot CLI command. It adds no production polling
scheduler, ticker, cron/cadence loop, sleep/wait-until-due, background/daemon
mode, loop/repeat/watch mode, second binary, automatic
`RetryReady`/`ResumeSuspended`/`RepairBlocked`/`RecoverStaleInflight`, Mutation
Hint API, native delta/provider cursor, direct 115 integration, destructive
removal, migration, public HTTP write surface, or Gate 5. It does not call
`EmitDuePoll`, `MergeSignal`, or any recovery primitive directly — it only calls
P6 once. A parent cancellation may leave a claimed Work `IN_FLIGHT`; P7 performs
no "cleanup" that would change the accepted durable state machine.

`FROZEN_CONTRACT_CHANGES: NONE`