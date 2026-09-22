# Index Core — Project State

> This file is the concise current-state truth.
> Keep it short enough for fast AI/session recovery.
> Historical detail belongs in research reports and ADRs.

## Last updated

2026-09-23

## Main branch

Latest accepted baseline:

`80ce33846fca8b4b2e2c2eef236ad2afed49e301`

Accepted content includes:

- project blueprint v0.1
- context governance
- PostgreSQL-first persistence decision
- Discovery 01 Xiaoya research
- Discovery 02 AList/OpenList collector research

## Current phase

**Post-D02 Decision Review**

Status:

**ACTIVE**

No D03 research is currently authorized.

The project is deciding, from D02 evidence, whether a narrow rclone/fsspec comparison can materially change architecture boundaries.

## Latest accepted phase

**Discovery 02 — AList / OpenList Collector capability investigation**

Status:

**ACCEPTED**

Accepted consolidated report:

`docs/research/ALIST-OPENLIST-COLLECTOR-DISCOVERY-REPORT.md`

## D02 accepted findings

1. AList/OpenList search index is a search projection, not Canonical Inventory.
2. Public FS APIs can recursively enumerate resource candidates.
3. Public API success does not prove provider-complete Snapshot semantics.
4. `len(content)==total` and storage health checks are only weak sanity checks.
5. `Parent + Name` / virtual path is a path-based matching key, not stable resource identity.
6. AList provider `id` is driver-dependent.
7. OpenList public FS API does not expose a general provider object ID.
8. Representative drivers show materially different ID/hash/mtime behavior.
9. No public native delta/change feed was found.
10. AList/OpenList search indexing lacks durable checkpoint/resume, staging and atomic reconcile.
11. Provider partial-list behavior can create unsafe deletion signals.

## Current decision question

Should the project run a narrow D03 comparison of rclone (and only if justified, fsspec)?

D03 is worth doing only if it could materially change one or more of these boundaries:

1. whether AList/OpenList remain the primary external Collector candidate
2. whether Stable Identity must be owned entirely by IndexCore
3. whether Snapshot completeness / partial-failure semantics must be owned entirely by IndexCore
4. whether IndexCore must implement provider/scanner functionality itself

## Current control issues

- #1 — Project control tower
- #17 — Post-D02 decision review
- #18-#21 — closed / not authorized

## Accepted persistence decision

**PostgreSQL-first**

- PoC inventory uses PostgreSQL
- MVP canonical inventory uses PostgreSQL
- there is no SQLite-first formal implementation stage
- final schema is still deferred until Architecture Gate
- Kernel remains dependent on Store Interface rather than PostgreSQL-specific Domain types

## Architecture status

**NOT FROZEN**

Architecture Gate has not been entered.

## Implementation status

**NO PRODUCT CODE**

Do not create:

- formal kernel implementation
- provider adapters
- database migrations
- production APIs
- UI
- CloudSite integration

## Current options under review

### Option A — Skip D03

Proceed to Architecture Gate using AList/OpenList as the current Collector reference and make IndexCore explicitly own:

- stable identity
- completeness semantics
- safe reconcile
- canonical inventory
- removal safety

### Option B — Narrow D03

Investigate rclone only enough to answer:

- does it expose stronger cross-backend identity through a usable process/API boundary?
- does it surface partial traversal failures/completeness materially better than AList/OpenList?

Only add fsspec if rclone comparison leaves a material unresolved question.

## Current project risks

- choosing a donor because it looks powerful rather than because evidence changes the boundary
- rejecting a mature donor before testing the exact gap
- mistaking path/name/hash for stable identity
- treating successful traversal as complete snapshot
- reopening broad Discovery and delaying Architecture indefinitely

## State maintenance rule

Accepted project state lives in Git.

Architect decides the next gate from evidence, not chat momentum.
