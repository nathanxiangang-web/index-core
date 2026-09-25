# Incremental P11 — Production Hybrid Runtime Hardening — Result

> Status: **ARCHITECT_ACCEPTED — MERGED TO MAIN `ec980a5`**
>
> Executing issue: #99 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P11-PRODUCTION-HYBRID-RUNTIME-HARDENING.md`
>
> Planning PR: #98 (merged `329210c`) · Predecessor: P10 hybrid runtime — Issue #95 / PR #96 (merged `f655f04`), closeout PR #97
>
> **P10 SAFETY BOUND PRESERVED · TRUE POST-CYCLE IDLE COOLDOWN · STARTUP RECOVERY PROGRESS GUARDED · READYZ LIFECYCLE HARDENED · STRUCTURED SLOG OBSERVABILITY · POSTGRESQL 18 GITHUB CI · TARGETED RACE EVIDENCE · RUNTIME DEFAULT DISABLED · QUERY /V1 UNCHANGED · NO SECOND WRITER · NO MIGRATION · NO NATIVE DELTA · NO NETWORK HINT**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. H1 — exact burst / idle algorithm

A burst is a sequence of cycle starts with **less than `BurstCooldown` of idle
time after the previous cycle finished**. Time spent *inside* a cycle is not
idle. Constants are unchanged: `MaxConsecutiveCyclesPerBurst = 4`,
`BurstCooldown = 1s`.

`Runtime` tracks `burstCount` and `lastCycleFinishedAt` (touched only by the
single event loop). Before starting a cycle:

```text
if lastCycleFinishedAt is zero:            start immediately (new burst)
else if now - lastCycleFinishedAt >= 1s:   reset burstCount; start immediately
else if burstCount < 4:                    start immediately
else:                                      wait only (lastCycleFinishedAt + 1s - now);
                                           then reset burstCount and start the next burst
```

After a cycle finishes, `lastCycleFinishedAt = now` and `burstCount++`.
`now` and `sleep` are injectable seams (`now func() time.Time`, `sleep
func(ctx, time.Duration) bool`), so the burst algorithm is unit-tested
deterministically without wall-clock sleeps.

## 2. H1 evidence

Deterministic (in-package, `p11_burst_internal_test.go`):

- `TestP11BurstFirstCycleStartsImmediately` — no previous cycle starts a new burst;
- `TestP11BurstIdleSatisfiesCooldown` — a full `>= 1s` post-cycle idle returns 0
  delay and resets the burst (no unnecessary extra cooldown);
- `TestP11BurstFifthContiguousWaitsRemaining` — 4 cycles + 200ms idle → waits
  exactly the remaining 800ms;
- `TestP11LongCycleDoesNotCountAsCooldown` — the fourth cycle ran long, but only
  10ms of post-cycle idle remains, so the cooldown budget is still ~full;
- `TestP11BurstUnderCapStartsImmediately` — below the cap starts immediately;
- `TestP11MarkCycleFinishedStampsAndCounts`.

Behavioural (external, existing P10 regressions still green):
`TestP10BurstCapAppliesToExternalWakes` (a cycle that outlives the 1s timer and
injects an external wake yields at most 4 contiguous cycles),
`TestP10BurstCooldownThenContinue`, `TestP10NeverOverlapsCycles`.

## 3. H2 — startup recovery progress guard

`RecoverStartupInflight` drains stale `IN_FLIGHT` roots in bounded batches
(`InflightRecoveryBatch = 100`). If `ListInflightRoots` returns roots but a whole
batch recovers **zero** rows, the durable state cannot advance, so recovery
returns a fatal non-progress error instead of looping forever. No retry/backoff
loop and no provider I/O. `serve` then fails closed with no Query/Hint listener
and the writer lock released only through the normal startup cleanup path.

Evidence:

- `TestP11StartupRecoveryZeroProgressFailsClosed` — `["r1"]` + `recover 0` → fatal
  "no progress" error, total 0;
- `TestP11StartupRecoveryLogHasStartupPhase` — `phase=startup` log emitted;
- `TestP10StartupRecoveryBeforeHintExposure` (real PostgreSQL) — normal recovery
  still drains `IN_FLIGHT` to `PENDING` before Hint exposure.

## 4. H3 — readiness lifecycle

`internal/transport/httpapi` production behavior is unchanged; P11 uses the
existing `Ready func() bool` seam. Service readiness now represents the whole
`serve` process:

- set **true only after** writer ownership + P10 startup recovery + worker/runtime
  actors started + Query listener bound + optional Hint listener bound;
- **set false at the beginning of every shutdown/fatal branch**, before any long
  actor join, so `/readyz` returns 503 while the process is draining.

Evidence (real PostgreSQL, `serve_readiness_test.go`):

- `TestP11ReadyzSteadyState` — `/readyz` returns 200 after successful startup;
- `TestP11ReadyzFlipsBeforeLongShutdownDrain` — with a blocked P10 cycle, shutdown
  flips `/readyz` to 503 while Query is still reachable, the second writer still
  cannot acquire the lock, and the lock is released only after the cycle stops;
- `TestP11ReadyzFalseOnFatalBeforeCleanup` — a fatal runtime path flips readiness
  to 503 before the blocked cleanup/join completes.

`/readyz` response schema and Query Q1–Q9 are unchanged.

## 5. H4 — structured slog observability

Existing `log/slog` only; no metrics subsystem. Wake sources are a bounded
coalesced bitmask (`startup` / `contention` / `hint` / `backlog` / `timer`) behind
the unchanged capacity-1 wake channel — no unbounded payload queue.

Events and fields:

```text
incremental_runtime_wake       wake_reason, burst_count
incremental_cycle_finished     stop_reason, duration_ms, due_candidates, due_attempted,
                               due_emitted, more_due_watches, selected_items, succeeded,
                               failed, interrupted_in_flight, backlog
incremental_retry_promotion    candidates, attempted, promoted, stale, more
incremental_inflight_recovered phase=startup|interrupted, root_id, recovered
incremental_runtime_fatal      error_class, phase
incremental_runtime_started/stopped
```

Never logged: Hint token/header, DB URL, provider credentials, adapter secret env
values.

Evidence: `TestP11RuntimeStructuredLogFields` captures JSON slog output and asserts
the required events/fields exist and that no secret-looking value
(`supersecret`, `INDEXCORE_HINT_TOKEN`, `postgres://`) appears;
`TestP11RuntimeFatalLogHasPhase` asserts the fatal event carries `phase`.

## 6. H5 — GitHub Actions CI

The intended workflow (PostgreSQL 18 service) is:

```text
checkout -> setup-go (go-version-file: go.mod, module cache)
  -> gofmt check
  -> go vet ./...
  -> go build ./...
  -> go test -p 1 -count=1 ./...        (PostgreSQL 18 service)
  -> go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
```

PostgreSQL 18 runs as a service container with disposable CI-only credentials fed
through `INDEXCORE_TEST_DATABASE_URL`; the job has an explicit
`timeout-minutes: 30`. No external SaaS CI dependency.

**Workflow activation.** The workflow was authored in this environment but could
not be pushed from it: the credential is an OAuth App token with only
`read:org`/`repo` scopes, and GitHub rejects pushes that create/update
`.github/workflows/**` without the `workflow` scope:

```text
! [remote rejected] ... (refusing to allow an OAuth App to create or update
workflow `.github/workflows/ci.yml` without `workflow` scope)
```

A rename/move to `.github/workflows/ci.yml` cannot bypass this — the check is on
the destination path. The exact YAML was therefore committed at `docs/ci/ci.yml`
for review, and the **Architect activated it with a `workflow`-scoped credential**:
`.github/workflows/ci.yml` now exists on this branch (`bd4fc53`), and the temporary
`docs/ci/ci.yml` copy was removed (`5040b75`) so there is only one authoritative
copy. No path outside the authorized surface was modified.

Race result locally (same commands the workflow runs): the targeted race suite
passed on real PostgreSQL 18 for both `internal/runtime/incrementalruntime` and
`internal/runtime/app` (no race reported).

**Provenance:** with `.github/workflows/ci.yml` active, the gofmt / vet / build /
full PostgreSQL suite / targeted race results are produced by GitHub Actions on the
PR head; the local runs above are the pre-push equivalent of the same commands.

## 7. H6 — documentation synchronization

Updated to current truth (P11 stated as the active implementation phase, plan #98
merged, execution Issue #99 open):

- `PROJECT-STATE.md`
- `NEXT-ACTIONS.md`
- `PROJECT-CONTEXT.md`
- `README.md`, `docs/README.md`, `docs/OPERATIONS.md`, `docs/CLI.md`, `.env.example`
  were already aligned by planning PR #98 and verified to state: P0–P10
  `ARCHITECT_ACCEPTED`; P11 active hardening; continuous in-process incremental
  execution exists when explicitly enabled; P7 remains a manual, writer-lock
  exclusive diagnostic; runtime default disabled; no native delta; Gate 5
  unauthorized.

A grep review confirms current docs no longer claim P9 is active, that a
production scheduler is universally absent, or that Hint work always requires
stopping `serve` when P10 is enabled.

## 8. Changed files

```text
internal/runtime/incrementalruntime/runtime.go             (burst/idle + recovery guard + slog)
internal/runtime/incrementalruntime/runtime_test.go        (H2/H4 tests)
internal/runtime/incrementalruntime/p11_burst_internal_test.go (new: deterministic H1 tests)
internal/runtime/app/app.go                                (readiness lifecycle)
internal/runtime/app/serve_runtime_test.go                 (fakeRuntime block-then-fatal)
internal/runtime/app/serve_readiness_test.go               (new: readiness tests)
.github/workflows/ci.yml                                   (PostgreSQL 18 CI, activated by the Architect: bd4fc53)
PROJECT-STATE.md
NEXT-ACTIONS.md
PROJECT-CONTEXT.md
docs/incremental/P11-PRODUCTION-HYBRID-RUNTIME-HARDENING-RESULT.md (new)
```

No `internal/kernel/**`, `internal/domain/**`, `internal/query/**`,
`internal/collector/**`, `internal/store/postgres/migrations/**`,
`internal/transport/httpapi/**`, or `internal/transport/hintapi/**` change. No
migration. Runtime remains default disabled.

## 9. Regression results

```text
gofmt -l <Go files>                  clean
go vet ./...                         clean
go build ./...                       ok
go test -p 1 -count=1 ./...          all packages ok (real PostgreSQL 18)
go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app   ok
```

P11 tests = 20 (burst 6 deterministic + runtime hardening 11 + app readiness 3).
Existing Gate 1–4 and P0–P10 tests remain green.

## 10. Boundary statement

P11 adds no new indexing/product capability. It changes no Query Q1–Q9, adds no
public HTTP endpoint, no `/readyz` schema change, no metrics endpoint, no second
writer/multi-daemon HA, no parallel P6 cycles, no automatic INTERNAL retry or
BLOCKED/SUSPENDED repair, no network Hint transport, no native delta/provider
cursor, no direct 115, no destructive removal, no migration/schema expansion, no
default-on runtime, and no Gate 5.

`FROZEN_CONTRACT_CHANGES: NONE`
## 11. Round 1 rework (Issue #99 review)

Three blockers from the P11 Round 1 review were fixed inside the authorized P11
surface; the accepted H1/H2/H3 direction and the frozen boundaries are unchanged.

1. **Timer wake no longer schedules a phantom cycle.** `wakeWith` was split into
   `recordWake` (merge a wake reason into the bounded bitmask only) and `wakeWith`
   (record **and** enqueue one coalesced token). The timer branch now calls
   `recordWake(wakeTimer)` because the timer event itself is the wake; previously
   it enqueued a second token that produced an immediate extra cycle (one timer
   expiry → two cycles). Invariant restored: **one timer expiry → at most one cycle
   start**. Hint/backlog lost-wakeup protection is unchanged. Proved by
   `TestP11TimerExpiryIsOneCycle` (startup + one 1s timer expiry = exactly 2 cycles)
   and `TestP11RepeatedTimersRemainOneCycleEach` (startup + two expiries = exactly 3
   cycles, `wake_reason=timer` recorded).
2. **Startup/fatal structured observability.** `RecoverStartupInflight` now emits
   `incremental_runtime_fatal` with `phase=startup_recovery` and a bounded
   `error_class` (`store` / `no_progress`) before returning. Runtime fatals carry a
   bounded phase via `runtimeFatalError`: `retry_maintenance`, `cycle`,
   `interrupted_recovery`. The error type's `Error()` never includes the raw cause
   (which may carry DB/provider/secret material); the cause is retained only for
   `Unwrap`. Proved by `TestP11StartupRecoveryNonProgressFatalLog`,
   `TestP11StartupRecoveryStoreErrorFatalLog`, `TestP11SystemicCycleFatalLogPhase`,
   `TestP11RetryMaintenanceFatalLogPhase`,
   `TestP11InterruptedRecoveryFatalLogPhase`, each also asserting that injected
   `postgres://…supersecret…` material never appears in the captured logs.
3. **H5 CI evidence** is now active: the Architect moved the reviewed workflow to
   `.github/workflows/ci.yml` (`bd4fc53`) and removed the temporary `docs/ci/ci.yml`
   copy (`5040b75`). GitHub Actions therefore runs gofmt / vet / build / the full
   PostgreSQL 18 suite / the targeted race suite on the PR head; the earlier
   Worker-side push limitation (credential lacking the `workflow` scope) no longer
   blocks H5.

`FROZEN_CONTRACT_CHANGES: NONE`

## 12. Architect closeout

P11 was accepted after two Architect review rounds and merged to `main` as
`ec980a5e00aa562cc3bd6d74c5e6f03aa46f280c`.

Final independent GitHub Actions evidence on the accepted head:

```text
gofmt                                             PASS
go vet ./...                                      PASS
go build ./...                                    PASS
Full PostgreSQL 18 test suite                     PASS
Targeted race suite (incrementalruntime + app)    PASS
```

Final acceptance:

```text
true post-cycle burst semantics                    PASS
startup recovery progress guard                    PASS
readiness lifecycle                                PASS
structured operational observability               PASS
PostgreSQL 18 GitHub CI                            PASS
targeted race evidence                             PASS
repository/operator documentation                  PASS
frozen contract expansion                          NONE

P11 Production Hybrid Runtime Hardening            ARCHITECT_ACCEPTED
FROZEN_CONTRACT_CHANGES                            NONE
```

P11 exit decision:

```text
AUTHORIZE_DEPLOYMENT_SOAK
```

Deployment soak is the next bounded step. It validates the current opt-in,
default-disabled runtime under real deployment duration and operational conditions.
This exit does **not** authorize runtime default-on, native delta/provider cursor,
network/public Hint, second writer/multi-daemon HA, migration/schema expansion,
destructive removal, direct 115 integration, or Gate 5.
