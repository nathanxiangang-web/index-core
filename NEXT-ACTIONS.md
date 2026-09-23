# Index Core — Next Actions

## Current phase

**Gate 3 — MVP Alpha / Standalone Runtime & Scale**

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex**

Status: **AUTHORIZED / IN PROGRESS**

Active execution issue:

**#47 — [CODEX][GATE-3] Standalone Alpha Runtime & Scale**

Executor branch:

**`alpha/gate3-runtime`**

Baseline:

**`main@410050d0b084d999063cde1a4be8d2051fbcb88f`** (Gate 2 merged and closed).

## Gate 3 objective

Turn the Architect-accepted Gate-2 Kernel PoC into a standalone, restartable,
deployable MVP Alpha without weakening any frozen Gate-1 semantics.

Required shape:

```text
real source / rclone / AList-OpenList adapter
        ↓
Collector Runtime
        ↓
DRAFT Snapshot + entries
        ↓
SUBMITTED
        ↓
Coordinator admit/resume + ProcessHead
        ↓
PostgreSQL Canonical Inventory + Journal
        ↓
read-only Query Service
        ↓
HTTP /v1
```

## Architect-locked decisions

- one Go binary: `indexcore`;
- Go 1.27.x + PostgreSQL 18.x + pgx/v5;
- explicit `indexcore migrate`; `serve` does not silently auto-migrate;
- single active write-orchestration daemon per DB in Gate 3;
- different roots may progress concurrently inside the daemon;
- HTTP transport uses `net/http` and is read-only;
- HTTP default bind is loopback; no auth system in Gate 3;
- root reconcile policy comes from persisted root config, not test-only injected config;
- rclone remains external and additive-safe;
- real AList/OpenList integration is a Gate-3 exit requirement, but remains a Collector Adapter;
- >=20,000 resources must be validated against real PostgreSQL;
- 100,000 resources is exploratory, not a hard acceptance threshold.

## Work order

Follow Issue #47 P0-P11 in order, with staged commits on one branch.

Recommended checkpoints inside the same branch/PR:

1. **Runtime foundation:** P0-P4
2. **Real source + Query transport:** P5-P7
3. **Scale / packaging / E2E:** P8-P11

Do not invent sub-gates or additional PRs.

## Required carry-forward regression

The full Gate-2 suite must remain green throughout Gate 3.

Do not weaken:

- per-root absolute FIFO;
- generation semantics;
- safe removal;
- Kernel-owned IO3 identity;
- append-only Journal;
- read-only Consumer boundary;
- path ambiguity;
- root lifecycle tombstones;
- rclone UNKNOWN-skip non-destructive behavior.

## Explicitly forbidden in Gate 3

- CloudSite integration;
- product UI/admin frontend;
- auth/account/tenant system;
- Scanner Resume / traversal checkpoint;
- provider-native delta / true incremental;
- destructive-safe provider COMPLETE without separately accepted evidence;
- multi-daemon writer / HA / distributed leases;
- Redis / Kafka / MQ;
- Search/Catalog/media/AI;
- downloader / 115 integration;
- Kubernetes;
- silent changes to Gate 1B/1C semantics;
- license-incompatible code copying.

## Review handoff

Open one PR only:

`alpha/gate3-runtime -> main`

Do not merge.

When P0-P11 are complete, use the status template in Issue #47 and stop for
ChatGPT Architect review.
