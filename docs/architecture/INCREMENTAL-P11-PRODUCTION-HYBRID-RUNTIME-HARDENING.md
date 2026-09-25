# Incremental P11 — Production Hybrid Runtime Hardening

> Status: **ARCHITECT AUTHORIZED — HARDENING IMPLEMENTATION AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P10 Hybrid Runtime Prototype — Issue #95 / PR #96 — **ARCHITECT_ACCEPTED**
>
> P10 merge: `f655f0447dbc7b5be575557e28a4c2d0f705f31c`
>
> P10 closeout: PR #97 / `eddf95d232b261bac24f019621d1e3285ffb3c8b`
>
> P10 exit decision: **AUTHORIZE_PRODUCTION_HYBRID_RUNTIME_HARDENING**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P10 proved that IndexCore can safely run a continuously useful incremental path
inside the existing `indexcore serve` process while preserving one PostgreSQL
writer-lock owner, a read-only Query API, bounded trusted Hint ingress, bounded
retry promotion, crash recovery, and serialized P6 execution.

P11 is deliberately **not a feature phase**.

It hardens the accepted P10 runtime for long-lived deployment by removing
prototype-only operational rough edges and by replacing submitter-only test
claims with repeatable CI evidence.

P11 answers:

> Can the accepted P10 hybrid runtime become an operationally coherent,
> independently verified production candidate without broadening IndexCore's
> frozen responsibility boundary?

## 2. P11 authority boundary

### Authorized

- correct P10 burst accounting so idle time itself satisfies the required
  cooldown and no unnecessary extra cooldown is added;
- fail-closed progress detection for startup stale-`IN_FLIGHT` recovery;
- lifecycle/readiness hardening using the existing `/readyz` contract;
- richer structured runtime logs using existing `log/slog`;
- GitHub Actions CI using PostgreSQL 18 and the repository's accepted test
  commands;
- targeted race testing for the hybrid runtime/app lifecycle where practical;
- current operator/project-memory documentation synchronization;
- tests/evidence/result report.

### Not authorized

- changing Query Q1–Q9;
- adding a new public HTTP endpoint;
- changing `/readyz` response schema;
- metrics/prometheus endpoint;
- second writer / multi-daemon HA;
- parallel P6 cycles or parallel dirty-scope execution;
- automatic INTERNAL retry;
- automatic BLOCKED/SUSPENDED repair;
- network-reachable Hint transport;
- native provider delta/cursor;
- direct 115 integration;
- destructive removal;
- migration/schema expansion;
- enabling the incremental runtime by default;
- Gate 5.

## 3. Production-candidate posture

P11 keeps:

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false
```

as the default.

P11 may make the opt-in runtime safer and better documented, but **default-on is
a separate future Architect decision** after hardening evidence and deployment
soak.

Likewise:

- Hint listener remains default disabled;
- Hint remains exact-loopback-only;
- Query remains read-only;
- P7 remains mutually exclusive through the same writer lock.

## 4. Hardening H1 — true burst / idle semantics

P10 correctly prevents Hint/timer wakes from bypassing:

```text
MaxConsecutiveCyclesPerBurst = 4
BurstCooldown                = 1s
```

but its accepted prototype counter is conservative: it resets only after an
explicit cooldown. Therefore four cycles separated by long idle periods can
still cause an unnecessary one-second sleep before a later cycle.

P11 must preserve the safety bound while making the semantic definition exact:

> A burst is a sequence of cycle starts with **less than BurstCooldown of idle
> time after the previous cycle finished**.

The cooldown budget is time between cycles, not time spent inside a cycle.

### Required model

Track at minimum:

```text
burst_count
last_cycle_finished_at
```

Before starting a cycle:

1. if no previous cycle exists, begin a new burst;
2. if `now - last_cycle_finished_at >= BurstCooldown`, idle time already
   satisfied the cooldown:
   - reset burst_count;
   - start immediately;
3. else if `burst_count < MaxConsecutiveCyclesPerBurst`, start immediately;
4. else wait only the **remaining** duration until:
   `last_cycle_finished_at + BurstCooldown`;
5. after that wait, reset burst_count and start the next burst.

After a cycle finishes, stamp `last_cycle_finished_at` and increment the
current burst count.

### Hard invariants

- no fifth immediately-contiguous cycle before one full second of post-cycle idle;
- Hint/timer/self wakes cannot bypass the bound;
- a long-running cycle does not count as the post-cycle cooldown;
- a real idle interval >=1s does satisfy the cooldown;
- no unbounded wake queue;
- max active P6 cycles remains 1.

Prefer deterministic clock/timer seams in tests; do not make the core tests rely
only on wall-clock sleeps.

## 5. Hardening H2 — startup recovery must prove progress

P10 startup recovery loops:

```text
ListInflightRoots
 -> RecoverStaleInflight(root)
 -> repeat until empty
```

Under the accepted real Store this should drain under exclusive writer
ownership, but a production loop must not be able to spin forever if the Store
or future code violates that assumption.

P11 must fail closed on non-progress.

### Required rule

If `ListInflightRoots` returns a root that is claimed to have stale
`IN_FLIGHT` work, then `RecoverStaleInflight(root)` must recover at least one
row for that root.

If it returns zero:

```text
startup recovery non-progress
 -> fatal error
 -> no Query listener
 -> no Hint listener
 -> writer lock released only after normal startup cleanup
```

Equivalent batch-level proof is acceptable only if it guarantees the loop can
never repeat the same durable state forever.

No retry/backoff loop is authorized here.

No provider request is allowed during startup recovery.

## 6. Hardening H3 — readiness reflects serve lifecycle

P11 uses the existing read-only `GET /readyz` contract only.

No new HTTP route and no response-schema change.

The app-level `Ready func() bool` must represent the serve process as a whole,
not merely "the worker goroutine was launched."

### Required behavior

`/readyz` must be not-ready when:

- serve startup has not completed;
- a shutdown/fatal path has begun;
- the process is draining a long-running P10 cycle or worker before writer-lock
  release.

This is especially important because P10 deliberately keeps Query reachable
while Hint/P10/worker actors may take longer than the graceful timeout to stop.

At the beginning of every serve shutdown/fatal branch:

```text
service_ready = false
```

must occur before waiting on long actor joins.

The Query server may still be reachable for a short drain window, but `/readyz`
must return 503 so a load balancer/orchestrator stops routing new traffic.

### Startup

Set service readiness true only after:

- writer ownership exists;
- P10 startup recovery (if enabled) succeeded;
- worker/runtime actors were started;
- Query listener successfully bound;
- optional Hint listener successfully bound.

Do not change `httpapi.Deps` into a write-capable dependency.

## 7. Hardening H4 — structured operational observability

No new metrics subsystem is authorized.

Use existing `log/slog`.

P11 should make runtime events useful enough to diagnose a long-running process
without raw DB/provider access.

Minimum event behavior:

### Runtime wake/cycle

```text
incremental_runtime_wake
  wake_reason
  burst_count

incremental_cycle_finished
  stop_reason
  duration_ms
  due_candidates
  due_attempted
  due_emitted
  more_due_watches
  selected_items
  succeeded
  failed
  interrupted_in_flight
  backlog
```

### Maintenance/recovery

```text
incremental_retry_promotion
  candidates
  attempted
  promoted
  stale
  more

incremental_inflight_recovered
  phase=startup|interrupted
  root_id (when one root)
  recovered

incremental_runtime_fatal
  error_class
  phase
```

Exact field names may differ, but secrets must never appear.

Do not log:

- Hint bearer token/header;
- DB URL;
- raw provider credentials;
- adapter secret environment values.

Wake observability may distinguish at least:

```text
startup
timer
hint
backlog
contention
```

without turning the wake channel into an unbounded payload queue.

A bounded bitmask/coalesced-reason design is acceptable.

## 8. Hardening H5 — independent CI evidence

P0–P10 repeatedly recorded that GitHub had no workflow/check evidence.

P11 closes that provenance gap.

Add:

```text
.github/workflows/ci.yml
```

Preferred CI baseline:

- checkout;
- Go version from `go.mod`;
- PostgreSQL 18 service;
- module cache;
- `go vet ./...`;
- `go test -p 1 -count=1 ./...`;
- build `./...`.

Set an explicit job timeout.

The PostgreSQL DSN must be supplied through:

```text
INDEXCORE_TEST_DATABASE_URL
```

and must not contain production credentials.

### Race evidence

Also run a targeted race suite if stable within normal CI time, preferred:

```text
go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
```

with the same PostgreSQL 18 service for app integration tests.

If the targeted race suite exposes an existing test-only shared-global seam that
cannot be safely fixed inside P11, stop for Architect review rather than simply
dropping race evidence.

Do not add external SaaS CI dependencies.

## 9. Hardening H6 — repository memory and operator docs become current

The repository currently contains stale P8/P9-era text in project-state and
operations documents.

P11 must make Git the authoritative project memory again.

At minimum update:

```text
PROJECT-STATE.md
NEXT-ACTIONS.md
PROJECT-CONTEXT.md
README.md
docs/README.md
docs/OPERATIONS.md
docs/CLI.md (only where incremental-runtime behavior is described)
.env.example
```

Required current truth:

- P0–P10 are ARCHITECT_ACCEPTED;
- P11 is the active hardening phase;
- continuous in-process incremental execution exists when explicitly enabled;
- P9's old "stop serve before P7 can execute hints" prototype limitation is no
  longer the current enabled-P10 behavior;
- P7 remains a manual diagnostic path and remains writer-lock-exclusive;
- runtime remains default disabled;
- no native delta/provider cursor;
- Gate 5 remains unauthorized.

Historical P9 result/architecture documents may keep historical statements when
clearly scoped to P9; current operator docs must not present superseded P9
limitations as present behavior.

## 10. No new persistence or scheduling mechanism

P11 adds no migration and no new scheduler table.

P11 continues to reuse:

```text
ScopeWatchState
DirtyScopeWork
P3 transitions
P4 ExecuteOne
P5 CycleRunner
P6 RunCycle
P8 MergeSignal
P9 Hint transport
P10 incrementalruntime
```

Do not build:

- another queue;
- another retry engine;
- another lease;
- another daemon;
- another canonical-write path.

## 11. Expected production change surface

Authorized production changes are expected primarily in:

```text
internal/runtime/incrementalruntime/**
internal/runtime/app/app.go
internal/runtime/app/incremental_runtime.go   (only if wake/readiness composition needs it)
.github/workflows/ci.yml
docs/**
README.md
PROJECT-STATE.md
NEXT-ACTIONS.md
PROJECT-CONTEXT.md
.env.example
```

Small test-only helper changes elsewhere are allowed.

No expected Store production change is required for H2; if a Store change is
proposed, it must remain read-only/diagnostic or stop for Architect review.

Not authorized production changes:

```text
internal/kernel/**
internal/domain/**
internal/query/**
internal/collector/**
internal/store/postgres/migrations/**
internal/transport/httpapi/**   # public transport behavior remains frozen
internal/transport/hintapi/**   # P9 transport capability remains frozen
```

If implementation believes one of these production areas is required, stop and
ask the Architect.

## 12. Mandatory tests

### 12.1 Idle already satisfies cooldown

Use deterministic time if practical.

Prove:

- run four cycles;
- allow >= BurstCooldown of true post-cycle idle;
- next wake starts immediately;
- no extra one-second sleep.

### 12.2 Fifth contiguous cycle still waits

Prove:

- four cycles complete with < BurstCooldown idle between them;
- fifth wake is already pending;
- cycle #5 cannot start until the remaining cooldown completes;
- max concurrent cycles = 1.

### 12.3 Long cycle does not satisfy post-cycle cooldown

Prove:

- fourth cycle itself runs > BurstCooldown;
- fifth wake is queued during it;
- after cycle #4 finishes, P11 still waits one post-cycle cooldown before #5.

### 12.4 Mixed wake storm

Generate startup/Hint/backlog/timer-equivalent wakes.

Prove:

- wake state stays bounded;
- no fifth contiguous cycle bypass;
- idle reset works;
- no cycle overlap.

### 12.5 Startup recovery no-progress

Inject:

```text
ListInflightRoots -> [r1]
RecoverStaleInflight(r1) -> 0, nil
```

Assert:

- recovery returns fatal non-progress error;
- `serve` fails closed;
- Query/Hint never expose;
- no infinite loop.

### 12.6 Startup recovery normal regression

Real PostgreSQL:

- one or more IN_FLIGHT rows;
- P11 recovery drains them;
- startup proceeds;
- no provider I/O during recovery.

### 12.7 Readiness during steady state

Real serve:

- after successful startup, `GET /readyz` returns 200.

### 12.8 Readiness flips before long shutdown drain

Block P10 or worker so shutdown cannot finish immediately.

Trigger shutdown.

While Query is still reachable during drain:

- `GET /readyz` returns 503;
- second writer still cannot acquire the lock;
- after actor release, serve exits and lock becomes acquirable.

### 12.9 Readiness on fatal runtime path

Inject P10 fatal error while Query is reachable.

Prove readiness becomes false before long cleanup/join.

### 12.10 Structured log fields

Use an in-memory slog handler or captured structured output.

Prove required events/fields exist and no configured secrets are emitted.

At minimum cover:

- timer wake;
- Hint wake;
- retry promotion with stale;
- normal cycle;
- fatal phase;
- startup recovery.

### 12.11 CI workflow validation

The implementation PR must show GitHub workflow/check evidence for its own head.

Architect acceptance requires:

```text
go vet                       PASS
full PostgreSQL test suite   PASS
build                        PASS
targeted race suite          PASS or explicit Architect-reviewed blocker
```

Do not accept only a worker-authored text claim if GitHub Actions is available.

### 12.12 Documentation regression

Tests or review must verify current docs no longer claim:

- P9 is the active phase;
- production scheduler is universally absent;
- Hint work always requires stopping serve when P10 is enabled.

Historical documents may retain historical phase-specific text.

### 12.13 Full regression

Required locally and in CI:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
go build ./...
```

## 13. Result report

Worker must create:

```text
docs/incremental/P11-PRODUCTION-HYBRID-RUNTIME-HARDENING-RESULT.md
```

It must record:

- exact burst algorithm;
- idle/cooldown proof;
- startup recovery progress guard;
- readiness lifecycle proof;
- structured log schema/examples without secrets;
- CI workflow/check URLs or identifiers;
- race-test result;
- changed files;
- full regression results;
- current documentation synchronization;
- `FROZEN_CONTRACT_CHANGES: NONE`.

## 14. Acceptance criteria

P11 passes only if:

1. P10's four-cycle safety cap remains intact;
2. real >=1s post-cycle idle satisfies cooldown without extra delay;
3. time spent inside a long cycle does not count as cooldown;
4. all wake sources remain bounded/coalesced;
5. startup stale recovery cannot livelock on zero progress;
6. startup recovery remains provider-I/O-free;
7. `/readyz` is false during startup and shutdown/fatal draining;
8. `/readyz` schema and Query Q1–Q9 remain unchanged;
9. logs provide actionable runtime lifecycle/cycle/retry/recovery fields;
10. no secret values are logged;
11. PostgreSQL 18 GitHub CI runs on the implementation PR;
12. full tests/build pass in CI;
13. targeted race evidence passes unless the Architect explicitly reviews a
    concrete blocker;
14. current docs accurately describe P10/P11 behavior;
15. incremental runtime remains default disabled;
16. no migration/new queue/new writer/native delta/network Hint/destructive
    removal/Gate 5 is introduced.

## 15. P11 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_DEPLOYMENT_SOAK
AUTHORIZE_INCREMENTAL_RUNTIME_DEFAULT_ON_DESIGN
AUTHORIZE_NATIVE_DELTA_DESIGN
RESEARCH_FURTHER
```

No exit is pre-authorized.

Default-on and native-delta decisions are explicitly separate from P11.

## 16. Current authorization

```text
P0 scoped refresh                         ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility          ARCHITECT_ACCEPTED
P2 durable scope-state design             ARCHITECT_ACCEPTED
P3 state persistence prototype            ARCHITECT_ACCEPTED
P4 one-shot dirty executor                ARCHITECT_ACCEPTED
P5 bounded executor loop                  ARCHITECT_ACCEPTED
P6 scheduler orchestration                ARCHITECT_ACCEPTED
P7 manual incremental command             ARCHITECT_ACCEPTED
P8 mutation hint ingestion                ARCHITECT_ACCEPTED
P9 trusted hint transport                 ARCHITECT_ACCEPTED
P10 hybrid runtime                        ARCHITECT_ACCEPTED
P11 production hybrid runtime hardening   AUTHORIZED AFTER THIS PLAN MERGES

incremental runtime default-on             NOT AUTHORIZED
second writer / multi-daemon HA            NOT AUTHORIZED
parallel P6 cycles                         NOT AUTHORIZED
automatic INTERNAL retry                   NOT AUTHORIZED
automatic BLOCKED/SUSPENDED repair          NOT AUTHORIZED
network-reachable Hint transport            NOT AUTHORIZED
native delta/provider cursor                NOT AUTHORIZED
direct 115 integration                      NOT AUTHORIZED
destructive delta/removal                   NOT AUTHORIZED
migration/schema expansion                  NOT AUTHORIZED
Gate 5                                      NOT AUTHORIZED

FROZEN_CONTRACT_CHANGES                     NONE
```
