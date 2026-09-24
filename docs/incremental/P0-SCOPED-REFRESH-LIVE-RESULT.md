# P0 Targeted Scoped Refresh — Live 115 Open Result (Round 2 evidence rework)

> Status: **LIVE EXECUTED — Phase A/B — INCREMENTAL SEMANTICS, REAL Q4, T3→T6 TIMING, HTTP/bytes — ALL OBSERVED**
>
> P0 overall PASS / merge decision: **reserved for the Architect**
>
> Execution issue: #62 · Parent architecture: #57
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Environment (non-production)

- OpenList **v4.2.6 / commit `2bdf16d`** at `http://127.0.0.1:5244` (container `openlist-p0`).
- storage **id=2, `driver="115 Open"`** (community-maintained driver over the **official
  115 Open API**), mount `/115open`, `status=work`, `page_size=200`, `limit_rate=1`,
  `cache_expiration=30` min. Driver judged from the authoritative
  `GET /api/admin/storage/get?id=2` (`/api/fs/list`'s `provider` is `"unknown"` on this build,
  `fsread.go:110`, and is **not** used).
- service identity `test` (non-admin; `role=0`; `permission=65341` includes write), restricted
  root `base_path=/115open/CloudSite/教程`.
- small test directory; **out-of-band** writes via the 115 official channel (never through the
  tested OpenList).

## 2. Phase A — IndexCore baseline (repeatable, same DB/root)

`TestLiveP0PhaseABaseline` (ResetSchema once, then a FULL `Service.Scan()` — not scoped —
to populate Canonical Inventory, "6 canonical" baseline):

| Path | resource_id |
| --- | --- |
| `/text.pptx` | `f1a9e79d-cefa-4cfc-b88d-708a668227dc` |
| `/test.xlsx` | `c7529852-62f4-4fa5-aefc-3950386016c2` |
| `/test.pdf` | `ec4dbb6d-79d5-4b84-9c69-ac40804e21ef` |
| `/test.docx` | `ea71d5c2-7cc8-4f60-834f-2d5b6d3911c9` |
| `/restsa.txt` | `6696f44c-cfdc-4884-acd6-fcd6085d2790` |
| `/利用cf获取证书教程.md` | `725e3faa-cae9-42bd-a5a4-2fbe015e9966` |

baseline full scan = 31 ms, `APPLIED`, generation 1.

## 3. Timeline (UTC, 2026-09-24)

| Step | Time | Observation |
| --- | --- | --- |
| T0 | `02:14:52Z` | `refresh=false` → **6 files** (= baseline) |
| T1 | — | operator uploads NEW file `resa.txt` **out-of-band** via the 115 official channel |
| T2 | `02:16:21Z` | `refresh=false` → **still 6 files; `resa.txt` NOT visible** ✅ decisive stale-cache gate |
| T3 | — | `scan.Service.ScanScope(root, "/", 100)` issues exactly **one canonical `refresh=true` observation** (no reset) |
| T4 | (within T3) | that single `refresh=true` refreshes the cache (`/api/fs/list` total=7) |
| T5 | — | reconcile `{Status: APPLIED, SnapshotLifecycle: RECONCILED, Generation: 1, AppliedGeneration: 2, Mutated: true}` ✅ |
| T6 | — | Q3 (get-by-id) + **real Q4** (`ListResources`) + Q6 (`ListActivePage`) all expose the new file ✅ |

## 4. Phase B — Incremental result (same DB/root, NO ResetSchema)

`TestLiveP0PhaseBScopedIncrement`:

- **before = 6** canonical → **after = 7**; **added = exactly `[/resa.txt]`** (only the new file);
- **all 6 baseline `resource_id`s unchanged** (`ea71d5c2…`, `ec4dbb6d…`, `c7529852…`, `f1a9e79d…`,
  `725e3faa…`, `6696f44c…` — identical before/after);
- **real Q4** `ListResources(root)` children = **7**; Q3 resolved **7/7**; Q6 active = 7;
- **no removal**: Q7 removed page empty; **zero** PRESENT rows with
  `removal_evidence_state <> 'NONE' OR missing_since IS NOT NULL OR consecutive_complete_missing <> 0`;
- no unrelated resource lost.

## 5. Measured (Round-2 evidence requirements met)

- **`T3→T5` = 3014 ms** (scoped refresh + reconcile).
- **`T3→T6` = 3020 ms** (includes the Q3/Q4/Q6/Q7 queries; correctly timed *after* the queries).
- **`/api/fs/list` (refresh=true): HTTP latency = 3311 ms, response_bytes = 2507, total = 7.**
- direct children **6 → 7**; **no 403 / 429 / throttle**; **no retries**; `page_size=200`,
  `limit_rate=1` unchanged.
- the probe issued **list/refresh observations + read-only queries only** — no provider mutation.

> Note: this run's latency (~3.0 s) is materially higher than the earlier warm run (142 ms);
> it reflects a cold cache plus the real 115 Open API round trip under the default
> `limit_rate=1`.

## 6. Verdict

The live probes established a populated Canonical Inventory, then proved that one targeted
scoped refresh **incrementally** integrated a genuine out-of-band 115 change: exactly one new
resource added, all prior resources and their ids preserved, no removal evidence, and Q3 + real
Q4 + Q6 visibility. Final P0 acceptance and any merge are **reserved for the Architect**.

## 7. Reproduce

```bash
# Phase A (baseline): ResetSchema + full Scan
INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
INDEXCORE_TEST_DATABASE_URL=postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable \
  go test ./internal/runtime/scan -run TestLiveP0PhaseABaseline -v -count=1

# [operator] out-of-band upload of a NEW file via the 115 official channel
# T0/T2: refresh=false must be unchanged (stale gate)

# Phase B (increment): NO ResetSchema
INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
INDEXCORE_P0_LIVE_SCOPE=/ INDEXCORE_P0_LIVE_EXPECT=/resa.txt \
INDEXCORE_TEST_DATABASE_URL=postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable \
  go test ./internal/runtime/scan -run TestLiveP0PhaseBScopedIncrement -v -count=1
```
