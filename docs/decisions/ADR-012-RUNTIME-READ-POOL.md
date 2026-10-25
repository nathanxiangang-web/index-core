# ADR012 candidate: bounded runtime sibling reads

Issue138 PR-B. PR142 prototype accepted for integration and merged6e0a128.
This contract is a review candidate, NOT production enablement or a source SLA.

## Modules and dependency direction

- `sourcebudget`: provider-neutral, process-local HTTP pacing and cooldown.
  One shared manager owns global,Root and source-endpoint-group budgets; every
  incremental source/auth/identity/topology request crosses its transport.
  It never reads credentials or provider bodies, writes canonical data or retries.
- `scan`: immutable Service clone accepts the shared HTTP manager. Existing
  collectors retain parsing/coverage responsibility. Same-root source failures
  identify failed slots, so canceled peers are not falsely BLOCKED as bad input.
- `incrementalexec`: bounded pool cycle reuses existing selection/claim/completion
  primitives. At most8 claims/results, at mostN=1/2/4/8 readers, one commit lane.
  Contiguous same-Root/same-parent cohorts use accepted frozen lineage; root,
  mixed-parent and unsupported cohorts use the accepted fence-v1 serial seam.
- `incrementalorch` materializes watches as before; `incrementalruntime` owns one
  event loop, retry promotion and interrupted-root recovery. `app/config` owns
  explicit opt-in composition. No Kernel, migration, source write or new queue.

## Defaults, order and fairness

Reader default1 keeps accepted serial execution.2/4/8 explicitly select the pool;
only that opt-in extends the execute-item ceiling from5 to8. Default watch and
cycle wall caps remain unchanged. N is a maximum, not a throughput guarantee.
Selection is serial, never callbacks racing ClaimWork. Existing aged/first-
discovery/FIFO planning is reused; one bounded round-robin Root slot prevents a
large earlier Root from monopolizing every cycle. Fairness cursor is process-local,
not a durable cross-restart service guarantee. No priority row rewrite.
Only contiguous sibling claims are batched, so per-Root selected order is retained.
Real DRAFT facts, original source time/base generation and Stage1/Stage2 fences
are unchanged. All source actors join before any completion or actor shutdown.

## Request policy and extension rules

All requests, not merely fs/list, consume no-burst minimum spacing at global,
Root and source-group levels. Group defaults to normalized HTTP origin; optional
adapter `request_budget_group` can bind multiple endpoints to one operator group.
It is NOT inferred115-account identity, a native upstream quota or an across-
process distributed ledger. Distinct clients/roots cannot bypass a shared group.
State maps are bounded; capacity exhaustion fails closed, never evicts a live
cooldown. No cross-origin redirects or credentials are logged. Budget waits are
context-cancelable and recheck cooldown after wake; canceled waiters own no slot.
HTTP429 respects Retry-After and exponential capped local backoff; API-envelope
429 uses typed Collector classification. A cooldown denies requests without I/O.
Default backoff45s aligns with the existing THROTTLED retry floor; local exponential
cap15m does not shorten a longer Retry-After. A qualified success after cooldown
resets escalation, never an uncompleted/overlapping request. Known cooled-down
Roots are skipped BEFORE claim via bounded read-only planning (up to8 Root probes
per selection and a rotating cursor), without consuming attempt/failure budget.
Cool down one endpoint without starving an independently available Root.
Real provider rates and production flags still require separate operator GO.

## Failure and recovery

No inline source retry. Failed slot keeps its typed classification; canceled or
unobserved peers become transient cohort-abort retries, not AUTH/INVALID failures.
Throttling/stale lineage/cohort abort temporarily selects serial fallback for
future work. A frozen failed claim is NEVER reused or rebased. Durable retry and
new claim epoch must precede fresh source I/O. Retry eligibility honors cooldown.
DB/unknown commit/completion errors fail closed, not swallowed as provider faults.
Parent/cycle cancellation stops admission, joins readers and reports every owned
interrupted Root for existing recovery. Writer ownership remains held until join.
Shutdown needs no source request, synthetic verification or SQL state repair.

## Required proof and rollout

Default/invalid config, actual budgeted HTTP starts, shared-group enforcement,
429/Retry-After, canceled wait, redirect denial, bounded maps, no tight retry;
real isolated PG18 claim/completion, Q5/Q8 order, mixed roots/parents, fairness,
typed peer failure, stale fences, late Hint, cancellation/drain/restart and serial
fallback. Compare actual app-composed runtime N1/2/4/8 under the SAME disclosed
fake-source budgets and resources; distinguish20k/50k SQL seed from traversal.
Record every incomplete/NO-GO gate.30..60m churn/OS kill/canary require real proof,
not a rebranding of the finite prototype matrix. Keep134/138 OPEN. No production
deploy, real provider I/O, unrelated126/133 or Watch lifecycle changes in this PR.
