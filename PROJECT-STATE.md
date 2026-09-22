# Index Core — Project State

> This file is the concise current-state truth.
> Keep it short enough for fast AI/session recovery.
> Historical detail belongs in research reports and ADRs.

## Last updated

2026-09-23

## Main branch

Latest accepted baseline:

`02a5a5e525ef5fd626418063f33b217b0b34e498`

Accepted content now includes:

- project blueprint v0.1
- context governance
- PostgreSQL-first persistence decision
- Discovery 01 Xiaoya research
- Discovery 02 AList/OpenList collector research

## Current phase

**Discovery 03 — Collector gap comparison**

Status:

**PLANNED / AUTHORIZED NEXT**

D03 is intentionally narrow.

It exists only because D02 proved two unresolved hard gaps:

1. no cross-driver stable resource identity
2. public traversal cannot self-prove provider-complete snapshot semantics

Research targets are limited to:

- rclone
- fsspec

The purpose is not to replace AList/OpenList by default.

The purpose is to determine whether mature alternatives already solve either hard gap before IndexCore designs its own solution.

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
12. D03 comparison is justified, but D02 does not select rclone/fsspec.

## Current control issues

- #1 — Project control tower
- #17 — Windows Foreman D03
- #18 — Worker A: rclone identity/metadata
- #19 — Worker B: rclone completeness/RC/failure semantics
- #20 — Worker C: fsspec identity/completeness gap check
- #21 — Worker D: independent collector gap matrix

## Accepted persistence decision

**PostgreSQL-first**

- PoC inventory uses PostgreSQL
- MVP canonical inventory uses PostgreSQL
- there is no SQLite-first formal implementation stage
- final schema is still deferred until Architecture Gate
- Kernel remains dependent on Store Interface rather than PostgreSQL-specific Domain types

## Architecture status

**NOT FROZEN**

Architecture Gate has not been reached.

Do not treat current diagrams or donor comparisons as final architecture.

## Implementation status

**NO PRODUCT CODE**

Discovery stage only.

Do not create:

- formal kernel implementation
- provider adapters
- database migrations
- production APIs
- UI
- CloudSite integration

unless a later accepted gate explicitly authorizes them.

## Current open questions

1. Can rclone expose a stronger cross-backend stable identity than AList/OpenList?
2. Can rclone make partial traversal failures visible enough to support Snapshot completeness decisions?
3. Can fsspec solve either stable identity or completeness more generally?
4. Which completeness properties must remain IndexCore-owned regardless of Collector?
5. Which identity properties must remain IndexCore-owned regardless of Collector?
6. After D03, is further Discovery necessary, or can Gate 0 close and Architecture Gate begin?

## Current project risks

- Mistaking a search index for canonical inventory
- Mistaking path/name/hash for stable identity
- Mistaking successful traversal for complete snapshot
- Mistaking directory refresh/diff for native delta
- Generalizing one backend's capability to all providers
- Letting donor capabilities expand the Kernel unnecessarily
- Letting AI workers change architecture outside their task
- Losing project truth in chat history instead of Git

## Current licensing posture

Research records license facts.

The project does not make broad legal conclusions from license names.

Current engineering posture:

- do not copy code without confirmed permission
- avoid coupling kernel implementation to restrictive/uncertain donor code until intentionally reviewed
- prefer public API / process boundaries where technically appropriate
- perform separate license review before actual adoption when needed

## State maintenance rule

Foreman may update this file only as part of an Architect-approved phase closeout or explicit context-maintenance task.

Foreman must not convert research candidates into accepted architecture.

Architect reviews every state transition before merge.
