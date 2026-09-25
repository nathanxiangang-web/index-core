# Incremental P11 — Production Hybrid Runtime Hardening — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL + RACE VERIFIED — ARCHITECT REVIEW PENDING**
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

**Blocker (environment credential scope).** The workflow content is committed at
`docs/ci/ci.yml`, not `.github/workflows/ci.yml`: this environment's GitHub
credential is an OAuth App token with token scopes only `read:org`, `repo`, and
GitHub rejects pushes that create/update `.github/workflows/**` without the
`workflow` scope:

```text
! [remote rejected] ... (refusing to allow an OAuth App to create or update
workflow `.github/workflows/ci.yml` without `workflow` scope)
```

Per the plan's rule to stop rather than drop evidence, the exact YAML is committed
at `docs/ci/ci.yml` (with a header explaining the intended location) so it is
reviewable in Git. To activate CI, an actor with a `workflow`-scoped credential
must copy/move it to `.github/workflows/ci.yml`. No path outside the authorized
surface was modified.

Race result locally (same commands the workflow would run): the targeted race
suite passed on real PostgreSQL 18 for both `internal/runtime/incrementalruntime`
and `internal/runtime/app` (no race reported).

**Provenance note:** until a GitHub Actions run exists for this head (which
requires the `workflow`-scoped push above), the local
`gofmt`/`vet`/`test`/`build`/`race` results remain submitter-provided evidence.

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
docs/ci/ci.yml                                             (new: intended PostgreSQL 18 CI workflow; see H5 blocker)
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

P11 tests = 13 (burst 6 deterministic + runtime hardening 4 + app readiness 3).
Existing Gate 1–4 and P0–P10 tests remain green.

## 10. Boundary statement

P11 adds no new indexing/product capability. It changes no Query Q1–Q9, adds no
public HTTP endpoint, no `/readyz` schema change, no metrics endpoint, no second
writer/multi-daemon HA, no parallel P6 cycles, no automatic INTERNAL retry or
BLOCKED/SUSPENDED repair, no network Hint transport, no native delta/provider
cursor, no direct 115, no destructive removal, no migration/schema expansion, no
default-on runtime, and no Gate 5.

`FROZEN_CONTRACT_CHANGES: NONE`