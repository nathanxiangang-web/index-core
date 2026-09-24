# Post-MVP Incremental Ingestion Blueprint

> Status: **D0 COMPLETE / P0 ACCEPTED / P1 ACCEPTED / P2 ACCEPTED / P3 ACCEPTED / P4 ONE-SHOT DIRTY EXECUTOR AUTHORIZED / PRODUCTION SCHEDULER NOT AUTHORIZED**
>
> Architecture tracking: #57
>
> Completed research: #59
>
> Scope: IndexCore infrastructure only. This is **not Gate 5 product architecture**.

## 1. Why this extension exists

The accepted Alpha already solves the most important product-path problem:

```text
user requests
    ↓
application / BFF
    ↓
IndexCore Query API
    ↓
PostgreSQL Canonical Inventory
```

Normal product reads no longer need to traverse the provider.

The remaining cost is the refresh path:

```text
Provider
   ↓
full traversal
   ↓
Collector
   ↓
Snapshot
   ↓
IndexCore
```

For large roots, repeatedly walking the full provider tree is wasteful and can increase latency,
provider API traffic, throttling risk, and real-cloud-drive exposure.

The original blueprint intentionally deferred true incremental work until:

- Snapshot correctness was stable;
- identity semantics were stable;
- safe reconcile was stable;
- crash recovery existed;
- PostgreSQL Store behavior was proven;
- external consumption was proven.

Gate 1–4 are now closed, so this is the correct time to design the deferred capability.

## 2. Discovery-first decision rule

No incremental implementation shape is selected yet.

D0 research has completed the evidence collection for this question:

```text
Can we discover real provider changes materially faster
than ordinary AList/OpenList cache visibility,
without repeating full traversal
and without rewriting mature provider drivers?
```

Candidate outcomes include:

```text
Mutation Hint
Native Delta / provider cursor
Scoped Refresh through OpenList/AList
Adaptive Polling
Hybrid strategy
Full-scan only
Stop / do not implement
```

Issue #59 produced the accepted capability report. The Architect selected **P0 Targeted Scoped Refresh** as the first bounded prototype. Execution is controlled by Issue #62 and `docs/architecture/INCREMENTAL-P0-SCOPED-REFRESH-PROTOTYPE.md`.

### Candidate future outcome — only if evidence supports native delta

For a provider that exposes a trustworthy native cursor or change feed:

```text
Provider
   ↓
committed provider cursor C0
   ↓
FetchChanges(C0)
   ↓
only changed objects / dirty scopes
   ↓
durable incremental batch
   ↓
existing Kernel safety + identity + reconcile
   ↓
Canonical Inventory + Journal
   ↓
commit provider cursor C1
```

Desired complexity:

```text
healthy incremental sync ≈ O(number of changes)
not O(total resources in root)
```

Full traversal remains available as a correctness fallback.

## 3. Non-negotiable rules

Incremental ingestion is an **optimization of acquisition**, not a second canonical-write path.

The following frozen rules remain unchanged:

1. Collectors never write Canonical Inventory directly.
2. Canonical truth is still owned by IndexCore.
3. Provider path is not identity.
4. Provider IDs are only strong evidence when their stability is qualified.
5. Missing is not automatically deleted.
6. Incomplete/partial evidence cannot silently advance removal state.
7. Canonical mutation + Journal + generation semantics remain Kernel-owned.
8. Same-root work remains serialized; different roots may run concurrently.
9. Cursor loss/gap/expiry must fail closed.
10. The public read-only Q1–Q9 contract does not need to change for the first incremental implementation.

## 4. Terminology

### Provider cursor

An opaque provider/runtime token meaning:

> Continue reading provider changes after this previously acknowledged point.

It is **operational ingestion state**, not a resource identifier and not a Query cursor.

Never reuse the term `cursor` without context in implementation docs:

- `provider_cursor` = Collector/runtime change-feed position;
- `query_cursor` = Q4/Q6/Q7 generation-bound pagination cursor.

### Change page

One provider response containing a bounded set of changes plus the next provider cursor.

### Incremental window

One or more persisted change pages coalesced into one bounded logical reconcile unit.

### Dirty scope

A path/provider object/subtree that is known to require verification, but whose current canonical
state cannot safely be inferred from the raw change event alone.

### Full resync required

A state indicating provider-cursor continuity can no longer be trusted and the root must re-establish
a baseline through the full-scan path.

## 4.1 Accepted P2 operational-state contract and P3 realization

P2 is ARCHITECT_ACCEPTED. The accepted operational model separates:

```text
ScopeWatchState
    recurring watch policy / cadence / due state

DirtyScopeWork
    claimed_* = signals owned by the current attempt
    pending_* = unclaimed/post-claim signals
```

P2 also freezes exact direct-child scope coverage, durable monotonic `signal_seq`, claim-scoped provenance, state-specific backoff semantics, atomic due-watch signal emission, and restart recovery.

P2 exit decision: **AUTHORIZE_STATE_PERSISTENCE_PROTOTYPE**.

P3 is defined by:

`docs/architecture/INCREMENTAL-P3-STATE-PERSISTENCE-PROTOTYPE.md`

P3 is ARCHITECT_ACCEPTED and merged at `c7c7091412a92c66e35d4d70f65a326ddc82a132`. It persisted the accepted operational model and proved its CAS/transaction/recovery semantics against real PostgreSQL.

P3 exit decision: **AUTHORIZE_ONE_SHOT_DIRTY_EXECUTOR_PROTOTYPE**.

P4 is defined by:

`docs/architecture/INCREMENTAL-P4-ONE-SHOT-DIRTY-EXECUTOR-PROTOTYPE.md`

P4 may prove exactly one persisted-work execution through the existing P0 `ScanScope` path. It does **not** authorize a scheduler, continuous executor, public trigger API, provider cursor, destructive delta, or Gate 5.

## 5. Capability model — research hypothesis, not accepted contract

The fields and interfaces in this section are **candidate design vocabulary only**.

Issue #59 must first establish what real providers/drivers actually expose. Do not implement this interface merely because it appears in the blueprint.

Incremental behavior, if later approved, must remain optional per Collector.

The existing Collector contract stays valid. Incremental support should be an optional extension,
conceptually:

```text
Collector
  ├─ FullScan(...)                  existing
  └─ IncrementalCollector           optional
       ├─ Capabilities(...)
       ├─ AcquireBootstrapCursor(...)
       └─ FetchChanges(from_cursor, limit)
```

A provider/adapter must declare capabilities explicitly.

Suggested capability fields:

```text
mode:
  NONE
  CURSOR_FEED
  CHANGE_NOTIFY
  DIRTY_SCOPE_ONLY

cursor_namespace
cursor_version

supports_bootstrap_cursor
supports_replay
gap_detectable
retention_known
supports_catch_up_until_current

stable_object_id_assurance

create_update_fidelity
move_rename_fidelity
delete_fidelity

supports_scope_verification
supports_positive_exhaustion
```

No adapter receives destructive authority merely because it says "supports delta".

## 6. Provider cursor model

Provider cursor should be stored as a structured opaque value:

```text
ProviderCursor
  kind
  namespace
  version
  value
```

Rules:

- `value` is opaque to Kernel/business logic;
- namespace/version prevent replaying a token through a different driver/protocol;
- cursor is scoped to one root + one Collector configuration identity;
- raw cursor values must not be exposed through public /v1;
- logs should use a hash/fingerprint, not the raw cursor;
- changing Collector config invalidates the committed cursor unless the adapter proves compatibility.

## 7. Collector configuration fingerprint

Each incremental state must be bound to a deterministic Collector configuration fingerprint.

Conceptually:

```text
fingerprint = digest(
  collector_kind
  + normalized non-secret config
  + cursor namespace/version
  + source scope identity
)
```

Secrets themselves must not be hashed into logs or persisted into config fingerprints.

If the effective source scope/config changes:

```text
cursor state => INVALID
sync state   => FULL_RESYNC_REQUIRED
```

Never keep using a cursor from a different source scope.

## 8. Durable state model

Incremental sync must survive crashes without losing provider changes.

Logical storage objects:

### ProviderSyncState

```text
root_id
collector_config_fingerprint
sync_mode
committed_provider_cursor
status
last_success_at
last_full_verification_at
last_error_code
last_error_at
```

Suggested status values:

```text
UNINITIALIZED
HEALTHY
CATCHING_UP
FULL_RESYNC_REQUIRED
PAUSED
```

### IncrementalBatch

```text
batch_id
root_id
from_cursor
to_cursor
batch_identity
state
change_count
dirty_scope_count
created_at
applied_at
cursor_committed_at
```

Suggested batch states:

```text
STAGED
PROCESSING
APPLIED
CURSOR_COMMITTED
FAILED
```

### IncrementalChange

Provider-neutral normalized observations belonging to a batch.

### Dirty / hot scope operational state

The earlier single `DirtyScope` sketch is superseded for P2 by two separate logical models:

- `ScopeWatchState` — recurring watch/cadence/next-due policy;
- `DirtyScopeWork` — one-shot coalesced verification intent with lost-wakeup-safe `signal_seq`.

See `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`.

These remain logical models. SQL migration and persistence implementation are not authorized by P2.

## 9. The core crash-safety rule

**Never advance the committed provider cursor before the fetched batch is durably staged.**

Required sequence:

```text
1. read committed cursor C0
2. provider FetchChanges(C0) => events + C1
3. durably persist batch B(C0 -> C1)
4. process B through normal Kernel-safe paths
5. mark B APPLIED
6. atomically:
     - commit ProviderSyncState.cursor = C1
     - mark B CURSOR_COMMITTED
```

Why this is safe:

### Crash before step 3

Nothing durable changed.

Restart from C0 and fetch again.

### Crash after step 3 but before canonical apply

The batch is already local.

Restart processes the staged batch without asking the provider again.

### Crash during/after canonical apply but before step 5

Reprocessing must be idempotent.

Derived Snapshot/batch identity must be deterministic so the existing duplicate/NOOP protections
can safely absorb replay.

### Crash after APPLIED but before cursor commit

Recovery sees:

```text
batch APPLIED
sync cursor still C0
batch.to_cursor = C1
```

It completes the cursor transition locally without refetching from the provider.

This gap is the reason the incremental batch itself must be durable.

## 10. Batch identity

Each staged batch needs deterministic identity.

Conceptually:

```text
batch_identity = digest(
  root_id
  + collector_config_fingerprint
  + from_cursor descriptor
  + to_cursor descriptor
  + canonicalized normalized changes
  + capability contract version
)
```

Provider event order must not accidentally change identity if order is not semantically meaningful.

If the provider has durable event IDs, retain them as evidence, but do not assume all providers do.

## 11. Change normalization

Provider-native events should normalize into a small provider-neutral set.

Candidate kinds:

```text
UPSERT
MOVE_HINT
DELETE_HINT
DIRTY_SCOPE
RESET_REQUIRED
```

### UPSERT

Meaning:

> The provider reports the current state of one object/resource strongly enough to construct a
> normal positive observation.

Can feed the existing Snapshot/identity/reconcile machinery.

### MOVE_HINT

Meaning:

> The provider says an object moved/renamed, but IndexCore still applies frozen identity rules.

If a stable provider object ID is strong evidence, the existing identity model may preserve
`resource_id`.

If evidence is weak, do not guess. Mark relevant scope dirty.

### DELETE_HINT

Meaning:

> The provider reports deletion/removal of an object.

**Initial incremental MVP must not directly turn this into canonical REMOVED.**

It creates removal/verification work only.

### DIRTY_SCOPE

Meaning:

> Something changed in this subtree/scope, but the feed does not provide enough current object state.

Schedule targeted verification.

### RESET_REQUIRED

Meaning:

> Cursor continuity is no longer trustworthy.

Immediately transition the root to `FULL_RESYNC_REQUIRED`.

## 12. Discovery Gate D0 — Change Discovery Capability Research — COMPLETE

Completed in Issue #59 and published as:

`docs/research/INCREMENTAL-CHANGE-DISCOVERY-REPORT.md`

Research must compare at least:

```text
A. Mutation Hint
B. Native Delta / provider cursor
C. Scoped Refresh via OpenList/AList
D. Adaptive Polling
E. Full Scan fallback
```

Primary scenario:

```text
real cloud drive already has a new object
AList/OpenList cache has not refreshed
goal: surface the object to IndexCore/Web earlier
without scanning the whole provider tree
```

The report must determine:

- whether 115 has an official/reliable native change mechanism;
- whether OpenList/AList can force-refresh one directory independently;
- whether that refresh actually reaches the real provider;
- request amplification and rate-limit/account risks;
- large-directory behavior;
- token/provider failure behavior;
- what mature tools already solve;
- what IndexCore would still need to own;
- realistic latency targets;
- UNKNOWNs requiring live tests.

D0 is complete. It introduced no production code, Store schema, IncrementalCollector interface, `sync` command, or provider cursor persistence.

### D0 exit decision

After the report, Architect chooses exactly one of:

```text
STOP
PROTOTYPE_MUTATION_HINT
PROTOTYPE_NATIVE_DELTA
PROTOTYPE_SCOPED_REFRESH
PROTOTYPE_HYBRID
KEEP_FULL_SCAN_ONLY
```

Only then may the later implementation sections become actionable.

## 13. Candidate Phase I1 — additive incremental MVP (not authorized)

The first implementation should optimize the common safe case:

```text
new object
updated object
rename/move with strong identity evidence
provider says scope changed
```

It should **not** attempt to solve destructive incremental removal yet.

Pipeline:

```text
native change feed
       ↓
durable batch
       ↓
normalize positive observations
       ↓
materialize bounded PARTIAL/additive Snapshot evidence
       ↓
existing admission / Kernel / reconcile
       ↓
Canonical Inventory + Journal
```

Important property:

> The first incremental implementation should require as little new Kernel semantics as possible.

The ideal first version changes Collector/runtime/Store plumbing while reusing the accepted Kernel.

## 14. Deletion policy

Deletion is where incremental systems most often become unsafe.

### Initial rule

```text
provider DELETE event
    ↓
DELETE_HINT
    ↓
DirtyScope / removal verification work
    ↓
NO immediate canonical deletion
```

This preserves:

```text
missing != deleted
provider event != canonical truth
```

### Future trusted-destructive delta

A later extension may allow explicit provider deletion to become strong removal evidence only if
the provider/adapter can prove, at minimum:

- stable object identity;
- durable ordered/replayable feed semantics;
- cursor continuity;
- gap/reset detection;
- deletion-event fidelity;
- feed retention behavior;
- bootstrap correctness;
- failure semantics.

That would be a **separate architecture review** because it changes removal-evidence semantics.

It is not part of I1.

## 15. Dirty-scope verification

Dirty scopes exist to avoid full-root rescans when a feed gives incomplete change detail.

Examples:

```text
directory changed
object delete hinted
event lacks full metadata
move destination known but source uncertain
provider emits "folder updated" only
```

The runtime should:

1. add/merge dirty scopes;
2. collapse child scopes only when the verifier coverage contract proves parent coverage subsumes the child; current P0 `ScanScope` is direct-child only, so parent/child scopes do **not** collapse;
3. verify a bounded subtree/scope;
4. materialize positive observations through the existing Snapshot path;
5. only use destructive conclusions if the scope-verification contract explicitly proves completeness.

If dirty scopes grow beyond a configured threshold:

```text
FULL_RESYNC_REQUIRED
```

This prevents "incremental" from degenerating into thousands of unsafe micro-scans.

## 16. Bootstrap: starting incremental mode safely

The dangerous race is:

```text
full scan starts
provider changes during scan
full scan ends
cursor starts too late
=> change lost
```

Preferred bootstrap where the provider supports it:

```text
1. acquire provider cursor C0 representing "changes after now"
2. run full baseline scan
3. consume delta from C0 until caught up
4. apply staged batches
5. commit final cursor
```

This bridges changes that occur while the baseline scan is running.

If a provider supports snapshot-at-cursor semantics, the adapter may use that stronger mechanism.

If the provider cannot establish a safe bootstrap point, native incremental mode remains disabled for
that adapter and IndexCore falls back to full scanning.

## 17. Cursor expiry, reset, gap, truncation

These cases must never look like "zero changes".

On any of:

```text
cursor expired
invalid cursor
provider reset
event retention exceeded
detected sequence gap
truncated feed
source identity changed
cursor namespace/version mismatch
```

the runtime does:

```text
mark FULL_RESYNC_REQUIRED
stop incremental cursor advancement
run/require full baseline recovery
bootstrap a new cursor safely
resume incremental mode
```

Fail closed.

## 18. No-change semantics

An empty change page only means "nothing to apply" when the adapter can prove cursor continuity.

It does **not** mean:

```text
the provider tree is a COMPLETE Snapshot
or
missing canonical rows should be removed
```

Incremental feed continuity and Snapshot completeness are related but distinct concepts.

## 19. Incremental window / generation control

Provider feeds may paginate heavily.

Applying every provider page as a separate canonical generation could create unnecessary generation churn
and more consumer `stale_cursor` responses.

The runtime should stage pages into a bounded **incremental window** until one limit is reached:

```text
max changes
max staged bytes
max wall time
caught up to current
```

The window is normalized/deduplicated into the current end state and then processed as one logical
reconcile unit where practical.

Rules:

- bounded memory/storage;
- deterministic normalization;
- canonical Journal records canonical results, not every raw provider event;
- raw provider ordering does not become canonical event ordering.

## 20. Same-root ordering

Existing rule remains:

```text
same root => serialized admission/application
different roots => bounded concurrency allowed
```

Incremental batches must enter the same absolute per-root ordering discipline.

No incremental worker may leapfrog earlier pending root work.

## 21. CLI shape

Do not silently change the meaning of existing `scan`.

Recommended future CLI:

### Force existing full traversal

```bash
indexcore scan --root <uuid>
```

Meaning remains:

> Run the configured Collector's full-scan path.

### Smart synchronization

```bash
indexcore sync --root <uuid>
```

Meaning:

```text
if healthy native incremental state exists:
    consume delta
else if full resync required:
    run safe baseline/bootstrap
else:
    use full scan fallback
```

Optional explicit mode:

```bash
indexcore sync --root <uuid> --mode auto
indexcore sync --root <uuid> --mode incremental
indexcore sync --root <uuid> --mode full
```

### Inspect sync state

```bash
indexcore root sync status --root <uuid>
```

Should show status/reason/timestamps/capability class, but not raw provider cursor secrets.

### Reset incremental state

Must require an explicit operator action and transition to `FULL_RESYNC_REQUIRED`.

## 22. HTTP API impact

First incremental implementation should add **no public write API**.

Q1–Q9 remain unchanged.

If a future product genuinely needs provider-sync operational status over HTTP, that should be proposed
as a separate read-only operational API change, not smuggled into Q9.

## 23. Rolling verification

Native delta is faster, but long-running systems need a drift backstop.

Incremental mode should support periodic verification policy:

```text
incremental feed
    ↓
normal fast path
    ↓
periodic full/scoped verification
    ↓
detect missed feed drift
```

Policy may be based on:

- time since last full verification;
- number of incremental windows;
- accumulated dirty scopes;
- provider-specific risk;
- manual operator request.

The blueprint does not freeze a universal interval.

## 24. Scheduling

Separate correctness from scheduling.

### First implementation

One-shot CLI-driven `indexcore sync --root` is sufficient to prove semantics.

### Later runtime scheduling

After one-shot sync is accepted, `serve` may gain bounded per-root scheduling.

Do not combine first incremental correctness work with a full scheduler rewrite.

## 25. Provider capability discovery

Before implementing a native adapter, document the source as:

```text
Provider / driver:
cursor/change-feed API:
cursor scope:
replay semantics:
retention:
gap/reset signal:
stable object ID:
create/update fidelity:
move/rename fidelity:
delete fidelity:
bootstrap cursor support:
dirty-scope listing:
rate limits:
known failure behavior:
```

Capability classification must remain:

```text
DIRECT
DERIVABLE
DRIVER_DEPENDENT
UNAVAILABLE
UNKNOWN
```

Do not claim that all rclone/AList/OpenList providers support native delta.

## 26. Security / privacy

Provider cursors can contain opaque provider metadata.

Rules:

- server-side only;
- never returned in public /v1;
- never placed in browser URLs;
- do not log raw cursor values;
- redact provider event payload fields that may contain secrets;
- credentials continue to use environment-secret references;
- operator diagnostics use cursor fingerprint/age/status, not raw token.

## 27. Observability

Minimum metrics/log fields:

```text
root_id
sync_mode
provider_capability_mode
batch_id
changes_fetched
changes_applied
dirty_scopes_created
dirty_scopes_verified
provider_pages
sync_duration
full_fallback_reason
cursor_age / lag when provider supports it
last_full_verification_at
resync_required_reason
```

Never log raw provider cursor.

## 28. Acceptance test matrix

The implementation gate must cover at least:

### Normal change cases

- create;
- metadata update;
- rename;
- move;
- repeated/no-change sync;
- duplicate provider event;
- provider event reordering where permitted.

### Safety cases

- delete event does not directly remove in I1;
- weak identity move does not guess;
- dirty scope collapses safely;
- high dirty-scope count triggers full resync;
- empty change page does not advance removal evidence.

### Cursor cases

- first bootstrap;
- normal C0 -> C1 -> C2;
- cursor expiry;
- cursor invalidation;
- provider reset;
- detected feed gap;
- changed Collector config fingerprint;
- cursor namespace/version mismatch.

### Crash points

- crash before batch stage;
- crash after stage before apply;
- crash during apply;
- crash after canonical apply before batch APPLIED marker;
- crash after APPLIED before cursor commit;
- restart with staged work;
- restart with APPLIED/uncommitted cursor.

### Concurrency

- same-root incremental work remains FIFO;
- same-root full scan cannot race past incremental work;
- different roots can sync concurrently within bounds.

### Regression

- existing full `scan` still works unchanged;
- Q1–Q9 behavior unchanged;
- Gate 2/3 Store/Kernel regression suite remains green;
- 20k baseline full-scan behavior does not regress unexpectedly.

## 29. Performance acceptance

At minimum prove:

1. On a 20k-resource root with a healthy native feed and ~10 changed objects, incremental sync does
   not enumerate all 20k provider objects.
2. Provider calls are proportional to delta pages + dirty verification, not root size.
3. A no-change incremental sync avoids full traversal.
4. Replayed batch is idempotent.
5. Cursor recovery after crash does not lose events.
6. Full fallback remains available and correct.
7. 100k resources should be an exploration target after 20k correctness is accepted.

No universal latency SLA is frozen in this architecture plan.

## 30. Candidate implementation phases — inactive until D0 exit decision

### I0 — Capability discovery + contract hardening

Deliver:

- optional IncrementalCollector interface design;
- provider capability matrix;
- provider cursor model;
- durable batch state-machine tests;
- bootstrap/fallback contract.

No provider-specific production code required yet.

### I1 — Additive native-delta MVP

Deliver:

- one real incremental-capable source/fixture;
- durable ProviderSyncState;
- durable IncrementalBatch;
- `indexcore sync --root`;
- positive/additive changes through existing Kernel path;
- dirty-scope creation;
- crash recovery;
- full fallback;
- no destructive delta.

### I2 — Dirty-scope verification

Deliver:

- bounded targeted verification;
- dirty-scope merge/collapse;
- fallback thresholds;
- rolling verification hooks.

### I3 — Real provider adapters

One provider at a time, only after capability proof.

Do not make a generic provider claim from one driver.

### I4 — Trusted destructive delta (optional)

Separate architecture review.

Only if a provider can prove the required delete/cursor guarantees.

## 31. What success could look like

Before:

```text
every refresh
   ↓
walk entire provider tree
   ↓
N resources of provider traffic
```

After, for a capable provider:

```text
first baseline
   ↓
full scan once
   ↓
store provider cursor
   ↓
next refreshes
   ↓
fetch only changes since cursor
   ↓
apply through normal Kernel
   ↓
periodic verification / fallback when required
```

The desired end state is:

> IndexCore remains conservative and trustworthy, while Provider traffic becomes proportional to actual change volume whenever the source can support it.

## 32. Current architecture decision

**Current decision: D0 research, P0 Targeted Scoped Refresh, and P1 Adaptive Hot-Scope Polling Feasibility are ARCHITECT_ACCEPTED. P1 exit decision is AUTHORIZE_DIRTY_SCOPE_STATE_DESIGN. P2 design is authorized; production incremental implementation remains unapproved.**

Accepted P0 evidence proved that, when a changed directory is known, one bounded OpenList `115 Open` `refresh=true` observation can surface a real out-of-band 115 change earlier than stale cache while preserving PARTIAL additive-safe semantics.

P1 proved that a **small bounded HOT scope set** can discover a real out-of-band 115 write within one configured 120-second-class interval while preserving P0 safety and explicit request/cycle budgets.

P1 is defined in `docs/architecture/INCREMENTAL-P1-HOT-SCOPE-POLLING-PROTOTYPE.md` and its accepted evidence is in `docs/incremental/P1-HOT-SCOPE-POLLING-RESULT.md`.

The next missing correctness layer is durable operational state before any production scheduler exists. P2 therefore designs separate `ScopeWatchState` and `DirtyScopeWork` models, restart-safe due/work semantics, and lost-wakeup-safe trigger coalescing. P2 is defined in `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`.

P2 does **not** authorize SQL migration, persistent dirty/watch tables, production scheduler, Mutation Hint API, native delta, provider cursor, production `sync`, destructive delta, or Gate 5.

The governing design principle remains:

> Any change-discovery optimization must reuse the existing Kernel safety path and must not become a second direct canonical-write mechanism.

### D0 research authorization — completed

D0 authorized and completed:

- official documentation research;
- source-code research;
- issue/bug/community evidence review;
- licensing/build-vs-buy analysis;
- request-path/rate-limit analysis;
- controlled non-production live tests when credentials/environment are explicitly provided.

Still not authorized:

- production Go code;
- SQL/migrations;
- new Store state;
- new `sync` CLI;
- provider cursor persistence;
- direct 115 integration;
- changes to Q1–Q9;
- destructive delta.

### Implementation authorization requires

1. Issue #59 research report completed; **DONE**
2. Architect decision on the discovery strategy; **DONE — P0 Scoped Refresh selected**
3. explicit prototype/implementation scope;
4. exact provider/tool selected;
5. exact safety/fallback contract;
6. only then: Store/interface/migration/test plan.
