# Incremental P1 — Adaptive Hot-Scope Polling Feasibility — Result

> Status: **PROTOTYPE DELIVERED — DETERMINISTIC + POSTGRESQL VERIFIED — LIVE P1 EXPERIMENT PENDING**
>
> **P1 NOT PASS** (live 115 Open hot-scope polling not yet executed)
>
> Executing issue: #66 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P1-HOT-SCOPE-POLLING-PROTOTYPE.md`
>
> Predecessor: #62 / PR #64 (P0 scoped refresh) — ARCHITECT_ACCEPTED
>
> **Production incremental sync / scheduler: NOT AUTHORIZED**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. What P1 tests

P0 proved the **actuator** (known changed directory -> one `refresh=true` observation -> PARTIAL
additive-safe reconcile). P1 tests the missing **detector**: with no Mutation Hint, can a small,
explicitly bounded set of HOT directories be force-refreshed at ~1–2 min cadence so external
writes become visible materially earlier than normal OpenList cache expiry?

P1 is a **feasibility prototype**, not a production scheduler.

## 2. Implementation (test-only; no production runtime change)

| File | Purpose |
| --- | --- |
| `internal/runtime/scan/scoped_poll_harness_test.go` | In-memory cadence/budget harness: due-scope selection, `max_scopes_per_cycle`, `minimum_scope_interval`, wall-time budget, no parent/child collapse. Drives the accepted `scan.Service.ScanScope`. |
| `internal/runtime/scan/scoped_poll_deterministic_test.go` | Fake-clock selection/budget tests. |
| `internal/runtime/scan/scoped_poll_safety_test.go` | Two bounded cycles on real PostgreSQL + deterministic AList mock: additive-safe reconcile. |
| `internal/runtime/scan/scoped_poll_live_test.go` | Env-gated live P1 probe (Phase A/B) with a recording proxy. |

**No existing production file was modified.** No runtime scheduler, no `serve` change, no DB
migration, no CLI, no public API, no direct 115 client. The harness is `_test.go`-only.

Prototype defaults (Issue #66 §6): `max_hot_scopes=5`, `max_scopes_per_cycle=5`,
`max_cycle_wall_time=60s`, `max_entries_per_scope=1000`, `minimum_scope_interval=120s`.

## 3. Deterministic evidence (PASS)

| Property | Test | Result |
| --- | --- | --- |
| only due HOT scopes selected; min interval honored | `TestPollHarnessSelectsOnlyDueScopes` | PASS |
| `max_scopes_per_cycle` enforced | `TestPollHarnessHonorsMaxScopesPerCycle` | PASS |
| wall-time budget stops further work + recorded | `TestPollHarnessStopsOnWallTimeBudget` | PASS |
| a failing scope is not retried in a loop | `TestPollHarnessErrorScopeDoesNotLoopUnbounded` | PASS |
| parent/child scopes not collapsed | `TestPollHarnessDoesNotCollapseParentChild` | PASS |
| `max_hot_scopes` enforced | `TestPollHarnessHonorsMaxHotScopes` | PASS |
| only HOT (not WARM/COLD) polled | `TestPollHarnessPollsOnlyHotScopes` | PASS |
| 2-cycle reconcile safety on real PostgreSQL | `TestPollHarnessReconcileSafety` | PASS |

## 4. Reconcile safety evidence (real PostgreSQL)

`TestPollHarnessReconcileSafety` (hot scopes `/`, `/a`, `/b`; out-of-band write simulated as a new
file in `/a` between cycles):

- cycle 0 (baseline): all three HOT scopes `APPLIED`, `Mutated=true`;
- cycle 1: `/a` `APPLIED`/`Mutated=true`; `/` and `/b` `APPLIED`/**`Mutated=false`**;
- **exactly one** new resource (`/a/a2.txt`); all prior resources remain (`/root.txt`, `/a/a1.txt`,
  `/b/b1.txt`, `/a`, `/b`);
- **no removal evidence** (`Q7` empty; `removal_evidence_state=NONE`, `missing_since` NULL,
  counter 0);
- **same-root FIFO** intact: `admission_seq = 1..6` (2 cycles × 3 scopes).

### Observation worth recording (pre-existing Kernel semantics, not introduced by P1)

Across a **generation change**, an unchanged scope is reported `Status=APPLIED` but
**`Mutated=false`** — the Kernel records the application (to advance `applied_generation`) without
mutating Canonical state. A repeat at the **same** generation with the same identity is `NOOP`.
P1 therefore expresses "unchanged scopes are safe" as **no canonical mutation + no new resource +
no removal evidence**, which is stronger than an outcome-label check.

## 5. Regression

- `go test -p 1 ./...` — green;
- `go vet ./...` — clean; `gofmt` — clean;
- no schema/migration; no Q1–Q9 change; full `Scan()`, P0 `ScanScope()`, and rclone unchanged.

## 6. Live P1 experiment (PENDING — requires environment)

Environment (same class as P0): non-production OpenList; storage = community `115 Open` driver
(official API); dedicated non-admin service identity with write permission; **3–5 small HOT
directories**; out-of-band write through the official 115 channel.

Sequence:

```text
Phase A  baseline cycle for all HOT scopes
Cycle 0  refresh=false confirms current cached state
T1       operator uploads exactly one new file OUT-OF-BAND into one HOT directory
before next poll  refresh=false for that scope still does NOT expose the file
Phase B  poll cycle: refresh each due HOT scope once, within budget
Expected changed scope adds exactly the new resource; unchanged scopes mutate nothing;
         Q3/Q4/Q6 expose the new resource; no removal evidence; no unrelated loss
```

Detection must occur within one configured HOT polling interval.

Metrics to record per scope: `path`, direct-child `total`, canonical `refresh=true` count, HTTP
status, HTTP latency, response bytes, 115 Open `page_size`, **derived** provider-page estimate
`ceil(total/page_size)` (labelled derived, not measured), error/403/429/throttle state. Per cycle:
due scopes, polled scopes, cycle wall time, budget exhaustion, changed-scope T1->Q3/Q4/Q6
visibility latency, retries.

Reproduce:

```bash
INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB" \
INDEXCORE_P1_LIVE_EXPECT_PATH=/hotA/new.txt \
INDEXCORE_TEST_DATABASE_URL=postgres://... \
  go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseABaseline -v -count=1
# [operator: out-of-band upload]
#   refresh=false must still not expose the new file (T2 stale gate)
INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB" \
INDEXCORE_P1_LIVE_EXPECT_PATH=/hotA/new.txt \
INDEXCORE_TEST_DATABASE_URL=postgres://... \
  go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseBCycle -v -count=1
```

## 7. Verdict

Deterministic selection/budget behavior and additive-safe reconcile through the polling harness
are **verified**. The **live P1 experiment has not run**, so **P1 is NOT PASS** and the exit
decision (`STOP_POLLING` / `KEEP_P0_MANUAL_ONLY` / `AUTHORIZE_MUTATION_HINT_INTEGRATION` /
`AUTHORIZE_HYBRID_HINT_PLUS_HOT_POLLING` / `AUTHORIZE_DIRTY_SCOPE_STATE_DESIGN` /
`RESEARCH_FURTHER`) remains with the Architect. Production scheduler/polling remains unauthorized.

## 8. Frozen-contract statement

- `FROZEN_CONTRACT_CHANGES: NONE`.
- No production scheduler, `serve` polling loop, DB migration, persistent dirty-scope queue,
  production sync CLI, public trigger API, direct 115 client, native cursor/delta, or destructive
  delta. Gate 5 not started.