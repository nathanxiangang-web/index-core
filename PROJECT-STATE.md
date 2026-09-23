# Index Core — Project State

> Concise current-state truth for recovery.

## Last updated

2026-09-23

## Main branch

Latest accepted baseline:

`688f6b9493cd00a9a4ee46d57df298ed2a89cf99`

Accepted content includes:

- project blueprint v0.1
- context governance
- PostgreSQL-first persistence decision
- D01 / D02 / D03 accepted research
- Gate 1A responsibility boundary and contract skeleton

## Current phase

**Gate 1B — Core Semantics**

Status:

**READY TO START**

No product implementation is authorized.

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

### Collector / Scanner

Owns:

- provider traversal / pagination
- provider error capture
- SnapshotEntry production
- Snapshot-level evidence
- refresh / cache policy execution

Does not own:

- canonical resource identity
- completeness acceptance
- confirmed removal
- canonical generation
- canonical Change Journal
- direct Canonical Inventory mutation

### Kernel

Owns:

- Canonical Inventory
- identity continuity semantics
- Snapshot acceptance
- completeness safety gate
- Safe Reconcile semantics
- Root / canonical generation semantics
- Canonical Change Journal semantics
- canonical conflict resolution responsibility

### Store

Owns:

- persistence of Kernel-defined Domain state
- atomic canonical+journal+generation commit semantics
- rollback / concurrency protection behind Store Interface

### Consumer

May read canonical/query state and own projections.

Must not mutate Canonical Inventory or bypass Query Contract.

## Gate 1B scope

Gate 1B freezes core semantics only:

1. Domain Model
2. Stable Identity v1
3. Snapshot Contract final core fields/evidence
4. Completeness acceptance semantics
5. Safe Reconcile state machine
6. Failure Model
7. Canonical Change Journal semantics
8. concurrent canonical conflict semantics
9. root ownership / overlap semantics

## Gate 1B must NOT design

- PostgreSQL schema / SQL / migrations
- exact Query API transport
- journal persistence/event schema
- final Collector selection
- Scanner checkpoint/resume
- native delta / true incremental
- product code

These belong to Gate 1C or post-MVP phases.

## Gate 1A preconditions carried into Gate 1B

1. `parent_ref` must remain Collector-local and never be canonical `resource_id`.
2. completeness heuristics may inspect submitted evidence + prior canonical state only; they must not trigger provider traversal.
3. root overlap/ownership semantics must be explicitly resolved.
4. Canonical Inventory is authoritative over derived Journal/projection repair.
5. hash, provider_object_id and native delta remain optional capabilities.

## Accepted persistence decision

**PostgreSQL-first**

Persistence shape remains deferred to Gate 1C.

Domain must remain independent of PostgreSQL schema/ORM.

## Implementation status

**NO PRODUCT CODE**

Gate 2 PoC remains blocked until Gate 1B + Gate 1C are accepted.
