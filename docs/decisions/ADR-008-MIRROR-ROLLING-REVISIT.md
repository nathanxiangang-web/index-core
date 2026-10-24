# ADR008 proposal: coalesce mirror discovery, retain durable rolling checks

Status: F2 candidate under Issue134, not accepted or deployed. Base is F1 merged
main f9bbc4e5a98965e6c7773d2453823061bd4b1355; merged-main CI39979311690 passed.
ADR004/005/006/007, P2 Work merge semantics and all Kernel invariants remain.

## Responsibilities and dependency graph

Config (default-off revisit duration) -> existing serial Executor -> additive
mirror completion port -> existing Root/Watch/Work transaction -> existing due
watch materialization + ClaimWork CAS -> normal scoped scan -> Kernel/Q8.
No migration, new queue, concurrent source reader or change to Canonical writes.

Parent directory metadata is NOT proof its contents are unchanged. An unchanged
parent listing only repeats an intent to inspect a known child; it cannot cancel
a Hint or prove the absence of a late file. Therefore suppression is allowed
only while another durable, bounded route to that inspection exists.

## Exact opt-in rule

INDEXCORE_INCREMENTAL_MIRROR_REVISIT / --incremental-mirror-revisit is zero
(disabled) by default; a positive whole-second interval must be30s..1h. F1's
independent default-off flag is unchanged. Only successful mirror observations
use this policy, and only ordinary non-root children outside the explicit HIGH
DiscoveryScope. Existing provider namespace, direct-child,8-level/1000 caps apply.

Under the existing root lock, validate unique PRESENT directory/no-removal
evidence for the parent and child against the current alist/openlist adapter
namespace. Compare Canonical updated_at with Work last_verified_at to refuse
suppression after a positive identity/path/metadata/evidence change; this is not
provider mtime or proof of unchanged contents. Missing/ambiguous/unknown proof,
future verification time or error takes the legacy merge path.

Enroll one WARM/BACKSTOP_ENROLL watch with cadence MIRROR_REVISIT for each proven
ordinary child in the SAME parent-completion transaction. Initial next_due is
now+interval. Never overwrite an existing operator/adaptive watch. Suppression
requires the managed scheduled watch, no error/defer, interval no greater than
the configured bound and next_due no later than now+interval. Repeated parent
observations NEVER postpone that deadline or advance Work verification.

- New/unverified child: existing immediate discovery path; F1 still applies.
- VERIFIED, no positive canonical change since verification: leave Work exactly
  untouched; the durable watch guarantees another eligibility opportunity.
- Already eligible PENDING with adequate priority: coalesce the duplicate parent
  intent, preserving its complete bucket, timestamps and CAS version.
- IN_FLIGHT, RETRY_WAIT, BLOCKED, SUSPENDED, uncertain/changed or explicit HIGH:
  retain the accepted legacy merge/backoff/claimed-prefix behavior.

All external MergeSignal, formal Hint, manual operator, retry promotion and
EmitDuePoll semantics stay unchanged. The watched Work remains VERIFIED until
the real due poll/HINT creates a new epoch. Thus a Hint is immediately eligible,
not trapped behind a synthetic future Work.not_before. New processes read the
same persisted watches; no in-memory debounce or expiry reset exists.

F2 also opts the existing orchestrator into a bounded due-watch selector. Reserve
one due candidate from ordinary/operator policy and one from the managed revisit
cohort, then retain next_due/root/scope FIFO. Each cohort reads at most the
existing due-attempt cap+1 (at most6) candidates. This prevents a large due WARM
set from hiding a HOT root, and continuous HOT roots from hiding rolling checks.
F2 requires at least two due attempts per cycle (accepted runtime default5).
MoreDue and unchanged EmitDuePoll version CAS remain authoritative. Disabled F2
retains the original due FIFO exactly. Execution stays serial; no extra poll
attempt, source read or overlapping writer is introduced by selection.

## Bounds, failure, rollback and extension

The configured interval bounds revalidation ELIGIBILITY, not source completion:
the existing five due attempts/five executions per cycle, four-cycle burst,
root/priority fairness and source capacity still apply. Overload, failure and
disabled watches cannot promise a wall-clock SLA. At most one watch per root/scope
and1000 child decisions per parent; no mutable unbounded process-wide cache.

Errors roll back watch enrollment, child signals and parent completion together.
Preserve post-claim signals and stale-CAS refusal. No child omission/deletion
authority is added; virtual-only and failed sources retain existing guards.

Disabling the flag stops new enrollment/coalescing and restores parent discovery
merges; persisted watches remain legitimate P3 state and keep polling. This is
intentional: never silently delete recovery intent for rollback. Operators may
retire an exact managed watch using existing watch CAS, not a bulk SQL cleanup.
Changing the configured interval does not overwrite existing watches; an older
longer watch is not sufficient suppression proof until explicitly reconfigured.

Other collectors require their own namespace/proof contract and tests. Do not
turn provider mtime into a subtree revision. F3 capacity/prefetch,20k/50k and live
deployment are separate gates. Require PG18/HTTP/Kernel/Q5/Q8 late-file, nested,
Hint/restart/crash/fairness/F1 tests and measured duplicate/scan/queue rates.

Isolated measurements and limitations are recorded in
[F2 evidence](../p0/P0-134-F2-ISOLATED-RESULTS.md). Exact-head CI and Architect
acceptance are required independently; local success does not enable production.
