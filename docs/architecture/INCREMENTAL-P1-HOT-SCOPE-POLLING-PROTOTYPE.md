# Incremental P1 — Adaptive Hot-Scope Polling Feasibility Prototype

> Status: **ARCHITECT AUTHORIZED — PROTOTYPE ONLY**
>
> Parent: Issue #57
>
> Predecessor: Issue #62 / PR #64 — P0 Targeted Scoped Refresh — **ARCHITECT_ACCEPTED**
>
> Production incremental sync: **NOT AUTHORIZED**
>
> Gate 5: **NOT AUTHORIZED**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Why P1 exists

P0 proved the **actuator**:

```text
known changed directory
        ↓
OpenList /api/fs/list refresh=true
        ↓
one coherent direct-child observation
        ↓
PARTIAL additive-safe reconcile
        ↓
Canonical Inventory becomes current earlier
```

The remaining problem is the **detector**:

> When an external user writes directly to real 115, IndexCore does not know which
> directory changed.

D0 established that, as of 2026-09-24, the audited public 115 Open API exposes no
native change feed, durable change cursor, webhook, or global "changes since" API.
Mutation Hint therefore cannot cover arbitrary external writes by itself.

P1 asks one bounded question:

> Can a small, explicitly bounded set of "hot" directories be force-refreshed at
> approximately 1–2 minute cadence with acceptable provider cost and failure behavior,
> so external writes become visible materially earlier than normal OpenList cache expiry?

This is a **feasibility prototype**, not a production scheduler.

## 2. Architect decision

Selected next step:

```text
P1_ADAPTIVE_HOT_SCOPE_POLLING_FEASIBILITY
```

Not selected yet:

- production scheduler;
- persistent DirtyScope queue;
- durable poll state / DB schema;
- public trigger API;
- production `sync` CLI;
- native delta / provider cursor;
- direct 115 client;
- destructive delta;
- Gate 5.

Why this is before Mutation Hint integration:

- Mutation Hint is useful only when the system already knows a write occurred.
- The original external-upload scenario has **no hint**.
- P0 already proved that once a scope is known, refresh works.
- The next uncertainty is whether bounded polling is operationally viable.

## 3. P1 architecture boundary

Prototype flow:

```text
operator-provided hot scope set
        ↓
test-only polling harness
        ↓
bounded due-scope selection
        ↓
existing scan.Service.ScanScope(...)
        ↓
OpenList 115 Open refresh=true
        ↓
PARTIAL additive-safe reconcile
        ↓
metrics / request-budget evidence
```

The harness may maintain **in-memory prototype state only**.

No new production Store schema is authorized.

## 4. Hot-scope semantics

A hot scope is an exact directory path already known to belong to the configured
IndexCore root.

Example:

```text
/
/downloads
/downloads/incoming
/media/new
```

Important:

- `ScanScope` observes **direct children only**.
- Polling `/downloads` does not detect a new file created inside
  `/downloads/incoming` unless `/downloads/incoming` itself is also polled.
- Parent/child hot scopes therefore MUST NOT be automatically collapsed merely
  because one path contains another.
- P1 does not claim complete coverage of the provider tree.

A directory outside the selected hot set may remain stale until:

- another explicit scope refresh;
- a later Mutation Hint;
- a future polling policy;
- or full-scan fallback.

## 5. Prototype cadence model

The prototype may use three in-memory cadence classes:

```text
HOT    target <= 120 s
WARM   target <= 10 min
COLD   not periodically refreshed by P1
```

Only **HOT** is required for live acceptance.

The 120-second figure is a **prototype target**, not a production SLA.

The harness may move a scope between HOT/WARM based on observed change/no-change
streaks, but the exact heuristic is not frozen architecture.

Minimum acceptable behavior:

- a HOT scope is never polled more frequently than its configured minimum interval;
- no tight loop on success or error;
- an error does not trigger unbounded immediate retry;
- one scope failure does not authorize root-wide destructive conclusions.

## 6. Request-budget contract

P1 must be bounded by explicit prototype limits.

At minimum the harness must support:

```text
max_hot_scopes
max_scopes_per_cycle
max_cycle_wall_time
max_entries_per_scope
minimum_scope_interval
```

Recommended first live values:

```text
max_hot_scopes        = 5
max_scopes_per_cycle  = 5
max_cycle_wall_time   = 60 s
max_entries_per_scope = 1000
minimum_scope_interval = 120 s
```

These are prototype defaults, not frozen production policy.

If a scope exceeds the existing P0 `maxEntries` bound or the cycle wall-time budget
is exhausted:

```text
fail/stop that scope attempt
record budget exhaustion
do not widen the request
do not infer absence/removal
do not silently switch to recursive traversal
```

## 7. Provider-cost accounting

OpenList exposes one HTTP `/api/fs/list refresh=true` observation per P0 scope
attempt, but the 115 Open driver may internally issue multiple provider list pages.

For P1 evidence record:

- scope path;
- direct-child `total`;
- configured 115 Open `page_size`;
- derived provider-page estimate:
  `ceil(total / page_size)`;
- canonical OpenList request latency;
- response bytes;
- HTTP status;
- 401/403/429/throttle/provider errors;
- retries (expected zero in the prototype unless explicitly tested);
- whole-cycle wall time.

The derived provider-page estimate is **not** a measured provider request count and
must be labelled as such.

Do not claim that one OpenList request equals one 115 API request.

## 8. Adaptive behavior allowed in P1

P1 may prototype a small policy such as:

```text
change observed
    -> keep/promote scope HOT

N consecutive no-change polls
    -> demote to WARM

new operator hint
    -> promote scope HOT immediately

budget pressure
    -> defer lower-priority scope
```

This policy must remain inside the test/probe harness.

No durable priority queue or scheduler service is authorized.

## 9. Safety contract

Every poll must reuse the accepted P0 path:

```text
TraversalStatus                = PARTIAL
CompletenessFlag               = PARTIAL
FreshnessEvidence              = FRESH_REFRESHED
CollectorCompletenessAssurance = WEAK_FAILURE_VISIBILITY
ProviderIdentityAssurance      = UNVERIFIED
SkippedScopes                  = UNKNOWN
```

Consequences remain unchanged:

- positive observations may add/update;
- absence does not create removal evidence;
- empty scoped result removes nothing;
- no root-wide completeness claim;
- no direct provider mutation;
- no stronger identity inference;
- Kernel semantics remain unchanged.

## 10. Failure behavior

Prototype must fail closed.

### 403 / permission failure

- surface the error;
- do not retry in a tight loop;
- do not mutate Canonical state from the failed observation.

### 429 / throttle

- record it explicitly;
- stop/defer further polling for that live experiment cycle;
- do not implement production backoff policy in P1.

### timeout / provider error

- record scope + elapsed time;
- leave Canonical state untouched for that failed attempt;
- continue only if the bounded harness policy explicitly permits another independent scope.

### scope too large

- existing `ScanScope` max-entry guard remains authoritative;
- mark the scope unsuitable for this polling profile;
- do not increase the cap merely to make the test pass.

## 11. Prototype implementation shape

Preferred implementation:

```text
internal/runtime/scan/scoped_poll_live_test.go
or equivalent gated test-only package

static/in-memory hot scope definitions
        ↓
poll cycle helper
        ↓
scan.Service.ScanScope
```

Allowed:

- gated live integration test;
- test-only transparent proxy, as in P0;
- in-memory cadence/priority state;
- deterministic fake-clock tests for cadence selection if useful;
- small non-production 115 Open live run.

Not allowed:

- production daemon loop;
- changes to `serve`;
- production scheduler package;
- DB migration;
- persistent dirty-scope table;
- public HTTP trigger;
- production CLI;
- direct 115 API calls from IndexCore;
- changes to Q1–Q9;
- destructive reconciliation.

## 12. Required deterministic tests

At minimum:

### Selection/budget

- only due HOT scopes are selected;
- `max_scopes_per_cycle` is honored;
- minimum interval is honored;
- wall-time budget stops further selections;
- one slow/error scope does not cause unbounded loop;
- parent/child scopes are not incorrectly collapsed.

### Reconcile safety

Reuse P0 behavior and prove through the polling harness:

- one changed hot scope adds exactly its positive change;
- unchanged scopes remain NOOP/additive-safe;
- unrelated resources remain;
- no removal evidence is advanced;
- same-root FIFO remains intact.

### Regression

- existing full `Scan()` unchanged;
- P0 `ScanScope()` unchanged;
- rclone unchanged;
- Q1–Q9 unchanged;
- no schema/migration;
- `go test -p 1 ./...` green.

## 13. Required live P1 experiment

Use the same class of environment accepted by P0:

- non-production OpenList;
- community `115 Open` driver;
- official 115 Open API;
- dedicated non-admin service identity;
- official 115 channel for out-of-band writes.

Prepare 3–5 small directories as hot scopes.

Minimum decisive scenario:

```text
Phase A
  establish IndexCore baseline for all hot scopes

Cycle 0
  refresh=false confirms current cached state

T1
  upload one new file OUT-OF-BAND into exactly one HOT directory

before next P1 poll
  refresh=false for that scope still does NOT expose the file

P1 polling cycle
  refresh each due hot scope once, within budget

Result
  changed scope discovers + reconciles the file
  unchanged hot scopes remain safe
  no unrelated resource loss
```

Record:

- number of hot scopes;
- cadence;
- per-scope direct-child totals;
- per-scope canonical HTTP latency/status/bytes;
- derived provider-page estimate;
- cycle wall time;
- changed-scope trigger-to-Q3/Q4/Q6 visibility latency;
- any 403/429/throttle/provider errors;
- whether any cycle exceeded budget.

## 14. P1 acceptance criteria

P1 may be Architect-accepted only if all are true:

1. external change in a HOT scope is detected without a Mutation Hint;
2. detection occurs within one configured polling interval;
3. no root-wide traversal occurs;
4. each due scope uses the accepted single canonical P0 observation;
5. request/cycle budgets are enforced;
6. unchanged scopes do not create canonical mutation/removal evidence;
7. changed scope remains additive-safe;
8. 115 Open request behavior is measured and does not show unacceptable throttle/risk in the bounded test;
9. existing P0/full scan/Q1–Q9 remain unchanged;
10. frozen contracts remain unchanged.

## 15. Explicit limitations

Even if P1 passes, it will **not** prove:

- arbitrary provider-wide changes are discovered;
- every directory can be polled every two minutes;
- 120 seconds is a universal SLA;
- large directories are cheap;
- polling replaces full verification;
- deletion can be trusted;
- a production scheduler is safe;
- persistent dirty-scope state is unnecessary.

P1 only answers whether a **small bounded hot set** is viable.

## 16. Exit decision

After P1 evidence, Architect chooses exactly one:

```text
STOP_POLLING
KEEP_P0_MANUAL_ONLY
AUTHORIZE_MUTATION_HINT_INTEGRATION
AUTHORIZE_HYBRID_HINT_PLUS_HOT_POLLING
AUTHORIZE_DIRTY_SCOPE_STATE_DESIGN
RESEARCH_FURTHER
```

No option is pre-authorized by this document.

## 17. Current authorization

**Only the P1 feasibility prototype described above is authorized.**

Production incremental sync remains **NOT AUTHORIZED**.
