# Incremental P12 — Deployment Soak with Reference Test Web (Result)

> Status: **SHORTENED TEST VALIDATION — PASSED (fail-closed)**
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

This document records a **shortened test validation** of the P12 deployment-soak
harness, at the task owner's explicit instruction to compress wall-clock time. It
proves the harness drives the real accepted chain end to end (provider mutation →
loopback Mutation Hint → P11 runtime → P0 scoped refresh → canonical → Reference
Test Web rendered visibility) and that **every mandatory scenario is gated
fail-closed**.

It is **not** the final `>=30 min / >=100 visibility` acceptance run. That run
uses the same harness with:

```bash
P12_MODE=acceptance   # P12_DURATION_SECONDS=1800, P12_TARGET_VISIBILITY=100
```

`P12_MODE=full` / `P12_MODE=smoke` are rejected: the shortened profile must never
masquerade as the mandatory long-duration soak.

## 1. Topology actually used

```text
host process   indexcore serve   Query 127.0.0.1:8080   Hint 127.0.0.1:8090 (exact loopback)
docker         postgres:18      127.0.0.1:55433  (volume p12-pgdata)
host process   AList fixture A  127.0.0.1:9050  (root A)
host process   AList fixture B  127.0.0.1:9051  (root B)
host process   Reference Test Web (next start) 127.0.0.1:3100 → INDEXCORE_BASE_URL=http://127.0.0.1:8080
```

The host-process form keeps the Hint endpoint on literal `127.0.0.1` (the
transport rejects anything else) and lets the driver, the provider fixtures, and
the Reference Test Web observer share one host, which is what makes restart/crash
control deterministic.

Soak runtime configuration (P12 §6):

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true
INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s
INDEXCORE_HINT_ADDR=127.0.0.1:8090
INDEXCORE_HINT_TOKEN=<ephemeral 64-hex-char token, never committed>
INDEXCORE_LOG_FORMAT=json
INDEXCORE_LOG_LEVEL=info
```

Repository default remains `INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false`.

## 2. Verification-only implementation

```text
scripts/soak/alist_fixture.py     AList/OpenList-compatible mutable provider fixture
scripts/soak/lib.sh               shared helpers (Hint retry, fixture control, visibility)
scripts/soak/run-p12-soak.sh      orchestration + scenarios + durable check (fail-closed)
scripts/soak/test-hint-429.sh     429 retry-path self-test (mocked curl)
docs/incremental/P12-DEPLOYMENT-SOAK-RESULT.md
```

Fixture surface: `POST /api/fs/list` (full pagination and scoped `refresh=true`),
`POST /api/auth/login`, plus a verification-only control plane
(`/__control/reset|upsert|delete|fail|block|unblock|delay|state`). It never
returns a successful truncation (`data.total == len(content)` of the requested
directory). Roots are bound with `token_env` references only; no provider secret
is persisted.

No production IndexCore code, contract, migration, or default was changed.

## 3. Fail-closed design

`run-p12-soak.sh` collects explicit predicate failures and exits non-zero if any
remain. Each mandatory scenario asserts its acceptance predicates rather than only
writing JSON:

| Scenario | Fail-closed predicates |
| --- | --- |
| steady | every mutation Hint final code `202`; visible within the timeout; `visible >= target` |
| hint burst | no unexpected final Hint code (only `202`/retryable `429`); all 10 burst mutations visible; `incremental_runtime_fatal` count unchanged |
| transient retry | `incremental_retry_promotion` observed; eventual visibility |
| graceful restart | degraded render seen; Reference Test Web held HTTP 200; no origin leak; recovery + later visibility |
| crash recovery | blocked `IN_FLIGHT` confirmed; startup stale-IN_FLIGHT recovery event; eventual visibility; `IN_FLIGHT` residue 0 |
| durable state | 0 unexpected `IN_FLIGHT` / `PENDING` / overdue `RETRY_WAIT` / `BLOCKED·SUSPENDED` / `PENDING` admission; >=2 ACTIVE roots; writer lock released |
| observer lifecycle | the continuous Reference Test Web observer PID must stay alive and its log must contain no `OBSERVER FAILURE` after every scenario |

Hint/backpressure: `hint_send()` retries retryable `429` up to a bounded attempt
count honouring `Retry-After`, with an initialized retryable-429 counter. The
final code is surfaced; a normal mutation that does not end in `202` fails the
run.

The `429` branch is executable without real saturation via
`scripts/soak/test-hint-429.sh` (mocked curl): genuine `429→202`, persistent
`429`, unexpected `500`, and transport `000`.

## 4. Shortened test run result

Command:

```bash
P12_MODE=test P12_DURATION_SECONDS=60 bash scripts/soak/run-p12-soak.sh
```

Outcome: `exit=0`, `P12 SHORTENED TEST VALIDATION completed (fail-closed, no failures)`.

| Observation | Result |
| --- | --- |
| profile | SHORTENED TEST VALIDATION |
| ACTIVE roots / hot scopes | 2 / 2 |
| steady duration | 60 s |
| successful mutation→Test Web visibility | **42** |
| Hint final `202` | 42 (steady) |
| Hint retryable `429` | 0 |
| visibility latency | min 50 ms · p50 55 ms · p95 1089 ms · max 1092 ms (n=42) |
| hint burst | 24 hints → final 202×24, retryable 429×0, unexpected 0; 10/10 visible; runtime fatal unchanged |
| transient retry | retry promotion observed; visible after 30 s (no DB edit / manual run) |
| graceful restart | Test Web held HTTP 200; degraded render seen; no origin leak; recovered + later visibility; `readyz` 503 observed this run |
| crash recovery | blocked `IN_FLIGHT` confirmed; startup recovery event observed; eventual visibility; `IN_FLIGHT` residue 0 |
| durable state | 0 unexpected `IN_FLIGHT` / `PENDING` / overdue `RETRY_WAIT` / `BLOCKED·SUSPENDED` / `PENDING` admission; 2 ACTIVE roots; writer lock released |
| observer lifecycle | continuous observer alive with no `OBSERVER FAILURE` after every scenario |

Latency is a soak observation, not an SLA. p95/max reflect
`INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s` plus the P6 burst cooldown.

The graceful/crash windows are coordinated so the observer never judges a healthy
page as degraded, or an unavailable page as normal: the degraded marker is
written **immediately before** shutdown/crash and is only cleared **after**
IndexCore is ready again.

## 5. Evidence files

Raw evidence written by the harness to `/tmp/p12/evidence/`:

```text
summary.env         run profile/parameters and totals (incl. failures count)
steady.csv          per-mutation visibility records
latency.txt         min/p50/p95/max
burst.json          hint burst final-code classification and visibility
transient.json      retry promotion and eventual visibility
restart.json        degraded / hold-200 / leak / recovery / inflight-created
crash.json          blocked in-flight, recovery events, residue
durable_state.json  final state machine snapshot
```

## 6. Known observation limits (UNKNOWN / prototype concerns)

1. **Not the final acceptance run.** Duration and visibility counts are
   intentionally reduced for a test pass; the mandatory `>=30 min / >=100` run
   must still be executed with `P12_MODE=acceptance`.
2. **`/readyz` 503 observability is timing-dependent.** It was observed in this
   run, but the shutdown order (`ready=false → stopHint → stopIncremental →
   srv.Shutdown`) can close the listener quickly when there is no long drain. It
   remains NON-BLOCKING for the shortened test; readiness-503 semantics are also
   covered by `internal/runtime/app/serve_readiness_test.go`.
3. **Binary provenance.** The index-core binary was extracted from the local
   `index-core-indexcore:latest` image (built at `8227230`); the only diff to the
   Phase-B baseline `20eff94` is documentation (#105), so the Go code is
   identical. A final run may rebuild from `20eff94` for exact provenance.
4. **Gate-4 Reference Test Web regression** (`PASS=52 FAIL=0`) was verified during
   Phase A and the Reference Test Web is unchanged by Phase B; it was not re-run
   inside this shortened pass.

## 7. Boundary declarations

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