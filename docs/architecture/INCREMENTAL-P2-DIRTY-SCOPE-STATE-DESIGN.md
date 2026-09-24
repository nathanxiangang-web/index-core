# Incremental P2 — Dirty / Hot Scope Durable State Design

> Status: **ARCHITECT AUTHORIZED — DESIGN ONLY**
>
> Parent: Issue #57
>
> P0 Targeted Scoped Refresh: **ARCHITECT_ACCEPTED**
>
> P1 Adaptive Hot-Scope Polling Feasibility: **ARCHITECT_ACCEPTED**
>
> P1 exit decision: **AUTHORIZE_DIRTY_SCOPE_STATE_DESIGN**
>
> Production scheduler / Store migration / persistent dirty-state implementation: **NOT AUTHORIZED**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P0 proved the safe refresh actuator. P1 proved that a small bounded HOT scope set can discover a real external 115 write within one configured interval.

P2 designs the durable operational state needed before any production scheduler exists.

The design must survive restart, combine polling and future Mutation Hint triggers, preserve P0 safety, and avoid creating a second Canonical-write path.

## 2. Core decision: WATCH policy and DIRTY work are separate models

Do not put cadence, timer state, Mutation Hint, retry state, and verification state into one ambiguous dirty-scope row.

```text
ScopeWatchState
  recurring policy / cadence
  when should this scope be checked again?

        ↓ due / explicit trigger

DirtyScopeWork
  coalesced one-shot verification intent
  this exact scope needs one safe verification pass

        ↓

existing scan.Service.ScanScope
        ↓
existing admission / Kernel / reconcile
```

This separation is required because a HOT scope may be watched while clean, a hint may dirty an unwatched scope, failures must not erase watch policy, and successful verification clears work rather than long-lived policy.

## 3. Scope identity and coverage

Logical key:

```text
(root_id, scope_key)
```

`scope_key` is a normalized root-relative directory path. `/` is valid. `..`, path escape, duplicate-slash ambiguity, and provider-specific opaque identifiers are not allowed in the Kernel-facing key.

Current accepted P0 coverage is:

```text
EXACT_DIRECT_CHILDREN
recursive = false
```

Therefore `/a` does **not** cover `/a/b`.

P2 must not collapse parent and child scopes merely because their paths overlap. Parent-child collapse is allowed only if a future verifier has an explicit coverage contract proving subsumption.

## 4. Logical model A — ScopeWatchState

Purpose: persist recurring observation policy and next-due state.

Conceptual fields:

```text
root_id
scope_key
watch_state
cadence_class
effective_interval
source_set
priority_class
last_due_at
last_attempt_started_at
last_attempt_finished_at
last_success_at
next_due_at
consecutive_failures
last_error_class
deferred_until
created_at
updated_at
version
```

Candidate watch states:

```text
HOT
WARM
COLD
DISABLED
```

P1's 120 seconds is feasibility evidence, not a universal frozen HOT interval. The effective interval must be explicit policy.

Restart rule: if `next_due_at <= now`, the watch is due after restart. Future scheduling must derive due work from persisted state rather than relying on an in-memory timer event.

## 5. Logical model B — DirtyScopeWork

Purpose: represent one coalesced requirement to verify an exact scope through the accepted safe path.

Conceptual fields:

```text
root_id
scope_key
work_state
signal_seq
claimed_signal_seq
reason_set
source_set
priority_class
first_seen_at
last_seen_at
not_before
attempt_count
consecutive_failures
last_attempt_started_at
last_attempt_finished_at
last_error_class
last_verified_at
last_verified_signal_seq
created_at
updated_at
version
```

Candidate work states:

```text
PENDING
IN_FLIGHT
RETRY_WAIT
BLOCKED
VERIFIED
SUSPENDED
```

P2 does not freeze SQL retention strategy. VERIFIED may be retained as current-state metadata or compacted later.

## 6. Trigger source and reason stay separate

Candidate sources:

```text
POLL_SCHEDULE
MUTATION_HINT
PROVIDER_EVENT
MANUAL_OPERATOR
RECOVERY
FULL_VERIFY_BACKSTOP
```

Candidate reasons:

```text
POSSIBLE_CHANGE
DELETE_HINT
MOVE_UNCERTAIN
METADATA_UNCERTAIN
MANUAL_VERIFY
DRIFT_VERIFY
RETRY
```

Multiple sources/reasons may merge while work is outstanding. New provenance must not overwrite earlier provenance.

Mutation Hint creates immediate dirty work; it does not automatically make a scope permanently HOT.

## 7. Coalescing invariant — signal_seq prevents lost wakeups

There is at most one active logical DirtyScopeWork per `(root_id, scope_key)`.

Every new trigger merges into that item and increments durable `signal_seq`.

When an attempt starts:

```text
claimed_signal_seq = signal_seq
```

After successful verification:

```text
if current signal_seq == claimed_signal_seq:
    transition may become VERIFIED
else:
    a newer signal arrived during the attempt
    remain/re-enter PENDING
```

Example:

```text
attempt starts for signal 7
new hint arrives -> signal 8
attempt 7 succeeds
signal 8 must still be processed
```

This lost-wakeup protection is a required P2 invariant.

## 8. Verification success is not the same as Canonical mutation

A successful fresh P0 `ScanScope` observation can legitimately return `Mutated=true` or `Mutated=false`.

Both may satisfy the claimed dirty signal if the exact scope was successfully refreshed and safely admitted/applied, and no newer signal arrived.

Therefore `Mutated=false` does not mean verification failure.

## 9. Polling becomes a trigger producer

Future conceptual flow:

```text
ScopeWatchState next_due_at <= now
        ↓
merge DirtyScopeWork
source=POLL_SCHEDULE
reason=POSSIBLE_CHANGE
        ↓
DirtyScopeWork executor
        ↓
ScanScope
```

Watch scheduling never writes Canonical state directly.

Future Mutation Hint, manual verification, and provider events must enter the same DirtyScopeWork path.

## 10. Failure and defer semantics

Provider-neutral failure classes:

```text
TRANSIENT_PROVIDER
THROTTLED
AUTH_OR_PERMISSION
SCOPE_TOO_LARGE
INVALID_SCOPE
ROOT_INACTIVE
CONFIG_INVALID
INTERNAL
```

Suggested transitions:

- transient/throttle -> `RETRY_WAIT` with bounded future eligibility;
- auth/config -> `BLOCKED`; no tight automatic retry;
- scope too large -> `BLOCKED` or separately authorized fallback; never silently raise max_entries;
- root inactive -> `SUSPENDED`.

Budget defer is **not** a provider failure. If a cycle cannot select eligible work due to budget, attempt/failure counters do not increment.

## 11. Crash and restart recovery

Current architecture remains one active writer daemon per DB. P2 does not authorize distributed leases or HA.

Crash rules:

- before claim: PENDING remains PENDING;
- after claim: stale IN_FLIGHT is recovered to PENDING/RETRY_WAIT on single-writer startup;
- after provider success but before VERIFIED: replay through P0 is safe and signal_seq prevents losing a newer trigger;
- after VERIFIED persisted: no replay unless a newer signal exists.

A future attempt token may protect against stale completion, but P2 does not authorize distributed lease machinery.

## 12. Same-root ordering remains frozen

Dirty verification must enter the existing same-root serialized admission/application discipline.

No dirty executor may bypass earlier root work or become a second Canonical-write lane.

Different roots may eventually execute concurrently only within the existing bounded model.

## 13. Full scan interaction

A later full scan does not automatically clear dirty work just because it happened later in wall-clock time.

Other verification can satisfy dirty work only if it proves equal-or-stronger scope coverage, freshness relative to the signal, successful admission/application, and no newer signal.

Current P0 root-level direct-child refresh does not verify nested scopes.

## 14. Root lifecycle

Routine watch scheduling executes only for ACTIVE roots.

For non-ACTIVE roots:

- recurring watch selection stops;
- outstanding state is retained;
- work becomes SUSPENDED rather than silently deleted;
- no automatic ScanScope runs.

P2 does not change existing root lifecycle semantics.

## 15. HOT / WARM / COLD lifecycle

P2 permits future adaptive policy but freezes no thresholds.

Candidate transitions:

```text
COLD -> HOT      operator/policy
WARM -> HOT      observed change
HOT -> WARM      sustained successful no-change
WARM -> COLD     long no-change
any -> DISABLED  operator
```

Watch demotion must never discard pending DirtyScopeWork.

## 16. Priority and fairness

Candidate priority classes are `URGENT`, `HIGH`, `NORMAL`, `LOW`.

Typical intent:

- manual/hinted write -> HIGH;
- HOT poll due -> NORMAL;
- rolling verification -> LOW.

Within a class, future selection should be deterministic using fields such as `eligible_at`, `first_seen_at`, and `scope_key`. Exact aging policy is deferred.

## 17. Security and abuse bounds

Future implementation must support:

- root-bound normalized paths;
- maximum watched scopes per root;
- maximum pending dirty scopes per root;
- duplicate trigger coalescing;
- no arbitrary path escape;
- no unbounded retry loop;
- no trigger that directly creates destructive Canonical behavior.

If pending work becomes excessive, a future policy may escalate to `FULL_RESYNC_REQUIRED` or operator attention, but P2 does not implement that.

## 18. Observability model

Minimum future operational fields:

```text
root_id
scope_key
watch_state
effective_interval
next_due_at
last_success_at
work_state
signal_seq
reason_set
source_set
attempt_count
consecutive_failures
last_error_class
not_before
last_verified_at
budget_defer_count
verification_duration
canonical_mutated
```

Operational scheduler state is not Canonical Journal state and is not exposed through Q1-Q9.

## 19. Required P2 design deliverables

The Worker design task must produce:

1. exact ScopeWatchState field semantics and invariants;
2. exact DirtyScopeWork field semantics and invariants;
3. watch/work state-machine diagrams;
4. signal coalescing and crash-recovery rules;
5. conceptual schema keys/indexes/version/CAS plan, without SQL migration;
6. trigger contract for polling, Mutation Hint, manual action, and future provider events;
7. failure/defer matrix;
8. interaction matrix with P0 ScanScope, P1 polling, full scan, same-root FIFO, and Q1-Q9;
9. deterministic future test matrix.

## 20. Required invariants

P2 is not acceptable unless all are explicit:

1. Watch policy and dirty work are separate state.
2. One logical active work item per `(root_id, scope_key)`.
3. New triggers coalesce and increment signal_seq.
4. Verification cannot clear a signal that arrived after its claim.
5. Success is based on fresh scope verification, not `Mutated=true`.
6. Budget defer is not provider failure.
7. Current P0 scope coverage is direct-child only; no unsafe parent collapse.
8. Restart loses neither due watches nor pending work.
9. No new direct Canonical-write path exists.
10. Dirty signals never create removal evidence directly.
11. Same-root ordering remains frozen.
12. Q1-Q9 remain unchanged.
13. Production scheduling remains unimplemented during P2.

## 21. Explicitly not authorized

Do not implement:

- SQL migration;
- dirty/watch Store tables;
- production scheduler or serve loop;
- persistent queue executor;
- Mutation Hint HTTP/API endpoint;
- production sync CLI;
- native delta/provider cursor;
- direct 115 client;
- destructive removal;
- Gate 5.

## 22. P2 acceptance criteria

P2 may be Architect-accepted only if the design:

1. separates recurring watch policy from one-shot verification work;
2. defines exact root-bound scope identity and direct-child coverage;
3. defines restart-safe due semantics;
4. defines lost-wakeup-safe signal_seq coalescing;
5. defines success independent of Canonical mutation;
6. defines provider-neutral failure/defer states;
7. defines single-writer crash recovery without inventing HA;
8. preserves frozen same-root ordering and P0 safety;
9. gives a migration-ready logical schema sketch without shipping SQL;
10. provides a deterministic future test matrix;
11. leaves Q1-Q9 and frozen contracts unchanged;
12. keeps production scheduler and persistence implementation unauthorized.

## 23. P2 exit decision

After P2 design review, Architect chooses exactly one:

```text
STOP
AUTHORIZE_STATE_PERSISTENCE_PROTOTYPE
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_ONE_SHOT_DIRTY_EXECUTOR_PROTOTYPE
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No option is pre-authorized.

## 24. Current authorization

```text
P0 scoped refresh                     ACCEPTED
P1 hot-scope polling feasibility      ACCEPTED
P2 dirty-scope state design           AUTHORIZED
production polling/scheduler          NOT AUTHORIZED
persistent dirty-scope implementation NOT AUTHORIZED
DB migration                          NOT AUTHORIZED
Mutation Hint production integration  NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```