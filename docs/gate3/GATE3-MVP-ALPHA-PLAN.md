# Gate 3 — MVP Alpha / Standalone Runtime & Scale Plan

> Architect decision: 2026-09-24
> Execution issue: #47
> Baseline: main@410050d0b084d999063cde1a4be8d2051fbcb88f
> Executor branch: alpha/gate3-runtime
> Status: ARCHITECT_ACCEPTED — READY_TO_MERGE
> Final verification head: 055402da83235e6dc5f88f45206378fb210a7672 (PR #49)

## 1. Purpose

Gate 2 proved the correctness of the IndexCore Kernel, PostgreSQL Store,
Canonical Journal, Query Contract and additive rclone Collector boundary.

Gate 3 is the blueprint's existing MVP gate. It turns that PoC into a standalone
Alpha runtime that can later be integrated by CloudSite in Gate 4 without making
CloudSite part of IndexCore.

## 2. Runtime boundary

```text
real source
   ↓
Collector Adapter
   ↓
DRAFT Snapshot + entries
   ↓
SUBMITTED
   ↓
Coordinator
   ↓
Canonical Inventory + Journal
   ↓
Query Contract
   ↓
read-only HTTP /v1
```

The daemon/orchestration layer may invoke Kernel and Store interfaces but must
not redefine Domain or bypass the Coordinator.

## 3. Locked implementation decisions

- binary: `indexcore`;
- Go 1.27.x;
- PostgreSQL 18.x;
- pgx/v5;
- SQL-first migrations;
- no ORM;
- explicit `indexcore migrate`;
- `indexcore serve` verifies schema but does not auto-migrate;
- one active write daemon per DB in Gate 3;
- different roots may run concurrently with a bounded worker limit;
- `net/http` for the Alpha read transport;
- HTTP binds to loopback by default until an auth layer exists;
- `log/slog` structured logs;
- env + flags configuration;
- rclone stays external;
- no Redis/Kafka/MQ/DI framework.

## 4. Source integration

### rclone

Must drive the complete runtime path, but remains additive-safe because its
current traversal mode does not prove confirmed no-skips.

### AList / OpenList

Gate 3 final acceptance requires a real AList/OpenList instance integration.

Rules:

- implement as a Collector Adapter;
- normalize to the frozen Snapshot contract;
- do not couple Kernel to AList/OpenList database tables;
- do not copy upstream source code;
- start additive-safe;
- any claim of destructive-safe COMPLETE requires separately reviewed evidence
  for the exact traversal mode.

CloudSite is not the AList/OpenList adapter. CloudSite remains a future Consumer.

## 5. Runtime commands

Minimum Alpha command surface:

```text
indexcore migrate
indexcore serve
indexcore root ...
indexcore scan --root <root_id>
```

A `doctor`/diagnostic command may be added if useful.

Root lifecycle mutations must use the frozen Kernel transaction/journal semantics,
not direct ad-hoc SQL.

## 6. Query HTTP transport

Expose Q1-Q9 through a provider-neutral read-only `/v1` transport.

Transport cannot hold Store mutation capabilities.

Generation-bound pagination and STALE_CURSOR semantics must survive HTTP
serialization.

Health:

- `/healthz` = process alive;
- `/readyz` = DB reachable + compatible schema + runtime ready.

## 7. Recovery model

Gate 3 must use the accepted durable admission model:

- resume existing absolute PENDING head;
- same admission_seq on recovery;
- no leapfrog;
- restart must not create duplicate work;
- bounded retry/backoff for transient runtime failure;
- no busy-spin.

Gate 3 does not implement multi-daemon distributed claiming/HA.

## 8. Scale proof

Hard Gate-3 scale floor: **20,000 resources** on real PostgreSQL 18.

Record at least:

- initial population wall time;
- identical repeat;
- small delta;
- Query pagination;
- Journal growth;
- row counts / approximate DB size;
- memory/peak RSS when reproducible.

100,000 resources is exploratory. No arbitrary latency SLO is frozen in Gate 3.

## 9. Packaging

Provide a reproducible Alpha build and local deployment:

- binary build;
- Dockerfile;
- PostgreSQL 18 compose/example;
- explicit rclone runtime dependency;
- persistent DB volume;
- restart/shutdown instructions.

No Kubernetes or HA.

## 10. Non-goals

- CloudSite integration;
- UI;
- auth/account/tenant system;
- Scanner Resume/provider traversal checkpoints;
- provider-native delta/true incremental;
- destructive-safe provider COMPLETE without new evidence;
- multi-daemon HA/distributed leases;
- Search/Catalog/media/AI;
- downloader/115 integration.

## 11. Acceptance

Gate 3 exits only when the exact Issue #47 P0-P11 work package is demonstrated,
the full Gate-2 regression suite remains green, the 20k scale report is
reproducible, real rclone and real AList/OpenList source paths have been exercised,
and no frozen Gate-1 semantics were changed.
**Result: ACCEPTED** by the Architect at PR #49 head
`055402da83235e6dc5f88f45206378fb210a7672` — Issue #47 P0-P11 demonstrated, the
full Gate-2 regression suite green, the 20k full-runtime-ingestion baseline
reproducible, real rclone + real AList/OpenList source paths exercised, and
`FROZEN_CONTRACT_CHANGES: NONE`.
