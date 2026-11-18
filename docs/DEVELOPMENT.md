# IndexCore Development Guide

This guide is for maintainers and contributors changing IndexCore code, tests, SQL migrations, build configuration, and repository documentation.

## 1. Project status and scope

IndexCore is a **Stable Alpha Foundation / Incremental Hardening** component.

- Gate 1–4: CLOSED / ARCHITECT_ACCEPTED.
- Incremental P0–P11: ARCHITECT_ACCEPTED.
- P12: shortened cross-repository test validation COMPLETE / ARCHITECT_ACCEPTED.
- The original P12 >=30-minute / >=100-visibility acceptance soak was NOT RUN and is NOT CLAIMED.
- The repository default for the hybrid incremental runtime is disabled.
- Gate 5 is NOT AUTHORIZED.

The purpose of maintenance is to preserve correctness, operability, and measured performance. Do not treat this status as permission to add product features or expand the Kernel.

## 2. Requirements

| Dependency | Required baseline |
| --- | --- |
| Go | 1.27.1 (see go.mod) |
| Database | PostgreSQL 18 |
| PostgreSQL driver | pgx/v5 |
| SQL schema | explicit SQL-first migrations |
| Provider tools | rclone on PATH when using the rclone Collector |
| Local test container | Docker, for the Makefile PostgreSQL 18 helper |

The CI workflow is authoritative for the required checks. If this guide and .github/workflows/ci.yml differ, update the guide or follow the current workflow; do not silently weaken CI.

## 3. Repository map

| Path | Responsibility |
| --- | --- |
| cmd/indexcore | binary entry point and CLI composition |
| internal/domain | provider-neutral domain types and semantics |
| internal/kernel | canonical decision and reconcile behavior |
| internal/collector | source adapters and normalized observations |
| internal/store | persistence behind the Store boundary |
| internal/query | read-side query behavior |
| internal/transport | HTTP transport and wire contract |
| internal/runtime | process composition, workers, scan and incremental orchestration |
| internal/sourcebudget | bounded source HTTP pacing/cooldown policy |
| internal/diagnostic | bounded operator diagnostics |
| internal/health | liveness/readiness support |
| testutil | test fixtures and owned test helpers |
| scripts/soak | verification-only P12 fixtures and orchestration |
| docs/architecture | frozen architecture contracts and phase plans |
| docs/decisions | accepted decisions and explicitly marked proposals |
| docs/p0, docs/incremental | issue-specific evidence and result records |

Keep the dependency direction narrow. A new package must have a clear owner and a reason to exist; avoid introducing generic frameworks for one call site.

## 4. Architecture boundaries

The canonical flow is:

~~~text
External source
    -> Collector
    -> normalized Snapshot + evidence
    -> Kernel validation / identity / completeness / Safe Reconcile
    -> Canonical Inventory + append-only Journal
    -> PostgreSQL Store
    -> read-only Q1–Q9 HTTP Query
    -> application server / BFF
~~~

Preserve the following:

1. Collector reports facts; it does not write Canonical rows or decide deletion.
2. Kernel owns canonical identity, completeness, safety, generation, reconcile, and Journal semantics.
3. Store owns transactions, persistence, locking, CAS, and durable admission guarantees behind the Store Interface.
4. Query/HTTP is read-only. Administrative writes remain CLI/runtime operations.
5. Consumers never access IndexCore PostgreSQL or import IndexCore internal Go packages.
6. One active write-orchestration process per database. Never create a second writer to increase throughput.
7. Scoped AList/OpenList refresh is PARTIAL and additive-safe. Missing children are not deletion evidence.
8. Preserve per-root FIFO admission, original observation timestamps, generation lineage, topology/parent checks, and all accepted identity/removal fences.

Before changing code, classify the requested responsibility as Collector, Kernel, Store, Query/Transport, Consumer, Operations, or Out of Scope.

## 5. Local development setup

### 5.1 Verify the toolchain

~~~bash
go version
docker version
git status --short
git remote -v
~~~

Use the Go version declared in go.mod.

### 5.2 Start a disposable PostgreSQL 18 test database

The Makefile helper uses container name indexcore-pg and host port 55432 by default. It removes an existing container with that name before starting a new one. Use it only when that container is known to be disposable.

~~~bash
make pg-up
~~~

Default test DSN:

~~~text
postgres://indexcore:indexcore@localhost:55432/indexcore?sslmode=disable
~~~

The database is for local tests only. Never override TEST_DSN or INDEXCORE_TEST_DATABASE_URL with a production connection string.

### 5.3 Build and test

~~~bash
gofmt -w <changed-go-files>
go vet ./...
go build ./...
make test
~~~

make test runs the complete package suite with go test -p 1 ./... against PostgreSQL 18. The -p 1 setting is intentional: DB-backed packages may reset their test schema and must not race each other at the package level.

When finished, stop only the disposable helper container:

~~~bash
make pg-down
~~~

make pg-down removes the helper container. Confirm the container name and ownership before using it.

### 5.4 Race and focused tests

Run the focused tests for the changed package first. For changes involving the hybrid runtime or app lifecycle, CI currently includes:

~~~bash
go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
~~~

The CI workflow also runs issue-specific PostgreSQL 18 adversarial/race suites for the P0 reliability and performance paths. Do not omit a relevant suite because the change appears small.

### 5.5 Scale tests

The accepted scale harness is invoked through:

~~~bash
make scale
~~~

The default fixture size is 20,000. Scale tests are resource-intensive and must use an owned disposable PostgreSQL 18 database. Run them only when the authorized task requires scale evidence. A pre-seeded database population is not proof that every source item was traversed.

## 6. SQL migrations and persistence

- Migrations are explicit and SQL-first.
- indexcore serve validates schema compatibility but does not auto-migrate.
- Do not edit an already-merged migration to change history. Add a new forward migration when a schema change is authorized.
- A schema change requires an Issue, an architecture decision where semantics or compatibility change, tests for old/new state, upgrade/rollback analysis, and an explicit release plan.
- Test migration behavior against disposable PostgreSQL 18, including restart and failure paths where relevant.
- Never use production SQL as a substitute for an application repair path. No ad hoc updates to Canonical, Journal, Snapshot, Work, admission, or Root tables.

## 7. Source requests and concurrency

Source reads may cause real upstream traffic. In the current production path, OpenList refresh=true may reach 115 Open; do not assume it is merely a local cache read.

Current code defaults:

- INDEXCORE_INCREMENTAL_READ_WORKERS=1 (serial).
- Values 2/4/8 are explicit bounded opt-ins.
- Global, Root, and provider-group source spacing defaults to 100ms each.
- HTTP 429 handling respects Retry-After and bounded local backoff.
- Source-budget grouping is process-local; it is not proof of the upstream account's shared quota.

Do not increase concurrency or shorten source intervals on production based on mock tests or a benchmark alone. Require measured source request starts, 429/5xx, actual source grouping, fairness, capacity/freshness debt, rollback-to-serial proof, and separate operator authorization. F1 first-discovery and F2 mirror-revisit remain default-off.

## 8. Test design standards

Tests should prove behavior, not merely exercise code paths.

- Prefer deterministic fixtures and barriers for concurrency/interleaving tests.
- Use owned PostgreSQL 18 for transaction, locking, CAS, and crash-recovery assertions.
- Include negative cases: missing, duplicate, stale, malformed, out-of-order, cancelled, and conflicting evidence.
- Preserve the original failing regression as a red-before/green-after proof when fixing a defect.
- Use race tests for shared state and lifecycle changes.
- Assert Canonical and Journal invariants, not only return values.
- For provider-facing work, count actual HTTP calls, retries, request rate, and status classes; do not infer calls from completed Work rows.
- Disclose skipped tests, inherited skips, timeouts, fixture limits, and tests not run.
- Do not weaken tests or assertions to make CI green.

## 9. Branch, commit, and PR workflow

1. Fetch and record the exact current main SHA.
2. Confirm the task Issue and acceptance criteria are authorized.
3. Create one task-specific branch from that SHA.
4. Keep the change bounded to one concern. Do not mix unrelated Issues or draft PRs.
5. Add tests and documentation with the code change.
6. Run relevant local checks, then wait for exact-head CI.
7. Submit one PR with the repository PR template completed.
8. Wait for independent Architect review. The Executor does not self-approve or self-merge.
9. After merge, verify merged-main CI and update PROJECT-STATE.md / NEXT-ACTIONS.md if the accepted state changed.

Use clear commit prefixes such as fix:, feat:, test:, docs:, refactor:, and perf:. Do not force-push or rewrite shared history unless the Owner explicitly authorizes it.

## 10. Definition of Done

A code task is DONE only when all applicable items are true:

- [ ] The change matches the authorized Issue and responsibility boundary.
- [ ] No frozen invariant or public contract changed unintentionally.
- [ ] Relevant unit, integration, PostgreSQL 18, and race tests pass.
- [ ] gofmt, go vet ./..., and go build ./... pass.
- [ ] Exact-head GitHub CI is completed/success on the submitted SHA.
- [ ] Documentation and ADRs reflect the accepted behavior.
- [ ] Production/provider side effects and limitations are stated honestly.
- [ ] Independent Architect review accepts the exact head.
- [ ] Merge and post-merge CI are verified before marking DONE.

For AI execution and handoff rules, see AI-DEVELOPMENT-PROTOCOL.md. For production and operator safety, see OPERATING-STANDARDS.md and OPERATIONS.md.