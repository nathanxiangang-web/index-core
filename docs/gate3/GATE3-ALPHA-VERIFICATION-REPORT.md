# Gate 3 — Standalone Alpha Verification Report

> Execution: Issue #47. Branch `alpha/gate3-runtime` (baseline `main@e3fa7ab`).
> Stack: **Go 1.27.1 + PostgreSQL 18.6 + pgx/v5 + rclone v1.75.1**.
> Rule: PASS is claimed only where tests actually exercise the behavior.

## Status template (Issue #47)

```
STATUS: READY_FOR_ARCH_REVIEW — GATE 3 STANDALONE ALPHA

RUNTIME: PASS
CONFIG_STARTUP: PASS
ROOT_ADMIN: PASS
WORKER_RECOVERY: PASS
RCLONE_SCAN_PATH: PASS
QUERY_HTTP_V1: PASS
OBSERVABILITY: PASS
SCALE_20K: PASS
PACKAGING: PASS
E2E_ALPHA: PASS
GATE2_REGRESSION: PASS

FROZEN_CONTRACT_CHANGES: NONE
CLOUDSITE_INTEGRATION: NONE
UI: NONE
SCANNER_RESUME: NONE
TRUE_INCREMENTAL: NONE
DESTRUCTIVE_PROVIDER_COMPLETE: NONE
MULTI_DAEMON_HA: NONE
```

## P0–P11 requirement status

| Phase | Requirement | Status | Evidence |
|-------|-------------|--------|----------|
| P0 | `cmd/indexcore`, version/build info, stdlib dispatch, package boundaries | IMPLEMENTED + TESTED | `cmd/indexcore/main.go`, `internal/runtime/{version,config,app}`, `internal/transport/httpapi`; CLI smoke |
| P1 | env+flags config, fail-fast, loopback default | IMPLEMENTED + TESTED | `internal/runtime/config` (config_test) |
| P2 | explicit `migrate`; `serve` verifies schema (no auto-migrate); `/healthz` `/readyz` | IMPLEMENTED + TESTED | `postgres.SchemaStatus`, `httpapi` (server_test); CLI smoke |
| P3 | root create/list/config/lifecycle via frozen generation+journal | IMPLEMENTED + TESTED | `root_admin.go` (root_admin_test), `indexcore root ...` |
| P4 | worker/recovery: FIFO head, resume same admission_seq, bounded concurrency, backoff, graceful stop | IMPLEMENTED + TESTED | `internal/runtime/worker` (worker_test), `AdmitOrResumeSnapshot`/`ProcessHead` |
| P5 | rclone scan: DRAFT→SUBMITTED→Coordinator; additive-safe | IMPLEMENTED + TESTED | `internal/runtime/scan` (scan_test, real rclone) |
| P6 | read-only HTTP /v1 (Q1–Q9), generation cursors, stable errors, no mutation | IMPLEMENTED + TESTED | `internal/transport/httpapi` (v1_test) |
| P7 | `log/slog` structured fields; no secrets | IMPLEMENTED + TESTED | scan/transport logging (CLI smoke output) |
| P8 | ≥20k resources on real PostgreSQL, reproducible baseline | IMPLEMENTED + TESTED | `internal/runtime/scale` (gated harness) |
| P9 | Dockerfile + compose (PostgreSQL 18), restart/persistent volume documented | IMPLEMENTED + TESTED (build) | `Dockerfile`, `docker-compose.yml`, `docs/gate3/ALPHA-DEPLOYMENT.md`, `make docker-build` |
| P10 | end-to-end Alpha scenario (11 steps) | IMPLEMENTED + TESTED | `internal/runtime/e2e` (TestAlphaEndToEnd) |
| P11 | this report | DONE | this file |

## Exact commands

```bash
make pg-up                       # PostgreSQL 18 on localhost:55432
make test                        # full suite (real PostgreSQL, -p 1)
make scale                       # 20k scale harness (SCALE_N=<n> to override)
make bin && ./bin/indexcore version
make docker-build                # indexcore:alpha (bundles rclone 1.75.1)
make compose-up                  # postgres:18 + indexcore
```

## 20k scale baseline (real PostgreSQL 18.6, N=20000)

Command: `make scale` (i.e. `INDEXCORE_SCALE_TEST=1 INDEXCORE_SCALE_N=20000 go test ./internal/runtime/scale -v`)

```
initial population: 12.81 s   (20000 ADD)
identical repeat  :  9.01 s   (NOOP)
small delta       : 13.31 s   (1 update + 1 add)
query pagination  :  0.257 s  (20001 rows, 1000/page)
canonical PRESENT = 20001     journal_events = 20002   snapshots = 3
db_size = 64.3 MiB            heap_alloc = 1.6 MiB
```

- No OOM; correctness held (canonical count and full pagination verified).
- No arbitrary latency SLO is frozen; this is a reproducible baseline.
- O(N²) hot paths found and fixed: prior identity resolution was indexed
  (`PriorIndex`) and journal sequence assignment now increments in memory instead
  of calling `MAX()` per event. 100k is exploratory, not a Gate-3 threshold.

## P10 end-to-end scenario coverage

`TestAlphaEndToEnd` (real rclone local backend) proves: migrate from empty;
create+configure root; scan; canonical population; `/v1` query incl. nested;
identical repeat NOOP; additive delta; durable PENDING resumes the same
`admission_seq` after restart; journal ordered/readable; rclone UNKNOWN skips stay
non-destructive (no `REMOVED`).

## CANDIDATE implementation choices (chosen for PoC ≠ newly frozen architecture)

- adapter config table `index_root_adapter_config` (provider-neutral binding);
- rclone bundled in the image at a pinned version, run as an external process;
- worker poll/backoff intervals (defaults 2s, configurable);
- scale harness gated by `INDEXCORE_SCALE_TEST`;
- `PriorIndex` in-memory indexing for identity resolution.

## Deferred / not supported (per Issue #47 non-goals)

CloudSite integration, UI/auth, Scanner Resume, provider-native delta / true
incremental, destructive-safe provider COMPLETE, multi-daemon HA, Redis/Kafka/MQ,
Search/Catalog/media/AI, downloader/115, Kubernetes. **No frozen Gate 1B/1C
semantics were changed.**