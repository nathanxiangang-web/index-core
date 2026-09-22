# Index Core — Project State

> This file is the concise current-state truth.
> Keep it short enough for fast AI/session recovery.
> Historical detail belongs in research reports and ADRs.

## Last updated

2026-09-23

## Main branch

Latest accepted baseline:

`db7663b30862a969625dd006b0dc0a868df0ab49`

Accepted content at that baseline:

- project blueprint v0.1
- Discovery 01 Xiaoya reports
- Xiaoya consolidated architecture research report

## Current phase

**Discovery 02 — AList / OpenList Collector capability investigation**

Status:

**PLANNED / NOT YET ACCEPTED**

No formal Index Core implementation is authorized yet.

## Latest accepted phase

**Discovery 01 — Xiaoya indexing chain investigation**

Status:

**ACCEPTED**

Accepted consolidated report:

`docs/research/XIAOYA-INDEX-ARCHITECTURE-REPORT.md`

## Current control issues

- #1 — Project control tower
- #9 — Windows Foreman D02
- #10 — Worker A: AList/OpenList indexing internals
- #11 — Worker B: AList/OpenList public FS API/provider metadata
- #12 — Worker C: AList/OpenList driver capability differences
- #13 — Worker D: independent AList/OpenList → Snapshot capability matrix / counter-evidence

## Current D02 scope

D02 is intentionally limited to **AList / OpenList**.

The immediate question is:

> Can AList/OpenList already serve as the external Collector/provider aggregation boundary, so Index Core does not need to implement dozens of cloud-storage scanners?

### Explicitly deferred

- rclone
- fsspec
- other provider abstraction frameworks

These become D03 candidates only if D02 proves AList/OpenList insufficient.

## Architecture status

**NOT FROZEN**

Architecture Gate has not been reached.

Do not treat current diagrams or candidate designs as final.

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

## Accepted findings from D01

1. Xiaoya demonstrates a useful pattern: pre-generated index assets can be distributed and imported by clients.
2. The exact Xiaoya index generator and generation location were not proven.
3. Xiaoya `index.zip` is insufficient as a general canonical resource snapshot because important resource facts are missing.
4. AList `x_search_nodes` is a search projection, not proof of a canonical resource inventory.
5. Xiaoya search data does not provide a reliable resource-level stable object identity.
6. Safety ideas such as completeness gating and soft deletion are useful references, but no donor implementation is accepted as the Index Core safety model.
7. Search/access and resource truth should remain separate concerns.

## Current open questions

1. Can AList/OpenList public API enumerate an entire root reliably?
2. Which resource fields are directly exposed?
3. Which fields are driver-dependent?
4. Is provider-native object ID exposed through the public API?
5. How reliable are mtime/hash semantics across drivers?
6. Can cache/refresh behavior cause stale or incomplete snapshots?
7. Is there any genuine native change/delta feed?
8. How are multiple storage roots uniquely isolated?
9. Does rename/move preserve any externally visible stable identity?
10. After D02, is D03 investigation of rclone/fsspec necessary?

## Current project risks

- Mistaking a search index for canonical inventory
- Mistaking path/name for stable identity
- Mistaking directory refresh/diff for native delta
- Generalizing one driver's fields to all providers
- Letting AI workers design architecture outside their task
- Letting discovery scope expand before a gate is accepted
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
