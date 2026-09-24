# P0 Targeted Scoped Refresh — Live 115 Open Result

> Status: **LIVE EXECUTED — T0–T6 OBSERVED — DECISIVE STALE-CACHE GATE PASSED**
>
> P0 overall PASS / merge decision: **reserved for the Architect**
>
> Execution issue: #62 · Parent architecture: #57
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Environment (non-production)

- OpenList **v4.2.6 / commit `2bdf16d`** at `http://127.0.0.1:5244` (container `openlist-p0`).
- storage **id=2, `driver="115 Open"`** (community-maintained driver over the **official
  115 Open API**), mount `/115open`, `status=work`, `page_size=200`, `limit_rate=1`.
  Driver judged from the authoritative `GET /api/admin/storage/get?id=2`
  (`driver="115 Open"` + official OAuth `access_token`/`refresh_token`). The
  `/api/fs/list` `provider` field is `"unknown"` on this build (`fsread.go:110`) and is
  **not** used as a driver criterion.
- service identity `test` (non-admin; `role=0`; `permission=65341` includes write),
  restricted root `base_path=/115open/CloudSite/教程`.
- small test directory; **out-of-band** write performed via the 115 official channel
  (never through the tested OpenList).

## 2. Timeline (all UTC, 2026-09-24)

| Step | Time | Observation |
| --- | --- | --- |
| T0 | `01:44:17Z` | `refresh=false` → **5 files** (old cache) |
| T1 | — | user uploads `restsa.txt` (38 B) **out-of-band** via 115 official channel |
| T2 | `01:45:45Z` | `refresh=false` → **still 5 files; `restsa.txt` NOT visible** ✅ decisive gate |
| T3 | — | `scan.Service.ScanScope(root, "/", 100)` issues exactly **one canonical `refresh=true` observation** |
| T4 | `01:46:26Z` | `refresh=false` → **6 files incl. `restsa.txt`** ✅ the single scoped refresh refreshed the cache |
| T5 | — | reconcile `{Status: APPLIED, SnapshotLifecycle: RECONCILED, AppliedGeneration: 1, Mutated: true}` ✅ |
| T6 | — | Q3 / Q4 / Q6 all expose `/restsa.txt` ✅ |

## 3. Measured

- **T6 − T3 (cold cache) = 312 ms**; warm re-run = **142 ms**.
- directory direct children: **5 → 6**; response small (KB-scale).
- no **403 / 429 / throttle**; **no retries** required.
- `page_size=200`, `limit_rate=1` unchanged.
- exactly one canonical `POST /api/fs/list refresh=true` per scope attempt (plus one
  `POST /api/auth/login` for the username/password identity).

## 4. Verdict

Live P0 scoped refresh was **observed** to synchronise a genuine out-of-band 115 change
into IndexCore, passing the decisive `T0/T1/T2` stale-cache gate and reaching Q3/Q4/Q6
visibility. The prototype did **not** delete or mutate anything on the provider; it issued
list/refresh observations only. Final P0 acceptance and any merge are **reserved for the
Architect**.

## 5. Reproduce

```bash
INDEXCORE_P0_LIVE_BASE_URL=http://127.0.0.1:5244 \
INDEXCORE_P0_LIVE_USER=test INDEXCORE_P0_LIVE_PASS=... \
INDEXCORE_P0_LIVE_SCOPE=/ INDEXCORE_P0_LIVE_EXPECT=/restsa.txt \
INDEXCORE_TEST_DATABASE_URL=postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable \
  go test ./internal/runtime/scan -run TestLiveP0ScopedRefresh -v -count=1
```

The decisive `T0/T1/T2` gate must be performed **outside** this probe, because the probe
itself issues the single canonical `refresh=true` observation.