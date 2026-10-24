# ADR009 proposal: budget before revisit cadence (Issue134 F3)

Status: DESIGN AND ISOLATED INVESTIGATION ONLY, not a runtime change. F2 code
integration was accepted at7c54a47 in PR136, merged db8f301; merged-main
CI40066617849 SUCCESS was verified before this branch. Production flags stay OFF.
Task: Issue134 comment6380420697. F3 does not authorize a scheduler rewrite,
production canary, concurrency, migrations or changes to Kernel/identity/Q8.

## Modules and experiment boundary

Test-only fixture -> existing PG18 Root/Watch/Work tables -> accepted selectors,
CAS and serial P6/P5 -> existing OpenList-compatible HTTP -> scanner/Kernel/Q5/Q8.
Query tracing obtains the actual Store SQL for read-only EXPLAIN ANALYZE; it does
not copy or replace selection SQL. A separate test-only capacity model illustrates
request budgets, never participates in runtime decisions. No new production port.

Resource ceiling on ssh dsh: owned PG18 container CPU1/memory1GiB/pids128,
512MiB tmpfs, restart=no/loopback; WAL64/32MB/checkpoint30s/fsync ON. Go
GOMAXPROCS2/GOMEMLIMIT512MiB. Per-scale bounded fixture/timeouts; no shared DB,
real115/OpenList or provider mutation. Exhaustion is BLOCKED, not permission to
raise caps, drop correctness checks or relabel partial work as a completed scan.

The2k/20k/50k populations are directory-rich DB load fixtures, not claims that
all source directories were traversed. SQL seeding is permitted ONLY after the
existing exact purpose/loopback/owner/PG18 guard. Directory rows are constructed
with consistent identity metadata, existing verified work and managed watches.
Subsequent sampled observations use real forced HTTP, Kernel and Journal. Record
seed count separately from HTTP count, completed change count and unobserved
changes. A1% source change set is not fully verified merely because samples pass.

## Capacity hypothesis and testable distinctions

Historical D0 throughput48.267 scopes/min is an older production observation,
not the new fixture's measured capacity.425 watches /2min =212.5 due intents/min,
before roots, Hint, first discovery or retry. P6 materializes at most five due
watches per cycle: PENDING can remain small while overdue-watch debt grows.
Measure both, oldest due age and oldest pending age, not only PENDING cardinality.

Measure fixed120s storm on2k/20k/50k, repeated no-change and1% late-file source
changes, new nested discovery, stale NORMAL and trusted Hint under root activity.
The fixture adds a real1250ms forced-list delay (not real upstream behavior),
reports actual refresh/min and scan/claim/Canonical timings with sample counts.
Also measure actual selector/root-canonical SQL plans and client-side quantiles,
DB size/temp/blocked-lock samples and container CPU/memory. A point-in-time lock
sample is not proof of zero historical contention. Finite traces are not a soak.

## Proposed budget contract, NOT implemented

Use a pessimistic measured service estimate: lower historical bound and recent
conservative EWMA, with hard request and concurrency ceilings. Classify costs in
source-refresh units (refresh=true can reach the real upstream). Reserve capacity
for roots, trusted Hint/explicit HIGH, first discovery/aged NORMAL and retries
BEFORE granting background verification tokens. Keep durable pending intent and
accepted F1 fairness; do not quietly push a Hint behind background due dates.

Illustrative allocation: at most50% of conservative capacity for background
revisit; the remaining50% is not free capacity to spend on speculative reads.
If N/B exceeds the allowed detection interval, report an unsatisfied freshness
budget. At50k and48.267/min even the entire capacity needs over17h per round;
50% needs over34h. Current F2 maximum interval1h is insufficient, not a knob to
silently raise. A later reviewed design must choose bounded rolling cohorts /
durable round cursor and explicit freshness reporting, or reduce active scope
coverage with an operator-visible contract. Do not create50k short-interval
watches and infer bounded load from the five-row materialization cap.

Simulation must separate offered demand from actual coalesced Work; backlog in
due ages and unsatisfied freshness still counts. Adaptive model results are
MODELED, not proof runtime adaptation exists or that production queue is stable.
Trusted work exceeding total service capacity must be durably retained and
reported overloaded; no model may claim bounded queues under arbitrary arrivals.

## Watch lifecycle proposal, NOT implemented

P3 watch identity is only(root,scope), not canonical resource identity. Existing
CASUpdateWatch does not atomically validate canonical identity/adapter epoch.
Therefore disappearance, a child404, inactive root or BLOCKED work is NOT by
itself permission to delete/retire a watch. Stopping due selection for an inactive
root does not reclaim watch cardinality. Preserve histories and tombstones.

A future bounded retirement transaction must lock Root -> adapter/Watch -> Work
in the accepted order and revalidate exact managed ownership, watch version,
canonical resource ID/epoch and trusted committed removal evidence/Journal
anchor. Reject path reuse/replacement, concurrent Hint or IN_FLIGHT/unknown
intent. Suspend or retire schedule via CAS, retaining work/provenance/failure
history and a reason/lineage record. Operator/adaptive watches are not owned.
Reappearance with a new resource ID requires fresh discovery and schedule, not
reactivating the old ID. ABA/adapter changes require retrying the new proof, not
blindly applying old CAS. This needs a separately reviewed API/schema decision;
this investigation only demonstrates current lifecycle gaps and existing CAS.

## Parallel prefetch decision gate

Do not add readers now. Decompose measured forced-list wait, selector queries and
scan-return remainder (not mislabeled exact admission critical path). Only if
source wait dominates AND a real upstream request budget permits it should a
separate bounded read-only prefetch design be considered. It requires immutable
source evidence, adapter/generation/config fences, small capped buffers, bounded
cancellation/retry, and serial Kernel admission/Canonical/Journal. Never add
multiwriter, speculative duplicate refresh or weaker deletion trust to improve
a benchmark. SQL/index/whole-root materialization bottlenecks are not solved by
more source calls.

## Acceptance and extension

Deliver one design/measurement PR, explicit PASS/BLOCKED/NOT_RUN receipts, raw
logs and caps. Full regression/race and exact-head CI remain independent gates.
Keep late-file/nested/virtual/identity/no-false-removal/restart/Hint tests. Current
capacity may fail; do not silently implement a scheduler fix in this PR. Future
runtime changes,20k/50k full traversal/soak and any live canary require separate
reviewed scope. Keep Issue134 OPEN and leave PR126/#133 untouched.
