# Gate 3 — Standalone Alpha Verification Report

> Execution: Issue #47. Branch `alpha/gate3-runtime` (baseline `main@e3fa7ab`).
> Stack: **Go 1.27.1 + PostgreSQL 18.6 + pgx/v5 + rclone v1.75.1**.
> Rule: PASS is claimed only where tests actually exercise the behavior.

## Status template (Issue #47) — AUTHORITATIVE

> This is the single authoritative status block for this report. Per-round rework
> history is appended below for traceability; it does not supersede this block.

```
STATUS: ARCHITECT_ACCEPTED — GATE 3 MVP ALPHA — READY_TO_MERGE
FINAL VERIFICATION HEAD: 055402da83235e6dc5f88f45206378fb210a7672 (PR #49, Round 5)

RUNTIME: PASS
CONFIG_STARTUP: PASS
ROOT_ADMIN: PASS
WORKER_RECOVERY: PASS
RCLONE_SCAN_PATH: PASS
ALIST_OPENLIST_REAL_SOURCE: PASS
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
| P4 | worker/recovery: root-scoped recovery for one-shot scan, FIFO head, resume same admission_seq, bounded concurrency, backoff, graceful stop that holds the writer lock until the worker stops | IMPLEMENTED + TESTED | `internal/runtime/worker` (worker_test), `AdmitOrResumeSnapshot`/`ProcessHead`, `ResolveUnadmittedSubmittedForRoot` |
| P5 | rclone scan: persist DRAFT (no root lock) → short SUBMITTED + admission → Coordinator; additive-safe; PARTIAL is not a source failure | IMPLEMENTED + TESTED | `internal/runtime/scan` (scan_test, real rclone), `CreateDraftSnapshot`/`SubmitAndAdmitSnapshot` |
| P6 | read-only HTTP /v1 (Q1–Q9), generation cursors, stable errors, no mutation | IMPLEMENTED + TESTED | `internal/transport/httpapi` (v1_test) |
| P7 | `log/slog` structured fields; no secrets | IMPLEMENTED + TESTED | scan/transport logging (CLI smoke output) |
| P8 | ≥20k resources on real PostgreSQL; reproducible baseline timed over the real runtime ingestion path (DRAFT persistence → Stage-1 admission → Coordinator) | IMPLEMENTED + TESTED | `internal/runtime/scale` (gated harness, Round 5) |
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

> Round 5: the harness drives the **real runtime ingestion path** —
> `CreateDraftSnapshot -> SubmitAndAdmitSnapshot -> Coordinator.ProcessHead` —
> and the timer starts **before the DRAFT is persisted**. Earlier numbers (which
> started timing only after the Snapshot was already persisted) were
> reconcile-only and are superseded by this full-ingestion baseline.

```
initial population: 16.29 s (draft=3.63s admit=2.6ms reconcile=12.65s)  (20000 ADD)
identical repeat  : 12.91 s (NOOP;  draft=3.79s admit=1.7ms reconcile=9.12s)
small delta       : 17.00 s (APPLIED; draft=3.69s admit=1.5ms reconcile=13.31s)
query pagination  :  0.143 s (20001 rows, 1000/page)
canonical PRESENT = 20001   journal_events = 20002   snapshots = 3
db_size = 71.1 MiB          heap_alloc = 47.3 MiB
```

- **Stage-1 split has no ingestion cost:** the short `SubmitAndAdmitSnapshot`
  transaction (lock root -> DRAFT->SUBMITTED -> `admission_seq` -> PENDING) stays
  **~1.5–2.6 ms at 20k entries** — it is O(1) and never scales with entry count.
  The DRAFT persistence (writing 20k entries, no root lock) is ~3.6–3.8 s and the
  reconcile is ~9–13 s.
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
---

# Round 2 rework (PR #49 Round-1 review, G3-R1..G3-R11)

| Item | Fix | Where |
|------|-----|-------|
| **G3-R1** | Real AList/OpenList Collector adapter over the AList HTTP API (BFS, login, hash normalization, additive-safe). Verified against a **real `xhofe/alist` instance** and end-to-end to HTTP /v1. | `internal/collector/alist`, `internal/runtime/scan`, `internal/runtime/e2e/alist_http_test.go` |
| **G3-R2** | HTTP transport no longer receives `*pgxpool.Pool`; `Deps` now holds only the read-only `query.Reader` and a narrow `health.Probe` (`Ping`/`SchemaStatus`). API-level test fails if a pool/store field returns. | `internal/health`, `internal/transport/httpapi`, `boundary_test.go` |
| **G3-R3** | Crash window closed: worker recovery sweep admits SUBMITTED-but-unadmitted Snapshots; `scan` drains an existing durable PENDING head before creating new work and retries after `ErrNotHead`. | `postgres/recovery.go`, `worker.go`, `scan.go` |
| **G3-R4** | Worker keeps an in-flight root set; a root already active is skipped and the loop continues (never `break`s), so a slow root cannot starve other roots. | `worker.go` |
| **G3-R5** | `serve` acquires a PostgreSQL advisory lock held for the process lifetime; a second daemon fails closed (`ErrWriterLockHeld`). | `postgres/writer_lock.go`, `app.go` |
| **G3-R6** | `INDEXCORE_RCLONE_CONFIG` / `--rclone-config` is passed to rclone as `--config <path>`; proven by a named-remote process-boundary test. | `rclone/adapter.go`, `config_test.go` |
| **G3-R7** | Readiness/`doctor`/`serve` reject **unexpected/future** migrations as well as missing ones. | `postgres/schema.go`, `httpapi`, `app.go` |
| **G3-R8** | Compose uses a one-shot `migrate` service with `service_completed_successfully` before `serve`; `serve` still never auto-migrates. | `docker-compose.yml`, `ALPHA-DEPLOYMENT.md` |
| **G3-R9** | End-to-end Alpha scenario exercised through the **real HTTP transport** (not direct Store/QueryReader calls) plus restart. | `internal/runtime/e2e/alpha_http_test.go` |
| **G3-R10** | Added the required regressions: scan interruption before SUBMITTED, failure after Snapshot creation, and **HTTP** stale-cursor mapping (409). | `scan/recovery_test.go`, `httpapi/stale_cursor_test.go` |
| **G3-R11** | This report/PR status updated honestly. | this file / PR body |

## Real AList/OpenList evidence (G3-R1)

A real AList instance was run locally (`xhofe/alist`, a `Local` storage mounted at
`/loc`), and both the adapter and the full runtime path were exercised against it:

```
INDEXCORE_ALIST_URL=http://127.0.0.1:5245 INDEXCORE_ALIST_USER=admin \
INDEXCORE_ALIST_PASS=... INDEXCORE_ALIST_PATH=/loc \
  go test ./internal/collector/alist -run TestRealAListInstance -v        # 3 entries, SUCCESS
INDEXCORE_ALIST_URL=... go test ./internal/runtime/e2e -run TestAlphaRuntimeWithRealAListSource -v
  # real AList source produced 3 HTTP-visible resources; repeat scan NOOP (additive-safe)
```

AList/OpenList are Collector Adapters only: the Kernel is never coupled to AList
DB internals, no upstream source is copied, and skip evidence stays UNKNOWN
(additive-safe) until a positive no-skip mode is separately evidenced.

## Round 2 test totals

Real PostgreSQL 18.6 + real rclone v1.75.1 + real AList instance; `go vet` /
`gofmt` clean; full Gate-2 regression green.
---

# Round 3 rework (PR #49 Round-2 review, G3-R2.1..G3-R2.8)

| Item | Fix | Where |
|------|-----|-------|
| **G3-R2.1** | `CreateSubmittedSnapshotAndAdmit` performs `DRAFT -> SUBMITTED + allocate admission_seq + INSERT PENDING` in ONE short per-root transaction, so admission order is authoritative and no ambiguous unadmitted-SUBMITTED state is created. Recovery of legacy/fault-stranded Snapshots uses `ResolveUnadmittedSubmitted`: exactly one candidate → admit; multiple ambiguous candidates for a root → **fail closed** (never ordered by DB `created_at`). | `snapshot_create_admit.go`, `recovery.go`, `scan.go`, `worker.go` |
| **G3-R2.2** | One-shot `scan --root` now resolves stranded SUBMITTED work for its root (and fails closed on ambiguity) and drains the existing PENDING head BEFORE collecting new work, so it recovers without a running daemon. | `scan.go` |
| **G3-R2.3** | Collector source/process failure is persisted and Kernel-REJECTED, and `scan.Service.Scan` now returns `ErrSourceFailed` so the CLI exits non-zero. PARTIAL/SUSPICIOUS additive-safe successes are not treated as failures. | `scan.go` |
| **G3-R2.4** | `serve` joins the worker goroutine (bounded by `shutdown-timeout`) BEFORE the writer lock is released; forced shutdown is logged clearly. | `app.go` |
| **G3-R2.5** | Added the exact regressions: interruption **before SUBMITTED** (DRAFT never executable) and failure **after Snapshot creation** (REJECTED audit trail, no canonical). | `scan_failure_test.go` |
| **G3-R2.6** | Clean-volume Compose smoke is automated and was actually run: `down -v -> up --build -> migrate exit 0 -> /readyz 200 -> restart -> ready again`. | `scripts/compose-smoke.sh`, `docker-compose.yml`, `make compose-smoke` |
| **G3-R2.7** | AList adapter paginates explicitly (page/per_page) until all `total` entries are collected, and fails the scan if `total != collected` (truncation). Skip evidence remains UNKNOWN. | `internal/collector/alist`, `pagination_test.go` |
| **G3-R2.8** | AList credentials are persisted only as environment-variable REFERENCES (`username_env` / `password_env` / `token_env`) and resolved at runtime; plaintext secrets are never written into `index_root_adapter_config`. | `scan.go`, `alist_http_test.go` |

## Compose clean-volume evidence (G3-R2.6)

`make compose-smoke` (`scripts/compose-smoke.sh`) output:

```
migrate exit code: 0
== GET /readyz -> {"schema_applied":4,"status":"ready"} ==
== GET /readyz after restart -> {"schema_applied":4,"status":"ready"} ==
COMPOSE SMOKE OK
```

(Note: postgres:18 requires the data volume mounted at `/var/lib/postgresql`; the
Compose file was corrected accordingly during this smoke run.)

# Round 4 rework (PR #49 Round-3 review: runtime transaction boundary + final safety semantics)

| Item | Fix | Where |
|------|-----|-------|
| **R4-1 Stage-1 split** | Stage-1 is split into (a) `CreateDraftSnapshot`: persist DRAFT + entries WITHOUT the per-root `FOR UPDATE` lock, and (b) `SubmitAndAdmitSnapshot`: a SHORT transaction that moves DRAFT -> SUBMITTED, allocates `admission_seq` and INSERTs PENDING under the root lock. A 20k-entry scan no longer holds the root lock while writing entries; a crash between the two leaves only an inert DRAFT. | `snapshot_create_admit.go`, `scan.go`, `recovery_order_test.go` |
| **R4-2 root-scoped recovery** | One-shot `scan --root A` now uses `ResolveUnadmittedSubmittedForRoot`, so it never admits stranded work for roots B/C. The daemon worker keeps the whole-database sweep. | `recovery.go`, `scan.go`, `recovery_order_test.go` |
| **R4-3 shutdown lock hold** | On graceful-shutdown timeout the writer lock is NOT released early: `runServe` keeps holding it and keeps waiting until the worker goroutine actually stops. The worker is also cancelled on the HTTP-early-exit path. | `app.go`, `shutdown_test.go` |
| **R4-4 PARTIAL semantics** | `TraversalStatus=PARTIAL` is a legal additive-safe input and is no longer classified as a source failure; only `FAILED`/`INTERRUPTED` cause a non-zero CLI exit. PARTIAL Snapshots are Kernel-evaluated (`acceptance_state=PARTIAL`) and are never rejected nor used to remove resources. | `domain/enums.go`, `scan.go`, `enums_test.go`, `scan_failure_test.go` |
| **R4-5 secret write boundary** | `UpsertAdapterConfig` (the `root adapter set --config` write boundary) rejects plaintext `password`/`token`/`secret`/`credential`/`api_key` fields; only environment references (`*_env`) are accepted. | `adapter_config_validate.go`, `root_admin.go`, `adapter_config_validate_test.go` |
| **R4-6 real fault regressions** | The two fault regressions now drive the real runtime persistence API instead of hand-built DB rows: `CreateDraftSnapshot` -> crash before finalize (DRAFT is inert, no admission), and `CreateDraftSnapshot` + `SubmitAndAdmitSnapshot` -> crash before Stage-2 (durable PENDING resumes with the SAME `admission_seq`). | `scan_failure_test.go` |

## Round 4 test evidence

Real PostgreSQL 18.6 + real rclone v1.75.1 + real `xhofe/alist` instance (3 entries,
3 HTTP-visible resources); full suite green; `go vet` / `gofmt` clean; no frozen
Gate 1B/1C semantics changed.

# Round 5 rework (PR #49 Round-4 review: final closeout)

| Item | Fix | Where |
|------|-----|-------|
| **R5-1 remove backdoor** | Deleted `CreateSubmittedSnapshot`, which could write a SUBMITTED Snapshot with no admission and thereby bypass the safe Stage-1 path. The only Snapshot-creation entry point is now `CreateDraftSnapshot` (+ `SubmitAndAdmitSnapshot`). | `snapshot_create.go` (removed) |
| **R5-2 force DRAFT** | `CreateDraftSnapshot` now REJECTS any caller-supplied `lifecycle_state` other than DRAFT (returns `ErrNotDraft` and persists nothing), so it can never smuggle a SUBMITTED Snapshot past admission. | `snapshot_create_admit.go`, `recovery_order_test.go` |
| **R5-3 real 20k ingestion baseline** | The scale harness now drives `CreateDraftSnapshot -> SubmitAndAdmitSnapshot -> Coordinator.ProcessHead` and times from BEFORE the DRAFT is persisted (full runtime ingestion, not reconcile-only). New N=20000 baseline above; the short Stage-1 admission stays **~1.5–2.6 ms (O(1))**, showing the split did not regress ingestion. | `internal/runtime/scale/scale_test.go` |
| **R5-4 rejection ≠ source failure** | A Kernel policy rejection of a SUCCESSFUL observation now returns the new `ErrReconcileRejected`, kept distinct from `ErrSourceFailed` (only FAILED/INTERRUPTED traversal). The outcome classification is a testable pure function. | `scan.go`, `outcome_internal_test.go` |

## Round 5 test evidence

Real PostgreSQL 18.6 + real rclone v1.75.1 + real `xhofe/alist` instance; full suite
green; 20k harness re-run over the real runtime ingestion path (`draft/admit/reconcile`
breakdown); `go vet` / `gofmt` clean; no frozen Gate 1B/1C semantics changed.

