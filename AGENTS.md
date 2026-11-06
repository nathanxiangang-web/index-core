# AGENTS — IndexCore Operating Contract

This file is the repository-wide instruction entry point for human developers and AI coding agents. Follow it before changing code, tests, migrations, configuration, or project documentation.

## 1. Mandatory context recovery

At the start of every task:

1. Verify the current remote main SHA and the task's target branch.
2. Read PROJECT-CONTEXT.md for product purpose and frozen boundaries.
3. Read PROJECT-STATE.md for the latest recorded state; re-check referenced GitHub Issues, PRs, and CI because the file is a dated snapshot, not a live monitor.
4. Read NEXT-ACTIONS.md and identify the exact authorized task.
5. Read ARCHITECTURE-INVARIANTS.md.
6. Read docs/DEVELOPMENT.md and docs/AI-DEVELOPMENT-PROTOCOL.md.
7. Read only the module contracts, ADRs, tests, and source files required by the task. Do not load the entire repository by default.

If repository evidence conflicts with chat history, use the current Git state and accepted evidence. Chat history is cache; Git is project memory.

## 2. Roles and execution model

The current working model is:

- **Owner:** the user. Sets product priorities and authorizes high-risk or scope-expanding work.
- **Architect:** ChatGPT or the explicitly assigned architecture reviewer. Defines boundaries, acceptance criteria, and independent review decisions.
- **Executor:** one Codex execution context (or one explicitly assigned developer) implements one bounded task.

Do not assume multiple workers, parallel subagents, or multiple people are available. Do not split one task into parallel code writers unless the Owner explicitly changes this rule and the shared contracts/workspaces are proven safe.

The Executor must not self-approve architecture, self-accept its own PR, or merge its own work.

## 3. Frozen product and architecture boundaries

IndexCore is a provider-neutral canonical resource indexing kernel, not a product backend.

Preserve these rules:

- Collectors acquire and normalize external facts; they do not decide canonical truth.
- The Kernel owns identity, completeness, safety, reconcile, Canonical Inventory, and Journal semantics.
- Store persists Kernel-defined state behind the Store Interface.
- Consumers use the read-only Q1–Q9 HTTP contract. They do not read or write IndexCore PostgreSQL directly.
- There is one active write-orchestration owner per database, protected by the PostgreSQL advisory writer lock.
- The trusted Mutation Hint listener is a separate exact-loopback-only interface; it is not a public application API.
- Missing or incomplete observations are not deletion evidence. Preserve the accepted absence, topology, identity, generation, FIFO, and Journal fences.
- serve does not auto-migrate. Schema changes are explicit SQL-first migrations.

Do not introduce product users/auth, search/catalog UX, preview/download, sharing, AI recommendations, direct 115 integration, or CloudSite-specific business logic into the Kernel.

## 4. Current authorization limits

The accepted Alpha foundation is in maintenance / incremental hardening.

- Gate 1–4 are closed and Architect-accepted.
- P12 shortened cross-repository test validation is accepted; the original >=30-minute / >=100-visibility soak was not run and is not claimed.
- Gate 5 is **NOT AUTHORIZED**.
- The hybrid incremental runtime repository default remains disabled.
- INDEXCORE_INCREMENTAL_READ_WORKERS defaults to 1. Values 2/4/8 are explicit opt-ins, not production authorization.
- F1 first-discovery and F2 mirror-revisit remain default-off unless a separate, reviewed, measured authorization says otherwise.
- No second writer, parallel P6 cycles, public Hint, native provider delta/cursor, automatic INTERNAL retry, automatic BLOCKED/SUSPENDED repair, destructive scoped removal, or schema expansion is authorized by a generic “continue” request.
- Open performance/reliability Issues and draft PRs are not accepted work merely because they exist.

A new feature, Gate, schema/contract change, production rollout, or change to a frozen invariant requires a bounded task and explicit authorization.

## 5. Required task workflow

1. **Baseline:** record the exact base SHA, branch, relevant Issue/PR, and current CI state.
2. **Classify:** identify whether the request belongs to Collector, Kernel, Store, Query/Transport, Consumer, Operations, or is out of scope.
3. **Bound:** define one objective, allowed files/modules, forbidden changes, dependencies, risks, and measurable acceptance criteria.
4. **Plan:** list the intended edits and tests before implementation. Stop for Architect review if the task requires a new invariant, schema/contract change, or cross-module redesign.
5. **Branch:** create a task-specific branch from the verified base. Never work directly on main.
6. **Implement:** make the smallest coherent change. Preserve existing public contracts unless explicitly authorized.
7. **Verify:** run focused tests, required PostgreSQL 18 tests, race tests where relevant, vet, build, and exact-head CI.
8. **Handoff:** report exact commit, changed paths, commands/results, evidence, known limits, and unresolved risks.
9. **Review:** wait for independent Architect review. Do not self-merge.
10. **Close:** mark DONE only after the accepted change is merged and its required post-merge verification is complete.

## 6. Task states

Use these states consistently:

- TODO — recorded, not ready to execute.
- READY — scope, dependencies, and acceptance criteria are clear.
- IN_PROGRESS — one Executor is actively working.
- REVIEW — implementation and evidence are submitted for independent review.
- FIX_REQUIRED — review found a concrete correction.
- BLOCKED — an external dependency, missing evidence, or authorization prevents safe progress.
- DONE — accepted, merged, and verified.

Do not use “complete” for a test that was shortened, skipped, partially run, or not performed.

## 7. Evidence and reporting

Classify statements as:

- **FACT:** directly observed repository, code, database, or provider evidence.
- **VERIFIED:** reproduced by a named test or a bounded, recorded operation.
- **INFERENCE:** plausible explanation that is not yet proven.
- **UNKNOWN / NOT_TRACKED:** evidence is unavailable; do not fill the gap with assumptions.

A completion report must include:

- Issue and exact base/head SHA;
- changed files and responsibility classification;
- behavior changed and behavior explicitly not changed;
- test commands, environment, exit status, and CI run for the exact head;
- production/provider effects (or an explicit statement that none occurred);
- limitations, skipped tests, missing telemetry, and rollback considerations;
- next action and current task state.

Never claim a production action from GitHub evidence alone.

## 8. Safety rules

- Tests use owned, disposable PostgreSQL 18 databases only. Never point test/reset/scale commands at a production DSN.
- Never edit Canonical, Journal, Snapshot, Work, admission, or Root state with ad hoc production SQL.
- Do not reset queues, bulk-retry work, force scans, send production Hints, or generate provider traffic without explicit task authorization and a bounded request budget.
- Do not expose DSNs, tokens, provider responses, source paths, user file names, raw SQL, or secrets in logs, Issues, PRs, or evidence.
- Preserve failed evidence. Do not weaken assertions or relabel partial runs to obtain PASS.
- A lower queue count or a green CI run is not, by itself, proof of product freshness, provider health, production stability, or performance improvement.

For the full procedures, see docs/DEVELOPMENT.md, docs/AI-DEVELOPMENT-PROTOCOL.md, and docs/OPERATING-STANDARDS.md.