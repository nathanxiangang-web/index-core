# Incremental P1 — Adaptive Hot-Scope Polling Feasibility — Result

> Status: **LIVE EXECUTED — EXTERNAL CHANGE DISCOVERED WITHOUT A MUTATION HINT, WITHIN ONE INTERVAL**
>
> **P1 overall PASS / merge decision: reserved for the Architect** (live evidence now available)
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
| `max_hot_scopes` caps only the HOT set (WARM/COLD do not consume it) | `TestPollHarnessHonorsMaxHotScopes`, `TestPollHarnessMaxHotScopesCountsOnlyHot` | PASS |
| only HOT (not WARM/COLD) polled | `TestPollHarnessPollsOnlyHotScopes` | PASS |
| 403 refresh-permission fails closed (once, no retry, no mutation) | `TestPollHarnessActuatorFailureFailsClosed/403` | PASS |
| provider/list error fails closed (once, no retry, no mutation) | `TestPollHarnessActuatorFailureFailsClosed/provider_error` | PASS |
| over `max_entries` fails closed through the harness (no mutation) | `TestPollHarnessOverMaxEntriesFailsClosed` | PASS |
| 2-cycle reconcile safety on real PostgreSQL | `TestPollHarnessReconcileSafety` | PASS |

### Round 1 Architect-review rework (2026-09-24, test-only)

PR #67 Architect Round 1 = PRE-LIVE CHANGES REQUIRED. All 7 BLOCKERs + the `maxHotScopes`
semantic fix are addressed **without any production change**:

- **`maxHotScopes`** now caps only the HOT set (`addScope` + `hotCount`);
- **failure paths** added through the actual P0 `ScanScope` actuator — 403 refresh-permission,
  provider/list error, and over-`max_entries` — each asserted to surface once (no tight retry) and
  to leave Canonical state unmutated (deterministic harness + real PostgreSQL);
- **live probe reworked** (still gated; not yet run) to satisfy BLOCKERs 1-6: a read-only
  `refresh=false` stale-cache gate before the cycle; seeding `lastPolled` from the previous poll and
  requiring the interval to have elapsed (next-due poll, not time-zero); `INDEXCORE_P1_LIVE_EXPECT_SCOPE`
  change attribution (`Mutated=true` for the changed scope, `false` for the rest); real nested Q4 for
  the expected parent with Q3/Q4/Q6 same-id; baseline `path -> resource_id` preservation + no-removal
  audit; per-scope canonical `refresh=true` count == 1 / status 200; `T6-T1 <= HOT interval`; and the
  actual `page_size` supplied via env (no hard-coded 200).

### Round 2 Architect-review rework (2026-09-24, test-only)

PR #67 Architect Round 2 = 3 residual acceptance gaps + 2 input guards, addressed test-only:

1. **The stale-cache gate must read the whole directory in one coherent response.** The gate now uses
   `per_page = maxEntries + 1` (not a fixed 200) and **requires `total == len(content)`**, so a target
   sitting beyond page 1 cannot be mis-read as "still not visible".
2. **The cycle must actually poll every due HOT scope within budget.** Phase B asserts
   `due == polled == len(scopes)` and **`budgetExhausted == false`**.
3. **No extra refresh path.** The proxy now also counts **all** `refresh=true` observations globally
   (`refreshTotal`); Phase B asserts `refreshTotal() == len(polled)` — exactly one canonical refresh per
   polled scope and none anywhere else.

Input guards: `EXPECT_SCOPE` must be one of the HOT scopes, and `parentOf(EXPECT_PATH)` must equal
`EXPECT_SCOPE`.

Live 115 validation remains **NOT AUTHORIZED** until the Architect reviews this rework.

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

## 6. Live P1 experiment — EXECUTED

Environment: non-production OpenList **v4.2.6 / commit `2bdf16d`**; storage **id=2 `driver="115 Open"`**
(official API, `page_size=200`, `limit_rate=1`, `cache_expiration=30 min`); dedicated non-admin
service identity `test` (write-capable); HOT scopes **`/` `/hotA` `/hotB` `/hotC`** (4 small dirs);
out-of-band write through the official 115 channel.

### Live result (2026-09-24, UTC)

| Step | Time / value |
| --- | --- |
| Phase A baseline | 14 canonical resources; `PHASE_A_POLL_TS = 2026-09-24T03:14:32Z` |
| T1 out-of-band upload | `p1-new.txt` into `/hotA` via official 115 channel; **T1 = 2026-09-24T03:19:30Z** |
| Stale cache gate (read-only `refresh=false`) | `03:19:56Z` → `/hotA` status 200, **total=1, content=1**, names `[keep.txt]` → **`p1-new.txt` NOT visible** ✅ (not `NOT JUDICABLE`) |
| Phase B poll cycle | `due=4 polled=[/ /hotA /hotB /hotC]`, `wall=4111 ms`, **`budget_exhausted=false`** |
| T6 visibility | **2026-09-24T03:20:00Z** |

Per-scope canonical metrics (each scope: exactly **one** `refresh=true`, HTTP **200**):

| scope | total | canonical refresh | status | latency | response bytes | page_size | derived provider pages |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `/` | 11 | 1 | 200 | 1128 ms | 3437 | 200 | 1 |
| `/hotA` | **2** | 1 | 200 | 959 ms | 813 | 200 | 1 |
| `/hotB` | 1 | 1 | 200 | 1049 ms | 480 | 200 | 1 |
| `/hotC` | 1 | 1 | 200 | 910 ms | 480 | 200 | 1 |

Outcome assertions (all PASS):

- **the external write was discovered with no Mutation Hint**: `added = [/hotA/p1-new.txt]`
  (before 14 → after 15), exactly one new resource;
- **`/hotA` `Mutated=true`; the other polled HOT scopes `Mutated=false`**;
- **Q3 / real Q4 / Q6 agree on the same `resource_id`** for the new file;
- all baseline `resource_id`s preserved; **no removal evidence** (Q7 empty; zero PRESENT rows
  carrying removal evidence);
- **`due == polled == 4`**, `budgetExhausted=false`, **total `refresh=true` count == 4**
  (no extra refresh path);
- **`T6 − T1 = 30.3 s` ≤ one HOT interval (120 s)**;
- no 403 / 429 / throttle; **no retries**.

> Detection occurred within one configured interval on a **stale-cache external write**, using only the
> accepted single-observation P0 path per due scope — no root-wide traversal, no provider mutation.

### Protocol

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
# Phase A (baseline, 3-5 HOT scopes) — prints PHASE_A_POLL_TS=...
INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB,/hotC" \
INDEXCORE_TEST_DATABASE_URL=postgres://... \
  go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseABaseline -v -count=1

# [operator: out-of-band upload of /hotA/new.txt; note T1 time]
#   Phase B requires: PREV_POLL_TS (Phase A), T1_TS (upload), PAGE_SIZE (actual storage setting),
#   HOT_INTERVAL (seconds, default 120). Phase B first performs a read-only refresh=false stale gate.
INDEXCORE_P1_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P1_LIVE_USER=... INDEXCORE_P1_LIVE_PASS=... \
INDEXCORE_P1_LIVE_SCOPES="/,/hotA,/hotB,/hotC" \
INDEXCORE_P1_LIVE_EXPECT_SCOPE=/hotA INDEXCORE_P1_LIVE_EXPECT_PATH=/hotA/new.txt \
INDEXCORE_P1_LIVE_PAGE_SIZE=200 \
INDEXCORE_P1_LIVE_PREV_POLL_TS=<RFC3339> INDEXCORE_P1_LIVE_T1_TS=<RFC3339> \
INDEXCORE_P1_LIVE_HOT_INTERVAL=120 \
INDEXCORE_TEST_DATABASE_URL=postgres://... \
  go test ./internal/runtime/scan -run TestLiveP1HotScopesPhaseBCycle -v -count=1
```

## 7. Verdict

Deterministic selection/budget behavior, additive-safe reconcile through the polling harness, and
**live execution on a real stale-cache external write** are all **verified**: the new file was
discovered without a Mutation Hint, within one configured interval (`T6−T1 = 30.3 s ≤ 120 s`), with
budgets enforced (`due == polled == 4`, no budget exhaustion, exactly one canonical refresh per due
scope), the changed scope mutated and the unchanged scopes did not, Q3/Q4/Q6 agreed on one id, and no
removal evidence was produced.

**P1 overall acceptance and the exit decision** (`STOP_POLLING` / `KEEP_P0_MANUAL_ONLY` /
`AUTHORIZE_MUTATION_HINT_INTEGRATION` / `AUTHORIZE_HYBRID_HINT_PLUS_HOT_POLLING` /
`AUTHORIZE_DIRTY_SCOPE_STATE_DESIGN` / `RESEARCH_FURTHER`) **remain with the Architect**.
Production scheduler/polling remains unauthorized; PR #67 does not merge.

## 8. Frozen-contract statement

- `FROZEN_CONTRACT_CHANGES: NONE`.
- No production scheduler, `serve` polling loop, DB migration, persistent dirty-scope queue,
  production sync CLI, public trigger API, direct 115 client, native cursor/delta, or destructive
  delta. Gate 5 not started.