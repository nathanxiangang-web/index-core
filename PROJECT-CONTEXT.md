# Index Core — Project Context

> Purpose: durable project context for maintainers, Architect sessions, and bounded Codex work.
> Rule: **Chat history is cache. Git repository is project memory.**

## 1. What this project is

IndexCore is an independent, reusable, provider-neutral resource indexing kernel.

It accepts external resource observations, evaluates identity/completeness/safety, maintains one Canonical Inventory, records canonical changes in an append-only Journal, and exposes read-only Query APIs.

Current status:

```text
Core development for accepted Alpha scope: COMPLETE
Gate 1–4: CLOSED / ARCHITECT_ACCEPTED
Post-MVP Incremental P0–P10: ARCHITECT_ACCEPTED
P11 Production Hybrid Runtime Hardening: ARCHITECT_ACCEPTED
P12 Deployment Soak with Reference Consumer: ARCHITECT PLAN ACTIVE
Operating mode: Stable Alpha Foundation / Incremental Hardening
Gate 5: NOT AUTHORIZED
```

IndexCore is not a CloudSite-only module and is not a full product backend.

## 2. Core flow

```text
Collector / Snapshot
        ↓
Validation + evidence
        ↓
Identity
        ↓
Completeness / safety
        ↓
Safe Reconcile
        ↓
Canonical Inventory
        ↓
Canonical Change Journal
        ↓
read-only Query / HTTP /v1
```

## 3. Current implementation

Accepted runtime stack:

- Go 1.27.x;
- PostgreSQL 18.x via pgx/v5;
- SQL-first migrations;
- rclone Collector;
- AList/OpenList Collector;
- single `indexcore` binary;
- explicit `migrate`, `doctor`, `root`, `scan`, `serve` CLI;
- read-only Q1–Q9 HTTP transport;
- single active writer daemon per database;
- restart-safe durable admissions;
- 20k real-PostgreSQL scale evidence.

PostgreSQL is the current Store implementation behind the Store Interface. Domain semantics do not depend on PostgreSQL schema/ORM.

## 4. Frozen responsibility boundaries

### Collector

Obtains external facts and emits normalized Snapshot/evidence.

Collector does not own Canonical Inventory, final canonical identity, final completeness acceptance, removal decisions, or the Canonical Change Journal.

Current runtime collectors are rclone and AList/OpenList. Both remain additive-safe by default unless a source can provide separately accepted positive completeness evidence.

### Index Kernel

Owns Canonical Inventory, identity continuity semantics, Snapshot acceptance, completeness/safety, Safe Reconcile, root/generation semantics, and Canonical Change Journal semantics.

### Store

Persists Kernel-defined state and supplies transaction, rollback, locking, CAS, and durable admission guarantees behind the Store Interface.

### Consumer

Applications read canonical truth through the Query Contract and may maintain projections. Consumers must not redefine resource truth or write IndexCore tables directly.

## 5. Current Consumer boundary

Gate 4 proved the intended integration:

```text
Browser / app
    ↓
application server / BFF
    ↓ server-side HTTP
IndexCore /v1
```

The separate reference implementation is:

`nathanxiangang-web/indexcore-reference-web`

It verified Q1–Q9 without direct PostgreSQL, IndexCore Go internals, provider dependencies, or CloudSite code.

## 6. Relationship to CloudSite

CloudSite 1.0 is **Legacy / Frozen Product**.

It may be consulted for historical requirements, UX lessons, preview/download behavior, and architecture-debt lessons, but it is not the IndexCore validation target and is not the default foundation for a future product.

Rule:

> Reference CloudSite requirements; do not inherit CloudSite architecture.

## 7. Relationship to external tools

### rclone

Supported runtime Collector. Runs as an external process and is additive-safe by default. Provider IDs/hashes are evidence, not automatically canonical identity.

### AList / OpenList

Supported runtime Collector through the HTTP API. Credentials are referenced through environment-variable names, not persisted as plaintext provider secrets.

The AList/OpenList search/index implementation is not IndexCore Canonical Inventory.

### Future providers

New providers belong behind the Collector boundary unless canonical truth/safety itself requires a Kernel change.

## 8. Execution model for future changes

P11 Production Hybrid Runtime Hardening is complete and Architect-accepted
(PR #100 merged as `ec980a5`).

P11 exit decision: `AUTHORIZE_DEPLOYMENT_SOAK`.

Deployment soak is now the P12 architecture-planning phase. The accepted Reference Web
(`nathanxiangang-web/indexcore-reference-web`) is the external read-only consumer baseline.
Implementation remains unauthorized until the P12 plan merges and bounded paired Issues are opened.

When work is authorized:

```text
ChatGPT Architect
       ↓
bounded execution Issue / plan
       ↓
Codex Executor
       ↓
branch + code/docs/tests
       ↓
Pull Request
       ↓
Architect review
       ↓
main
```

Codex must not silently create a new Gate, change frozen invariants, broaden Kernel responsibility, make a deferred product decision, or merge architecture changes without review.

## 9. Maintenance rule

IndexCore is now a stable Alpha foundation, not an invitation to keep adding features.

Before changing the Kernel, classify the requirement:

```text
IndexCore responsibility
Collector responsibility
Consumer/product responsibility
Operations/deployment responsibility
Out of scope
```

Only true IndexCore responsibility should modify the core.

## 10. Project memory hierarchy

### Current user/operator docs

- `README.md`
- `docs/README.md`
- `docs/QUICKSTART.md`
- `docs/CLI.md`
- `docs/HTTP-API.md`
- `docs/COLLECTORS.md`
- `docs/INTEGRATION.md`
- `docs/OPERATIONS.md`

### Current architecture state

1. `PROJECT-CONTEXT.md`
2. `PROJECT-STATE.md`
3. `ARCHITECTURE-INVARIANTS.md`
4. `NEXT-ACTIONS.md`
5. current approved Issue/PR, if any

### Deep evidence

- `docs/architecture/`
- `docs/decisions/`
- `docs/gate2/`
- `docs/gate3/`
- `docs/gate4/`
- `docs/research/`
- source code / tests / exact commits

## 11. Context recovery protocol

A fresh maintenance or architecture session should:

1. verify remote `main`;
2. read `PROJECT-CONTEXT.md`;
3. read `PROJECT-STATE.md`;
4. read `ARCHITECTURE-INVARIANTS.md`;
5. read `NEXT-ACTIONS.md`;
6. confirm whether a new Gate/Issue is actually authorized;
7. read only the deeper contracts/evidence needed for the requested change.

If no new Gate is authorized, do not convert a vague "continue" request into implementation work.

## 12. Evidence standard

Claims should remain precise: FACT / VERIFIED, INFERENCE, or UNKNOWN.

Provider capability vocabulary remains: DIRECT, DERIVABLE, DRIVER_DEPENDENT, UNAVAILABLE, UNKNOWN.

Do not turn one driver's behavior into a universal provider capability.

## 13. Detailed history

Project blueprint:

- `通用资源索引内核项目蓝图 v0.1.md`

Accepted research:

- `docs/research/XIAOYA-INDEX-ARCHITECTURE-REPORT.md`
- `docs/research/ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md`
- `docs/research/D03-COLLECTOR-GAP-COMPARISON.md`

Final Gate 4 consumer evidence:

- `docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`
