# Index Core — Project State

> Concise current-state truth for recovery.

## Last updated

2026-09-23

## Main branch

Always verify the current remote `main` before execution.

Governance transition commit:

`24a084c51f88a2eafd4ec7d30569963192619f96`

Accepted content includes:

- project blueprint v0.1
- context governance
- PostgreSQL-first persistence decision
- D01 / D02 / D03 accepted research
- Gate 1A responsibility boundary and contract skeleton
- removal of the experimental subagent workflow

## Execution model

```text
ChatGPT Architect
       ↓
Codex Executor
       ↓
Pull Request
       ↓
ChatGPT Architect Review
       ↓
main
```

The previous Foreman + Worker A/B/C/D topology is retired.

The experimental subagent topology is retired.

## Current phase

**Gate 1B — Core Semantics**

Status:

**REWORK / CONSOLIDATION**

No product implementation is authorized.

Active execution issue:

**#37 — [CODEX][GATE-1B] Core Semantics Rework**

Legacy PR #35 and legacy Worker Issues #29-#33 are superseded by #37 and must not drive new work.

## Latest accepted architecture phase

**Gate 1A — Responsibility Boundary & Contract Skeleton**

Status:

**ACCEPTED**

Accepted documents:

- `docs/architecture/GATE1A-RESPONSIBILITY-BOUNDARY.md`
- `docs/architecture/GATE1A-COLLECTOR-CONTRACT-SKELETON.md`
- `docs/architecture/GATE1A-STORE-QUERY-CONTRACT-SKELETON.md`
- `docs/architecture/GATE1A-BOUNDARY-ATTACK-REPORT.md`

## Gate 1A frozen boundaries

### Collector
Owns traversal/pagination/provider-error capture/SnapshotEntry production/normalized evidence.

Does not own canonical identity, final completeness acceptance, confirmed removal, canonical generation, Canonical Change Journal or direct Canonical Inventory mutation.

### Kernel
Owns Canonical Inventory, identity continuity, Snapshot acceptance, completeness safety, Safe Reconcile, root/generation semantics and Canonical Change Journal semantics.

### Store
Persists Kernel-defined Domain state and supplies atomic commit / rollback / concurrency protection behind the Store Interface.

### Consumer
Reads canonical/query state and owns projections; cannot mutate Canonical Inventory or redefine truth.

## Gate 1B scope

Freeze:

1. Domain Model
2. Stable Identity v1
3. Snapshot Contract core fields/evidence
4. Completeness acceptance semantics
5. Safe Reconcile
6. Failure Model
7. Canonical Change Journal semantics
8. deterministic per-root input ordering
9. root ownership/lifecycle semantics

## Gate 1B Architect rework requirements

The current Gate 1B design must correct:

- hash is evidence, not identity
- same path/size is not sufficient identity proof
- provider IDs require stability qualification before strong use
- rename/move continuity horizon must be compatible with removal safety
- directory-move v1 semantics must be frozen or conservatively unsupported
- PARTIAL/STALE/SUSPICIOUS absence must not mutate prior canonical truth
- freshness evidence must be provider-neutral
- Collector failure-visibility/completeness assurance must be explicit
- Kernel must not re-traverse providers for removal validation
- canonical lifecycle state vs removal-control state must be unambiguous
- Journal append-only semantics must not conflict with repair
- MOVE/RENAME + UPDATE semantic result must be frozen
- per-root accepted input ordering must be deterministic
- no Gate 1D
- no subagent/Foreman/4-worker workflow in formal docs

## Gate 1B must NOT design

- PostgreSQL schema / SQL / migrations
- exact Query API transport
- journal physical persistence/event schema
- final Collector selection
- Scanner checkpoint/resume
- native delta / true incremental
- product code

## Persistence decision

**PostgreSQL-first**

Persistence shape remains Gate 1C.

Domain remains independent of PostgreSQL schema/ORM.

## Implementation status

**NO PRODUCT CODE**

Gate 2 PoC remains blocked until Gate 1B and Gate 1C are Architect-accepted.
