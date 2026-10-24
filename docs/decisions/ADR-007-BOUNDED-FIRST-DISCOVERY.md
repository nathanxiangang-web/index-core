# ADR007: finite first-discovery and aging slots (Issue134 F1)

Status: F1 code integration accepted in PR135 at6dea288; merged main f9bbc4e,
Architect comment6368555436. Exact-head CI39976674601 and merged-main
CI39979311690 passed. Not production-enabled and not a replacement for
ADR004/005/006 or P2 state rules. F2 default-OFF integration is accepted under
ADR008; F3 capacity remains a separate investigation under ADR009.

## Motivation and scope

[Measured D0](../p0/P0-134-D0-FIRST-DISCOVERY.md) demonstrates multi-minute waiting
for a newly discovered ordinary directory with roughly1s individual scans.
Do not promote an entire mirror root to HIGH, remove protected absence checks,
or hide delay by making consumers read storage directly. F2 requeue suppression
and F3 capacity/prefetch remain separate PR/design gates.

## Modules / dependency graph

Runtime config -> app composition -> existing serial CycleRunner -> Executor
planning -> additive FirstDiscoveryStore (read-only SQL selectors) -> unchanged
ClaimWork CAS -> exactly one scoped scan -> existing atomic completion/Kernel/Q8.

Enable only by `INDEXCORE_INCREMENTAL_FIRST_DISCOVERY_ENABLED=true` or the
equivalent `--incremental-first-discovery` flag. Default false preserves the
accepted priority/FIFO selector exactly. One-shot CLI execution outside a cycle
also retains the legacy selector. Missing planning port fails startup closed.

## Eligibility and proof

A first-discovery candidate is eligible PENDING/NORMAL, never claimed
(attempt_count0), never verified, RECOVERY/POSSIBLE_CHANGE, non-root and within
the eight-level discovery bound. Root must be ACTIVE. Existing positively
observed, unambiguous PRESENT directory evidence must match the adapter's
provider-path namespace, not a UI/root-relative guess. The direct parent Work
must have a successful verification and no current error/blocked/retry state;
non-root parent must also have unambiguous PRESENT directory evidence. Both
child and non-root parent evidence must have removal_evidence_state=NONE.

This narrowly supports the existing alist/openlist scoped-refresh contract.
Another collector needs its own namespace/proof adapter and tests, not guessed
provider identity. Scope deletion, ambiguity or root changes after selection
still encounter the normal scanner's fresh guards; a planning read grants no
canonical authority and no ownership. ClaimWork version CAS is unchanged.

## Finite deterministic window

- Each opted-in cycle has3..5 selected-item slots (existing wall cap<=60s).
- Its first slot checks for oldest eligible NORMAL/LOW work waiting>=30s,
  ordered by first_seen,root,scope. One such slot guarantees old/sibling work
  progresses even under continuously arriving HIGH or first discoveries.
- At most min(2,max_items-2) first-discovery slots, at most ONE per root per
  cycle, oldest first_seen then root/scope. Existing runtime burst cap4 therefore
  bounds this to8 global /4 per-root fast slots per burst. No durable priority
  is changed, and a directory loses first-scan eligibility after its first claim.
- Remaining slots use unchanged URGENT>HIGH>NORMAL>LOW, first_seen/root/scope FIFO.
  At least one slot remains for that queue even if both reserved lanes are full.
- Empty cohort probes fall back; an empty fresh cohort is probed at most once
  per cycle. Max three bounded planning reads before one claim, never a reselect
  after stale CAS, second provider request, new worker or concurrent scan.

Existing explicit-subtree HIGH semantics and durable merge/retry rules remain.
Only opted-in operational service order gains finite reserved slots; Canonical
admission order, identity, generations, Journal and source trust are unchanged.
Age/quotas are not promises during provider failure, canceled cycles or overload.
All planning state is cycle-local, serial and size-bounded; no process-global
per-root map, migration, queue rewrite or clearing of history is introduced.

## Verification / extension / rollback

Require real isolated PG18 + HTTP + Kernel/Q8 regression for401 older normal
scopes vs one fresh nested folder, actual claim positions and explicit modeled
source time separately from fixture wall time. Stress100+ fresh arrivals plus
HIGH flood/sibling normal progress; prove quota, CAS/rollback, namespace/parent,
known-child requeue, snapshot/identity/no-removal safety. Existing atomic discovery
completion remains the only child publication boundary; its rollback/crash
regressions remain mandatory. Add targeted race and full CI.

Rolling revalidation/requeue logic is deliberately unchanged (F2).2k/20k/50k and
fresh production target measurements are later explicit gates, not acceptance
claims for F1. Disable the flag to recover legacy scheduling, without touching
durable work, histories or canonical data. Deploy only after architecture review,
native hash/rollback checks and separate production GO per Issue134.

Test evidence: [F1 isolated results](../p0/P0-134-F1-ISOLATED-RESULTS.md).
