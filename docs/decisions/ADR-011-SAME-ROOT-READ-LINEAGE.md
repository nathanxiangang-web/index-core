# ADR011 proposal: frozen same-root read lineage

Status: Issue138 PR-A CANDIDATE, NOT Architect accepted or runtime-enabled.
Baseline main562a852, merged-main CI40081586002 SUCCESS verified before coding.

## Modules and dependencies

Unwired SameRootPrototype -> shared readScope (N=1/2/4/8 bounded readers) ->
atomic frozen DRAFT batch -> existing SubmitAndAdmit -> Coordinator/Kernel.
Store owns lineage, dependency and transaction checks. Collector owns source
facts. Kernel/Canonical/Journal writers remain serial and provider-neutral.
The legacy ScanScope and version1 cross-root fence contracts remain unchanged.
No migration, extra queue, runtime pool, public API, config flag or real provider.

## Eligibility and durable order

Only1..8 distinct, already claimed, PRESENT sibling directory scopes of ONE
ACTIVE Root qualify. Root/ancestor/descendant batches are rejected before I/O.
The order is fixed by durable claim start time then scope key, NOT HTTP arrival.
Every read captures the same base generation, admission frontier and adapter
revision. Results must all be successful before they become executable input.

An atomic transaction persists ALL actual DRAFT Snapshots/entries and an identical
ordered Snapshot-ID roster in their provenance. This is the durable reservation
boundary, before any admission. A crash before it leaves only existing claims;
normal recovery must create a new claim epoch and refetch. A crash after it can
resume from the frozen source evidence without calling the provider again.
Partial persistence must roll back. No empty reservation masquerades as source
evidence. Retained result count<=8, entries<=1000 each, finite read/commit budgets.

The batch is deliberately bounded and barrier-based, not a streaming scheduler.
Unresolved predecessor or unexpected admission blocks/rejects later input.
Only the roster prefix may be admitted, exactly base_frontier+rank+1. Every
predecessor must have the exact normal terminal Snapshot/Admission/result receipt.
Its resulting generation must form a contiguous chain from the captured base.
Unknown/unrelated admissions, malformed roster, duplicate claim consumption,
configuration ABA, lifecycle/parent/claim changes invalidate the observation.

## Conflict detection, not path-only optimistic rebasing

Disjoint paths alone are insufficient: identity matching may use a global hash,
stable object ID or a missing size/mtime move candidate. At the atomic freeze
boundary, a consistent database snapshot captures a SHA256 dependency digest for
each slot. Its read set includes the directory and subtree/ancestors, global
same-hash/same-size/provider-ID candidates, and all non-NONE removal evidence.
It uses the latest authoritative identity observation, not the mutable projection.
Policy tuple revision is captured too. A conservative superset is intentional:
an unchanged dependency is necessary, not a promise of maximal concurrency.
The initial full-prior digest implementation failed the50k speed gate(1.1796x).
Predicate pushdown now transfers the SAME dependencies rather than all Root rows;
normal/generic plans are checked against the full-root Go reference. Kernel prior
loading is unchanged. Stage1/Stage2 are still NOT O(1): SQL may scan the inventory
and large candidate/missing/tombstone sets remain expensive. Population evidence
must expose CPU/memory/Root-lock costs, not promise arbitrary scalability.
Only sibling cohorts are currently eligible; mixed parent/child trees are not
silently represented by the flat sibling performance fixture.

Stage1 AND Stage2, under Root/adapter/policy/Watch/Work locks (preserving the
existing Root -> Watch -> Work relative order), verify
the durable predecessor chain and recompute that dependency digest. A changed
read set rejects the old observation even when the preceding scope was a sibling.
No canonical write, removal evidence or Journal event from it is authorized.
Recovery/refetch requires a new durable attempt; no in-place evidence edits.

The stored source generation and observed_at are NEVER changed to current values.
Version2 scoped_fetch_fence explicitly means captured base + validated committed
prefix, not the version1 exact-generation rule. Version3 directory coverage is an
explicit review-required extension: its Generation remains the ORIGINAL base;
Store must establish lineage and unchanged dependencies before using the normal
fresh complete direct-child gate. Version2 coverage retains exact-generation
semantics. No PARTIAL/virtual/failed/unknown listing gains absence authority.

## Recovery and limits

Frozen IDs are resumable on a fence-aware binary, preserving source timestamps.
Committed-but-unacknowledged slots replay their exact durable receipt so the
caller can complete its still-owned Work; acknowledged slots may be skipped.
Duplicate/foreign callbacks and consumed claim epochs fail closed. Cancellation
does not mark Work VERIFIED. The caller owns existing CompleteSuccess/typed
failure/recovery. The prototype cannot safely refund source requests across
processes and is not a durable global request ledger. Runtime budgets/backoff,
in-flight claim limits, fairness and automatic serial fallback belong to PR-B.

Conservative fallback: abort unresolved batch; recover/reclaim through existing
APIs; read fresh source facts and use the ordinary serial path. No raw SQL repair,
no fabricated current generation, and no retry of uncertain provider writes.

## Required proof and honest NO-GO

Real owned PG18 + fake OpenList, one Root with400+ durable queued scopes. Compare
N1/2/4/8, actual16/64/400 refreshes with1250ms wall delay; report queue/fetch/commit/
Q5/Q8 latency, calls, duplicates, accepted fraction and CPU/DB costs. Database
preseed sizes2k/20k/50k are not actual full traversals. Four-reader >1.5x is a gate,
otherwise record NO-GO. Include reversed replies, parent-child rejection, shared
identity dependencies, config ABA, late Hint, stale claim, missing/virtual input,
durable restart/backend termination and atomic persistence failure.
Soak30..60m is required for later runtime acceptance; disclose NOT_RUN if absent.

No production enablement or old-binary downgrade is certified. Explicit Architect
review must accept this provenance/coverage contract before integration or PR-B.
