# Incremental P12 — Deployment Soak with Reference Test Web (Result)

> Status: **HARNESS IMPLEMENTED — SHORTENED VALIDATION RUN PASSED**
>
> Issue: `nathanxiangang-web/index-core#103` (P12 Phase B)
>
> Branch: `validation/incremental-p12-deployment-soak`
>
> Baseline: `20eff944338ac884e1b0eba0a31b404d2573ce10`
>
> Reference Test Web: PR #5 head `98884cb94beb300e8a4167007ac7a84114b03a57` (Phase A, Architect accepted)
>
> Plan: `docs/architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md`

## 0. Scope of this result

This document records a **shortened validation run** of the P12 deployment-soak
harness, at the task owner's explicit instruction to compress the wall-clock
time. It proves the harness drives the real accepted chain end to end (provider
mutation → loopback Mutation Hint → P11 runtime → P0 scoped refresh → canonical →
Reference Test Web rendered visibility) and that every mandatory scenario is
exercised with real components.

It is **not** the final `>=30 min / >=100 visibility` acceptance run. That longer
run remains to be scheduled before final P12 acceptance; the same harness and the
same evidence schema produce it by setting `P12_MODE=full` (defaults
`P12_DURATION_SECONDS=1800`, `P12_TARGET_VISIBILITY=100`).

## 1. Topology actually used

```text
host process   indexcore serve   Query 127.0.0.1:8080   Hint 127.0.0.1:8090 (exact loopback)
docker         postgres:18      127.0.0.1:55433  (volume p12-pgdata)
host process   AList fixture A  127.0.0.1:9050  (root A)
host process   AList fixture B  127.0.0.1:9051  (root B)
host process   Reference Test Web (next start) 127.0.0.1:3100 → INDEXCORE_BASE_URL=http://127.0.0.1:8080
```

The host-process form is deliberate: the Hint endpoint must stay on literal
`127.0.0.1` (the transport rejects anything else), and the driver, the provider
fixtures, and the Reference Test Web observer then share one host, which is what
makes the restart/crash control deterministic.

Soak runtime configuration used (P12 §6):

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true
INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s
INDEXCORE_HINT_ADDR=127.0.0.1:8090
INDEXCORE_HINT_TOKEN=<ephemeral 64-hex-char token, never committed>
INDEXCORE_LOG_FORMAT=json
INDEXCORE_LOG_LEVEL=info
```

Repository default remains `INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false`.

## 2. New verification-only implementation

```text
scripts/soak/alist_fixture.py    AList/OpenList-compatible mutable provider fixture
scripts/soak/lib.sh              shared helpers (hint, fixture control, visibility)
scripts/soak/run-p12-soak.sh     orchestration: provision + steady + scenarios + durable check
```

Fixture surface: `POST /api/fs/list` (full pagination and scoped `refresh=true`),
`POST /api/auth/login`, plus a verification-only control plane
(`/__control/reset|upsert|delete|fail|block|unblock|delay|state`). It never
returns a successful truncation (`data.total == len(content)` of the requested
directory). Roots are bound with `token_env` references only; no provider secret
is persisted.

No production IndexCore code, contract, migration, or default was changed.

## 3. Shortened run result

Command:

```bash
P12_MODE=full P12_DURATION_SECONDS=120 P12_TARGET_VISIBILITY=10 P12_BURST_HINTS=24 \
  bash scripts/soak/run-p12-soak.sh
```

Outcome: `exit=0`, `P12 soak completed`.

| Observation | Result |
| --- | --- |
| ACTIVE roots | 2 (root A `/hot-a`, root B `/hot-b`) |
| hot scopes | 2 |
| steady duration | 120 s |
| successful mutation→Test Web visibility | **40** |
| Hint durable `202` | 40 / 40 |
| Hint `429` | 0 |
| visibility latency | min 49 ms · p50 55 ms · p95 1089 ms · max 1095 ms (n=40) |

Latency is a soak observation, not an SLA. The high p95/max reflect the
`INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s` acceleration setting plus the P6 burst
cooldown, not provider slowness.

### 3.1 Steady cycle

Each cycle: mutate fixture direct-child state → timestamp → authenticated Hint to
the exact scope → durable `202` → wait for the Reference Test Web observer to
confirm rendered visibility (`--wait-visible`) → record latency. No cycle failed
within the 60 s visibility budget.

### 3.2 Hint burst

24 rapid valid Hints across both hot scopes (repeated same-scope Hints included):
`202`×24, `429`×0, no `incremental_runtime_fatal`, all 10 burst mutations became
Test Web-visible. One timer/hint runtime remained one serialized P6 executor.

### 3.3 Provider transient → automatic retry

One injected provider `500` on `/hot-a`: `retry_promotion_seen=true`, and the
mutation became visible **30 s** later with no DB edit and no manual
`incremental run`. No automatic INTERNAL promotion occurred.

### 3.4 Graceful restart

Reference Test Web stayed up while IndexCore shut down and restarted:

- Test Web kept serving **HTTP 200** and showed its accepted degraded state;
- the private IndexCore origin never appeared in rendered HTML;
- after restart the same Test Web recovered and later mutations became visible
  again without restarting the Web.

`/readyz` reaching `503` was **not observable** in this run — see §5.

### 3.5 Deterministic crash recovery

The fixture blocked one scoped request after P4 claimed the work
(`IN_FLIGHT` confirmed), IndexCore was hard-killed, the provider was restored,
and IndexCore was restarted on the same PostgreSQL volume. Startup stale-
IN_FLIGHT recovery ran (`incremental_inflight_recovered`), the work became
executable again, and the resource became Test Web-visible; no `IN_FLIGHT`
residue remained.

### 3.6 Final durable state

```text
unexpected IN_FLIGHT             0
unresolved PENDING work          0
overdue RETRY_WAIT               0
BLOCKED / SUSPENDED              0
PENDING admission                0
ACTIVE roots                     2
writer lock free after shutdown  true
work_state                       VERIFIED:2
admission_status                 APPLIED:47  NOOP:10
root_lifecycle                   ACTIVE:2
```

## 4. Evidence files

Raw evidence written by the harness to `/tmp/p12/evidence/`:

```text
summary.env         run parameters and totals
steady.csv          per-mutation visibility records
latency.txt         min/p50/p95/max
burst.json          hint burst codes and visibility
transient.json      retry promotion and eventual visibility
restart.json        degraded / hold-200 / leak / recovery
crash.json          blocked in-flight, recovery events, residue
durable_state.json  final state machine snapshot
```

## 5. Known observation limits (UNKNOWN / prototype concerns)

1. **`/readyz` 503 window is not network-observable in this deployment.** The
   shutdown order is `ready=false → stopHint → stopIncremental → srv.Shutdown`
   (`internal/runtime/app/app.go:345-358`); with no long writer drain the Query
   listener closes before a probe can catch the 503. Eight concurrent tight
   `/readyz` pollers recorded only connection failures around SIGTERM. The
   readiness-503 semantics themselves are covered by the accepted
   `internal/runtime/app/serve_readiness_test.go`. This run therefore records
   `readyz_503=false` truthfully rather than claiming it.
2. **This is not the final acceptance run.** Duration and visibility counts are
   intentionally reduced for a test pass; the mandatory `>=30 min / >=100` run
   must still be executed and recorded before final P12 acceptance.
3. **Binary provenance.** The index-core binary was extracted from the local
   `index-core-indexcore:latest` image (built at `8227230`); the only diff between
   `8227230` and the Phase-B baseline `20eff94` is documentation (#105), so the
   Go code is identical. A final run may rebuild from `20eff94` for exact
   provenance.
4. **Gate-4 Reference Test Web regression** (`PASS=52 FAIL=0`) was verified during
   Phase A and the Reference Test Web is unchanged by Phase B; it was not re-run
   inside this shortened pass.

## 6. Boundary declarations

```text
INDEXCORE_CONTRACT_CHANGES: NONE
REFERENCE_WEB_BOUNDARY_CHANGES: NONE
RUNTIME_DEFAULT_ON: NO
NATIVE_DELTA: NO
NETWORK_HINT: NO
SECOND_WRITER: NO
MIGRATION: NO
GATE5: NO
FROZEN_CONTRACT_CHANGES: NONE
```