# Index Core — Project Context

> Purpose: minimal durable context for any Architect / Foreman / AI session.
> Read this before detailed research documents.
> Rule: **Chat history is cache. Git repository is project memory.**

## 1. What this project is

Index Core is a planned independent, reusable resource indexing kernel.

It is **not** a CloudSite-only module.

CloudSite may become one consumer of Index Core, but the kernel must remain usable without CloudSite and without a specific storage product.

The project exists because resource discovery, provider access, search, application logic and state ownership became too coupled in previous CloudSite evolution.

## 2. Core problem

The target system must eventually accept resource facts from one or more collectors and maintain a trustworthy canonical resource inventory.

Candidate input sources may include:

- AList / OpenList
- snapshot files
- remote feeds
- provider-specific collectors
- other storage abstraction tools if later proven necessary

The kernel must not assume a specific collector implementation.

Conceptual flow:

```text
Collector / Snapshot / Feed
          ↓
      Validation
          ↓
       Identity
          ↓
         Diff
          ↓
     Safety Guard
          ↓
      Reconcile
          ↓
Canonical Inventory
          ↓
   Change Journal
```

## 2.1 Initial persistence decision

The first formal persistence target for PoC and MVP is **PostgreSQL**.

There is no SQLite-first implementation phase.

This is a storage implementation decision, not a Domain coupling decision:

- Kernel depends on Store Interface
- Domain types do not depend on PostgreSQL schema/ORM
- another store may be introduced later without redefining Canonical Inventory semantics

## 3. Responsibility boundaries

### Collector layer
Obtains external resource facts and produces a standard snapshot/change input.

Collector does not own Canonical Inventory.

### Index Kernel
Owns canonical resource truth, validation, identity, reconcile, safety and change journal.

### Storage layer
Persists kernel state behind an explicit store boundary.

### Consumer layer
CloudSite, search, catalog, media UI, admin UI and other applications consume kernel outputs.

Consumers do not become source-of-truth.

## 4. Current working hypothesis

Snapshot is the preferred formal input boundary.

This is still subject to Architecture Gate confirmation.

A collector may obtain that snapshot by:

- recursively reading an API
- importing a pre-generated index asset
- reading a remote feed
- other mechanisms

The kernel should care about snapshot semantics, not how a provider was accessed.

## 5. Relationship to CloudSite

CloudSite v1.0.0 demonstrated that the application can work with AList as an access/download layer.

Index Core is being researched independently before any new CloudSite integration.

Current rule:

- do not modify CloudSite during Discovery
- do not copy CloudSite V2 code into this repository yet
- evaluate useful V2 mechanisms later, after external alternatives are understood

## 6. Relationship to AList / OpenList

AList/OpenList are currently being investigated as possible external Collector/provider aggregation boundaries.

They are not assumed to be the kernel database.

Their search database is not assumed to be canonical inventory.

Discovery 02 exists to determine exactly what their public APIs and drivers can reliably expose.

## 7. Relationship to rclone / fsspec

They are **not currently in Discovery 02**.

They are deferred candidates for Discovery 03 only if AList/OpenList prove insufficient as the external Collector boundary.

Do not introduce them early without an Architect decision.

## 8. Command topology

```text
Architect / ChatGPT
        ↓
Local Windows Foreman
        ↓
Worker A / B / C / D
        ↓
Foreman QA + cross-check
        ↓
single integration PR
        ↓
Architect final review
        ↓
main
```

Workers are untrusted execution units.

Worker PASS is not final acceptance.

Only evidence, reproducibility, tests (once implementation begins) and Architect acceptance allow changes into main.

## 9. Project memory hierarchy

### L1 — always read
1. `PROJECT-CONTEXT.md`
2. `PROJECT-STATE.md`
3. `ARCHITECTURE-INVARIANTS.md`
4. `NEXT-ACTIONS.md`
5. current control / Foreman GitHub issues

### L2 — read when needed
- `docs/research/`
- `docs/decisions/` (when ADRs exist)
- contracts and architecture docs

### L3 — evidence
- source code
- exact commits
- test output
- external donor repositories

Do not load all L2/L3 material unless the task requires it.

## 10. Context recovery protocol

A fresh Architect/AI session must:

1. read the four L1 files in order
2. read the current GitHub control issue
3. read the active Foreman issue
4. verify main HEAD
5. identify current phase and latest accepted gate
6. only then read deeper reports required for the current decision
7. do not start implementation merely because older chats mention implementation ideas

## 11. Worker context isolation

Foreman should give each Worker a narrow Task Packet containing only:

- TASK-ID
- GOAL
- minimum BACKGROUND
- INPUT / repositories / exact commits
- MUST ANSWER
- INVARIANTS relevant to that task
- OUTPUT
- DONE WHEN
- DO NOT

Do not send the entire project history to every Worker.

## 12. Evidence standard

Research claims should be categorized as:

- VERIFIED / FACT
- INFERENCE
- UNKNOWN

Capabilities should use precise statuses where relevant:

- DIRECT
- DERIVABLE
- DRIVER_DEPENDENT
- UNAVAILABLE
- UNKNOWN

Do not turn one driver's behavior into a universal provider capability.

## 13. Where detailed history lives

Accepted Xiaoya research:

`docs/research/XIAOYA-INDEX-ARCHITECTURE-REPORT.md`

Project blueprint:

`通用资源索引内核项目蓝图 v0.1.md`

Current phase and tasks:

`PROJECT-STATE.md`
`NEXT-ACTIONS.md`
