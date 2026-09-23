# Index Core — Project Context

> Purpose: minimal durable context for any Architect / Codex execution session.
> Rule: **Chat history is cache. Git repository is project memory.**

## 1. What this project is

Index Core is an independent, reusable resource indexing kernel.

It is **not** a CloudSite-only module.

CloudSite may become one consumer, but the kernel must remain usable without CloudSite and without a specific storage product.

## 2. Core problem

The system accepts external resource facts from collectors and maintains one trustworthy Canonical Inventory.

Conceptual flow:

```text
Collector / Snapshot / Feed
          ↓
      Validation
          ↓
       Identity
          ↓
   Completeness Gate
          ↓
      Reconcile
          ↓
Canonical Inventory
          ↓
   Change Journal
```

Candidate external sources may include AList/OpenList, rclone-backed collectors, snapshot files, remote feeds or provider-specific adapters.

The Kernel must not assume a specific Collector implementation.

## 3. Persistence direction

The first formal persistence target for PoC and MVP is **PostgreSQL**.

There is no SQLite-first phase.

This is an implementation choice behind a Store Interface:

- Kernel Domain does not depend on PostgreSQL schema/ORM
- Store persists Kernel-defined Domain state
- another Store may be introduced later without redefining Canonical Inventory semantics

## 4. Frozen responsibility boundaries

### Collector
Obtains external facts and emits normalized Snapshot / evidence.

Collector does not own Canonical Inventory, canonical identity, final completeness acceptance, removal or Canonical Change Journal.

### Index Kernel
Owns:
- Canonical Inventory
- identity continuity semantics
- Snapshot acceptance
- completeness/safety gate
- Safe Reconcile
- root/generation semantics
- Canonical Change Journal semantics

### Store
Persists Kernel-defined Domain state and supplies atomic commit / rollback / concurrency guarantees behind the Store Interface.

### Consumer
CloudSite, Search, Catalog and future applications read Kernel truth through Query contracts and may maintain projections.

Consumers do not redefine resource truth.

## 5. Relationship to external tools

### AList / OpenList
Useful provider aggregation / access boundaries.

Accepted research shows their search index is not Canonical Inventory and public identity/completeness semantics are driver-dependent.

### rclone
Accepted research shows stronger generic traversal-error propagation than AList/OpenList, but stable ID/hash/change-notify remain backend-dependent.

### fsspec
Useful reference abstraction; no evidence currently requires it in the main path.

No final Collector has been selected.

## 6. Relationship to CloudSite

CloudSite remains separate while Index Core architecture is frozen.

Do not modify CloudSite during Gate 1.

Integration is deferred until Index Core architecture and PoC are accepted.

## 7. Execution topology

The project execution model is intentionally small:

```text
ChatGPT Architect
       ↓
Codex Executor
       ↓
branch + docs/code/tests
       ↓
Pull Request
       ↓
ChatGPT Architect Review
       ↓
main
```

### ChatGPT Architect owns
- architecture
- scope
- invariants
- gate definitions
- task packets
- review / acceptance
- decisions to advance phases

### Codex Executor owns
- repository inspection required by the assigned task
- implementation of the Architect-approved task
- docs/code/tests/fixtures required by that task
- branch/commit/push/PR preparation
- evidence of correctness
- reporting blockers instead of inventing architecture

Codex must not silently:
- create new gates
- change accepted invariants
- broaden project scope
- select deferred architecture on its own
- merge its own architecture PR

There is no Foreman layer, no Worker A/B/C/D production topology and no subagent layer in the formal project workflow.

## 8. Quality model

Codex output is not accepted because it says PASS.

Architect review uses:
- architecture invariants
- evidence
- cross-contract consistency
- reproducible tests once implementation starts
- fault injection / failure-path verification where appropriate

A high rework rate is treated as a signal to narrow the task and tighten the contract, not to add more autonomous execution layers.

## 9. Project memory hierarchy

### L1 — always read
1. `PROJECT-CONTEXT.md`
2. `PROJECT-STATE.md`
3. `ARCHITECTURE-INVARIANTS.md`
4. `NEXT-ACTIONS.md`
5. current control Issue
6. current Codex execution Issue

### L2 — read when needed
- accepted architecture docs
- `docs/research/`
- `docs/decisions/`
- current PR review

### L3 — evidence
- source code
- exact commits
- tests / fixtures
- external donor repositories

Do not load all L2/L3 material unless required by the task.

## 10. Context recovery protocol

A fresh Architect or Codex execution session must:

1. read the four L1 files in order
2. read control Issue #1
3. read the active Codex execution Issue
4. verify remote `main` HEAD
5. identify current phase and latest Architect-accepted gate
6. read only the deeper material required by the current task
7. stop and report if repository state conflicts with the task packet

## 11. Evidence standard

Claims should be categorized as needed:

- FACT / VERIFIED
- INFERENCE
- UNKNOWN

Provider capabilities should remain precise:

- DIRECT
- DERIVABLE
- DRIVER_DEPENDENT
- UNAVAILABLE
- UNKNOWN

Do not turn one driver's behavior into a universal provider capability.

## 12. Detailed history

Accepted research:
- `docs/research/XIAOYA-INDEX-ARCHITECTURE-REPORT.md`
- `docs/research/ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md`
- `docs/research/D03-COLLECTOR-GAP-COMPARISON.md`

Project blueprint:
- `通用资源索引内核项目蓝图 v0.1.md`

Current state:
- `PROJECT-STATE.md`
- `NEXT-ACTIONS.md`
- Issue #1
- active Codex execution Issue
