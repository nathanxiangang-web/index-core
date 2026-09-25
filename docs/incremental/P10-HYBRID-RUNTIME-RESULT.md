# Incremental P10 — Hybrid Runtime Prototype — Result

> Status: **ARCHITECT_ACCEPTED — MERGED TO MAIN `f655f04`**
>
> Executing issue: #95 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P10-HYBRID-RUNTIME-PROTOTYPE.md`
>
> Planning PR: #94 · Predecessor: P9 trusted hint transport — Issue #91 / PR #92 — ARCHITECT_ACCEPTED (merge `a561f25`)
>
> **SAME SERVE PROCESS / SAME WRITER LOCK · DEFAULT DISABLED · ONE SERIALIZED P6 LOOP · DURABLE-STATE-FIRST HINT WAKE · TRANSIENT/THROTTLED RETRY PROMOTION ONLY · STARTUP STALE-IN_FLIGHT RECOVERY · MAX 4 CYCLES / 20 ITEMS PER BURST · SYSTEMIC FAILURE IS FATAL · QUERY /V1 REMAINS READ-ONLY · NO SECOND WRITER · NO MIGRATION · NO NATIVE DELTA**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Runtime API and dependency boundary

Package `internal/runtime/incrementalruntime`:

```go
type CycleRunner interface {
    RunCycle(ctx context.Context, cfg incrementalorch.Config) (incrementalorch.Result, error)
}

type MaintenanceStore interface {
    ListDueRetryWork(ctx context.Context, now time.Time, limit int) ([]state.DirtyScopeWork, error)
    RetryReady(ctx context.Context, rootID, scopeKey string, expectedVersion int64, now time.Time) (state.DirtyScopeWork, error)
    ListInflightRoots(ctx context.Context, limit int) ([]string, error)
    RecoverStaleInflight(ctx context.Context, rootID string, now time.Time) (int, error)
}

type Config struct {
    WakeInterval       time.Duration
    MaxRetryPromotions int
    Cycle              incrementalorch.Config
}

func New(store MaintenanceStore, cycle CycleRunner, cfg Config, logger *slog.Logger, now func() time.Time) (*Runtime, error)
func (r *Runtime) Wake()
func (r *Runtime) RecoverStartupInflight(ctx context.Context) (int, error)
func (r *Runtime) Run(ctx context.Context) error
```

Hard caps: `MaxRetryPromotionsCap = 5`, `MaxConsecutiveCyclesPerBurst = 4`,
`BurstCooldown = 1s`, `InflightRecoveryBatch = 100`, wake interval `[1s, 60s]`.
`DefaultCycleConfig()` reuses the accepted P7 composition (`5 / 5 / 60s`).

The runtime receives only the narrow P3 maintenance selectors plus one accepted
P6 `RunCycle`. It receives no Query handler, HTTP response writer, raw provider
credential, or second writable connection.

## 2. Startup topology

```text
schema compatible
  -> AcquireWriterLock
  -> bounded stale-IN_FLIGHT startup recovery
  -> existing Gate-3 admission worker
  -> P10 hybrid incremental runtime
  -> bind read-only Query listener
  -> optional P9 Hint listener LAST
```

Disabled (`INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false`, the default) preserves
the P9 topology exactly: no runtime goroutine, no scheduler timer, no recovery.

## 3. Wake / timer semantics

One event loop, one `time.Timer` (re-armed), one capacity-1 `chan struct{}` wake.
`Wake()` is non-blocking and coalescing: a duplicate wake is dropped while one is
pending. Dropping is safe because the P8 signal and due watches are already
durable and the timer is the fallback.

Wake sources: startup, timer, Hint (post-merge), self (bounded backlog).
At most one P6 `RunCycle` is active at a time; the loop never overlaps cycles.

## 4. Hint notifier wiring

`hintapi` production code is unchanged. The app layer wraps the P8 ingester:

```text
hintapi Deps.Ingester = app.notifyingIngester{inner: P8, wake: runtime.Wake}
```

`IngestOne` returns the durable P8 result and only on success calls the
non-blocking `Wake()`. HTTP `202` keeps its durable-merge-only meaning and never
waits for P6/provider execution. `hintapi.Deps` gains no executor/runtime
capability.

## 5. Automatic retry promotion

`ListDueRetryWork(ctx, now, limit)` returns only ACTIVE-root, due
(`pending_not_before <= now`) `RETRY_WAIT` rows whose `last_error_class` is
`TRANSIENT_PROVIDER` or `THROTTLED`, deterministically ordered. P10 promotes at
most `MaxRetryPromotions = 5` per pass via the accepted P3 `RetryReady` CAS; a CAS
conflict is stale state and is skipped (never retried inline). Any other Store
error is fatal.

`INTERNAL`, `AUTH_OR_PERMISSION`, `CONFIG_INVALID`, `INVALID_SCOPE`,
`SCOPE_TOO_LARGE`, `ROOT_INACTIVE`, `BLOCKED`, and `SUSPENDED` are never
auto-promoted.

## 6. Recovery

**Startup.** After writer-lock acquisition and before any listener:
`ListInflightRoots(ctx, 100)` -> `RecoverStaleInflight(rootID)` -> repeat until
drained. No provider I/O. Any error fails `serve` closed with no listener exposed.

**Same-process wall-time interruption.** Only after P6 returned, and only when
`Executor.InterruptedInFlight == true && Last.ClaimedSignalSeq > 0 &&
Last.RootID != ""`, P10 calls `RecoverStaleInflight(last.RootID)` once and never
retries the item inline.

## 7. Backlog burst bound

The burst counter applies to **every immediately-contiguous cycle regardless of
wake source** (timer / Hint / self-backlog); it is reset only by the mandatory
cooldown, so no wake source can start the fifth consecutive cycle. A hard cap
allows at most 4 consecutive cycles / 20 selected P4 attempts per burst, then a 1s
cooldown. Hint floods cannot bypass the cooldown and wakes coalesce while it is
active. Self-continuation is additionally triggered by more due retries than the
promotion budget, `MoreDueWatches`, or executor `MAX_ITEMS`.

## 8. Error policy

Normal bounded stops (`NO_ELIGIBLE_WORK`, `MAX_ITEMS`, `MAX_WALL_TIME`, durable
item-local failures, stale retry-promotion CAS) are not fatal.

**`STALE_SELECTION` is bounded non-fatal contention, not a system failure.** Under
the hybrid runtime a trusted Hint (or due-watch mutation) can legitimately change
a DirtyScopeWork version between P4 selection and claim, so P5 reports
`STALE_SELECTION` and P6 returns a non-nil executor error. P10 detects
`Executor.StopReason == STALE_SELECTION` (or `errors.Is(err,
incrementalexec.ErrStaleSelection)`), never reselects inside the same P6 cycle, and
schedules a later wake — still subject to the burst/cooldown cap. This carve-out
deliberately does **not** cover `INTERNAL_ITEM_FAILURE`, `SYSTEMIC_ERROR`,
`MATERIALIZATION_ERROR`, or unexpected executor/store failures, which remain fatal
and bring `serve` down.

P10 returns a fatal error to `serve` on P6 materialization/systemic executor
error, retry-maintenance Store error, startup recovery error, interrupted-root
recovery error, or unexpected event-loop termination. There is no top-level retry
loop and no silent degraded mode.

## 9. Lifecycle / writer lock

Shutdown order (P10 §16): drain Hint handlers -> cancel+join P10 runtime ->
cancel+join the existing worker -> shut down Query -> release the writer lock. If
the graceful timeout expires while either write-capable actor is alive, the writer
lock keeps being held and the process keeps waiting. Startup failure funnels
through one helper that stops both the runtime and the worker before returning.

## 10. Tests and evidence

P10 tests = **33** (real PostgreSQL where noted):

- **Store selectors** (2, real PG): `TestP10ListDueRetryWorkAllowlist` (only
  TRANSIENT/THROTTLED, ACTIVE root, due, bounded, empty before barrier);
  `TestP10ListInflightRoots` (deterministic sorted, bounded).
- **Config** (4): disabled default; wake-interval bounds `1s..60s` (ignored while
  disabled); flags; env load + invalid env failure.
- **Runtime** (15): config validation; startup recovery drains bounded batches and
  surfaces errors; retry promotion bounded to 5 with `limit = promotions+1`; stale
  CAS skipped without blocking later candidates; maintenance Store error fatal;
  systemic cycle error fatal; interrupted-claim exact-root recovery once; recovery
  error fatal; never-overlapping cycles; burst cap/cooldown; `STALE_SELECTION`
  non-fatal while INTERNAL/systemic stays fatal; external-wake burst cap; cooldown
  resume.
- **App lifecycle + integration** (12, real PG + httptest): disabled regression
  (no runtime constructed); startup recovery requeues `IN_FLIGHT` before Hint
  exposure; startup recovery error fails closed with no Hint exposure; runtime
  fatal is fatal to serve; startup failure joins runtime + worker before the lock
  is acquirable; shutdown with an in-flight (non-cancellable) P10 execution holds
  the writer lock until it stops; Hint wake drives a cycle before a 60s timer;
  timer wakes make zero provider requests for a not-yet-due watch; manual P7
  `incremental run` fails with `ErrWriterLockHeld`;
  **`TestP10IntegrationDueWatchExecutes`** (due watch -> P10 wake -> real
  P6/P5/P4/P0 -> provider refresh + Canonical + VERIFIED);
  **`TestP10IntegrationHintPostExecutesBeforeLongTimer`** (HTTP 202 returns while
  the provider refresh is still blocked, then the Hint wake drives real execution
  before the 60s timer); **`TestP10IntegrationRetryPromotionExecutes`** (due
  TRANSIENT_PROVIDER RETRY_WAIT -> RetryReady -> same-pass real execution ->
  VERIFIED, while INTERNAL stays RETRY_WAIT).

Test provenance: there is no GitHub Actions / check run attached, so the
full-suite result below is submitter-provided local evidence on real PostgreSQL 18,
not an independent CI attestation.

## 11. Changed files

```text
internal/runtime/incrementalruntime/config.go            (new)
internal/runtime/incrementalruntime/runtime.go           (new)
internal/runtime/incrementalruntime/runtime_test.go      (new: 11 tests)
internal/runtime/app/incremental_runtime.go              (new: seams + notifier)
internal/runtime/app/app.go                              (P10 wiring + lifecycle)
internal/runtime/app/serve_runtime_test.go               (new: 9 tests)
internal/runtime/config/config.go                        (P10 config + validation)
internal/runtime/config/runtime_test.go                  (new: 4 tests)
internal/store/postgres/incremental_selector.go          (retry/inflight read selectors)
internal/store/postgres/incremental_p10_selector_test.go (new: 2 tests)
.env.example                                             (P10 env)
docs/OPERATIONS.md                                       (P10 config + operations)
docs/incremental/P10-HYBRID-RUNTIME-RESULT.md            (new)
```

No `internal/kernel/**`, `internal/query/**`, `internal/domain/**`,
`internal/collector/**`, `internal/store/postgres/migrations/**`,
`internal/transport/httpapi/**`, P9 `internal/transport/hintapi/**` production
code, or `cmd/**` change. No migration.

## 12. Regression results

```text
gofmt -l <Go files>             clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

Existing Gate 1–4 and P0–P9 tests remain green.

## 13. Boundary statement

P10 adds one in-process hybrid runtime component inside the existing `indexcore
serve` writer process. It adds no second writer/process/sidecar, no parallel P6
cycles or dirty-scope execution, no Query `/v1` write route, no executor/runtime
capability in `hintapi.Deps`, no automatic `INTERNAL` retry, no automatic
`RepairBlocked`/`ResumeSuspended`, no non-loopback/network Hint transport, no
native delta/provider cursor, no direct 115 integration, no destructive removal,
no migration, and no Gate 5. Provider I/O still occurs only through P4 -> P0.

`FROZEN_CONTRACT_CHANGES: NONE`
## 14. Round 1 rework (Issue #95 review)

Two production blockers + one mandatory-evidence blocker from the P10 Round 1
review were fixed inside the authorized P10 surface
(`internal/runtime/incrementalruntime`, P10 tests, and this report); the accepted
topology/config/lifecycle boundary is unchanged.

1. **`STALE_SELECTION` is bounded non-fatal contention, not fatal.** The runtime
   now inspects the returned P6 result before treating an executor error as fatal:
   `Executor.StopReason == STALE_SELECTION` (or
   `errors.Is(err, incrementalexec.ErrStaleSelection)`) is handled as expected
   hybrid Hint/P4 optimistic-CAS contention — non-fatal, no same-P6-cycle
   reselect, and a later bounded wake. `INTERNAL_ITEM_FAILURE`, `SYSTEMIC_ERROR`,
   `MATERIALIZATION_ERROR`, and unexpected executor/store failures remain fatal.
   Proved by `TestP10StaleSelectionIsNonFatal` and
   `TestP10SystemicErrorStillFatal`.
2. **Burst accounting covers every wake source.** The cycle counter is no longer
   reset when a cycle reports `backlog=false`; it is reset only by the mandatory
   cooldown. Timer, Hint, and self-backlog wakes therefore cannot start the fifth
   consecutive cycle before the 1s cooldown. Proved by
   `TestP10BurstCapAppliesToExternalWakes` (a cycle that both outlives the 1s timer
   and injects an external wake) and `TestP10BurstCooldownThenContinue`.
3. **Mandatory P10 end-to-end evidence added** (real PostgreSQL + httptest
   provider): `TestP10IntegrationDueWatchExecutes`,
   `TestP10IntegrationHintPostExecutesBeforeLongTimer`, and
   `TestP10IntegrationRetryPromotionExecutes` (all in
   `internal/runtime/app/serve_runtime_integration_test.go`), proving the new
   daemon wiring really reaches the accepted P6/P5/P4/P0 path.

P10 tests = 33 (selector 2 / config 4 / runtime 15 / app lifecycle + integration
12).

`FROZEN_CONTRACT_CHANGES: NONE`

## 15. Architect closeout

P10 was accepted after two Architect review rounds and merged to `main` as
`f655f0447dbc7b5be575557e28a4c2d0f705f31c`.

Final acceptance:

```text
same-process / same-writer topology                    PASS
single serialized P6 runtime                          PASS
durable-state-first Hint wake                         PASS
STALE_SELECTION bounded contention                    PASS
retry allowlist                                       PASS
startup / interrupted-claim recovery                  PASS
wake-source-independent burst cap                     PASS
systemic failure fail-closed                          PASS
writer-lock lifecycle / joins                         PASS
real end-to-end P10 evidence                          PASS
frozen contract expansion                             NONE

P10 Hybrid Runtime                                    ARCHITECT_ACCEPTED
FROZEN_CONTRACT_CHANGES                               NONE
```

Evidence provenance remains explicit: the reported full-suite `gofmt`,
`go vet ./...`, and `go test -p 1 -count=1 ./...` results were submitter-provided
local evidence on PostgreSQL 18; no GitHub Actions/check run was attached to the
P10 branch.

Non-blocking hardening note: the prototype burst counter is intentionally
conservative and is reset only after the mandatory cooldown, so a widely
separated idle cycle can occasionally incur an unnecessary one-second cooldown.
This reduces throughput/latency only; it does not weaken the P10 safety bound.

P10 exit decision:

```text
AUTHORIZE_PRODUCTION_HYBRID_RUNTIME_HARDENING
```

This authorizes the next design/hardening phase around operability, lifecycle,
observability, cadence behavior, test/CI evidence, and production defaults. It
does **not** authorize native delta/provider cursor, network-reachable Hint
transport, a second writer, destructive removal, migration, or Gate 5.
