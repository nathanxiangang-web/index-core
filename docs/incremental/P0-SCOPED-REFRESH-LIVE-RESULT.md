# P0 Targeted Scoped Refresh — Live 115 Open Result (Round 5: proxy-instrumented)

> Status: **LIVE EXECUTED — canonical request captured by a test-only transparent proxy**
> — **increment, real Q4, exact T3→T6, canonical latency/bytes, count == 1**
>
> P0 overall PASS / merge decision: **reserved for the Architect**
>
> Execution issue: #62 · Parent architecture: #57
>
> `FROZEN_CONTRACT_CHANGES: NONE`

> This result supersedes the earlier latency figure: the prior `3311 ms / 2507 B` was an
> **extra** `refresh=true` probe, not the canonical observation. The metrics below are captured
> by a **test-only transparent proxy** placed between IndexCore and the real OpenList, so they
> belong to the **one** canonical `POST /api/fs/list refresh=true` that `ScanScope` issues.

## 1. Environment (non-production)

- OpenList **v4.2.6 / commit `2bdf16d`** at `http://127.0.0.1:5244` (container `openlist-p0`).
- storage **id=2, `driver="115 Open"`** (community driver over the official 115 Open API),
  mount `/115open`, `status=work`, `page_size=200`, `limit_rate=1`, `cache_expiration=30` min.
  Driver judged from authoritative `GET /api/admin/storage/get?id=2`
  (`/api/fs/list`'s `provider` is `"unknown"` on this build, `fsread.go:110`, and is not used).
- service identity `test` (non-admin; `role=0`; `permission=65341` includes write), restricted
  root `base_path=/115open/CloudSite/教程`.
- small test directory; **out-of-band** writes via the 115 official channel (never through the
  tested OpenList).

## 2. Phase A — IndexCore baseline (same DB/root)

`TestLiveP0PhaseABaseline`: `ResetSchema` once, then a FULL `Service.Scan()` (not scoped) to
populate Canonical Inventory — **7 canonical** resources, baseline scan 27 ms / `APPLIED` /
generation 1:

| Path | resource_id |
| --- | --- |
| `/text.pptx` | `d6a22075-e17d-4904-ad74-09a25aff6779` |
| `/test.xlsx` | `b4443c28-a28c-4c3d-bb15-f6a7e33ae053` |
| `/test.pdf` | `46271f87-b21f-4ae4-9460-96e301fb2e15` |
| `/test.docx` | `90b06c52-09ea-47da-bc6d-c69deba7f7ec` |
| `/restsa.txt` | `1376e8a3-ce21-42a3-a4b6-cd69579ee015` |
| `/resa.txt` | `1d0425bb-a157-4798-94c9-18638a6582d0` |
| `/利用cf获取证书教程.md` | `4f5db015-e34b-4f32-8b82-4c2b39ab755b` |

## 3. Timeline (UTC, 2026-09-24)

| Step | Time | Observation |
| --- | --- | --- |
| T0 | `02:20:44Z` | `refresh=false` → **7 files** (= baseline) |
| T1 | — | operator uploads NEW file `r3esa.txt` **out-of-band** via the 115 official channel |
| T2 | `02:21:40Z` | `refresh=false` → **still 7 files; `r3esa.txt` NOT visible** ✅ decisive stale-cache gate |
| T3 | — | `scan.Service.ScanScope(root, "/", 100)` through the proxy: exactly **one** canonical `refresh=true` (no reset) |
| T4 | (within T3) | that single `refresh=true` refreshes the cache (canonical response total = 8) |
| T5 | — | reconcile `{Status: APPLIED, SnapshotLifecycle: RECONCILED, Generation: 1, AppliedGeneration: 2, Mutated: true}` ✅ |
| T6 | — | Q3 + **real Q4** + Q6 + Q7 read; all expose the new file with the SAME id ✅ |

## 4. Phase B — Incremental result (same DB/root, no ResetSchema)

`TestLiveP0PhaseBScopedIncrement`:

- **before = 7** canonical → **after = 8**; **added = exactly `[/r3esa.txt]`**;
- **all 7 baseline `resource_id`s unchanged** (`d6a22075…`, `b4443c28…`, `46271f87…`, `90b06c52…`,
  `1376e8a3…`, `1d0425bb…`, `4f5db015…` — identical before/after);
- **real Q4** `ListResources(root)` children = **8**, and it **contains `/r3esa.txt`**;
- **Q3 / Q4 / Q6 agree on the same id** `4f84fb67-37ac-4924-a7f6-97476b4c59f1`;
- **no removal**: Q7 empty; **zero** PRESENT rows with
  `removal_evidence_state <> 'NONE' OR missing_since IS NOT NULL OR consecutive_complete_missing <> 0`;
- no unrelated resource lost.

## 5. Captured metrics (test-only transparent proxy)

- **canonical `refresh=true` count = 1** (proves exactly one canonical observation per attempt).
- **canonical `/api/fs/list refresh=true`: HTTP status = 200, latency = 1197 ms,
  response_bytes = 2839, total = 8.**
- **`T3→T5` = 1221 ms** (scoped refresh + reconcile).
- **`T3→T6` = 1228 ms** (exact window `t6.Sub(t3)`, includes Q3/Q4/Q6/Q7).
- consistency: canonical latency (1197 ms) **<** `T3→T5` (1221 ms) — the single request is
  contained within the whole operation, as it must be.
- no 403 / 429 / throttle; **no retries**; `page_size=200`, `limit_rate=1` unchanged.
- observations + read-only queries only — **no provider mutation**.

## 6. Verdict

Proven on real 115 Open + OpenList: an already-populated Canonical Inventory was incremented by
**one** targeted scoped refresh, integrating a genuine out-of-band 115 change — exactly one new
resource added, all prior resource ids preserved, no removal evidence, Q3 + real Q4 + Q6 in
agreement. Final P0 acceptance and any merge are **reserved for the Architect**.

## 7. Reproduce

```bash
# Phase A (baseline): ResetSchema + full Scan
INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
INDEXCORE_TEST_DATABASE_URL=postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable \
  go test ./internal/runtime/scan -run TestLiveP0PhaseABaseline -v -count=1

# [operator] out-of-band upload of a NEW file via the 115 official channel
# T0/T2: refresh=false must be unchanged (stale gate)

# Phase B (increment, through the recording proxy): NO ResetSchema
INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
INDEXCORE_P0_LIVE_SCOPE=/ INDEXCORE_P0_LIVE_EXPECT=/r3esa.txt \
INDEXCORE_TEST_DATABASE_URL=postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable \
  go test ./internal/runtime/scan -run TestLiveP0PhaseBScopedIncrement -v -count=1
```
