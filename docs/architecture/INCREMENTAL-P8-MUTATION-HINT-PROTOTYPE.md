# Incremental P8 — Mutation Hint Ingestion Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P7 Manual Incremental Command Prototype — Issue #85 / PR #86 — **ARCHITECT_ACCEPTED**
>
> P7 merge: `8ae9b01c6a8d2c4b8797077b280e795f71dc712b`
>
> P7 exit decision: **AUTHORIZE_MUTATION_HINT_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P2/P3 already reserved the provider-neutral provenance value:

```text
MUTATION_HINT
```

and P3 already implements the durable merge primitive:

```text
DirtySignal
  -> Store.MergeSignal
  -> DirtyScopeWork
```

P8 answers one narrow question:

> Can a trusted caller that already knows "something changed under this directory scope" durably add that knowledge into the existing DirtyScopeWork state machine, without provider traversal, without a second writer process, without a second executor path, and without gaining deletion/removal authority?

Desired shape:

```text
trusted in-process caller
        ↓
MutationHintService.IngestOne
        ↓
validate canonical scope + hint semantics
        ↓
DirtySignal{
  Source: MUTATION_HINT,
  ...
}
        ↓
existing Store.MergeSignal
        ↓
existing DirtyScopeWork state machine
        ↓
existing P5/P4/P0 execution path later
```

P8 is **ingress only**.

It does not execute work itself.

## 2. Single-writer boundary

P2/P3 freeze:

> exactly one active writer process owns operational-state transitions for one database.

P8 therefore does **not** add:

- a second writer process;
- a standalone hint-writing CLI;
- a public HTTP write API;
- a local sidecar writer;
- a second advisory-lock owner.

The P8 service is an **in-process primitive** intended to be called only by trusted code already running inside the active single-writer runtime boundary.

It does not acquire the global writer advisory lock itself.

Future phases may decide how a production runtime exposes this primitive to trusted external systems. That transport decision is not part of P8.

## 3. Existing state model is reused unchanged

P8 reuses the already accepted enum:

```go
state.SourceMutationHint
```

No new TriggerSource is needed.

P8 also reuses the existing TriggerReason values.

No migration is authorized.

No DirtyScopeWork schema change is authorized.

No new event-deduplication table is authorized.

## 4. Preferred package/API

Preferred package:

```text
internal/runtime/incrementalhint/
```

Preferred conceptual API:

```go
type Store interface {
    MergeSignal(context.Context, state.DirtySignal) (state.DirtyScopeWork, error)
}

type Request struct {
    RootID   string
    ScopeKey string
    Reason   state.TriggerReason
}

type Service struct {
    store Store
    now   func() time.Time
}

func New(store Store, now func() time.Time) (*Service, error)

func (s *Service) IngestOne(
    ctx context.Context,
    req Request,
) (state.DirtyScopeWork, error)
```

Exact names may vary. Semantics may not.

One call ingests exactly one scope hint.

No batch API in P8.

## 5. Hint payload contract

P8 request contains exactly:

```text
root_id
scope_key
reason
```

### 5.1 Root ID

`root_id` identifies the existing IndexCore root.

P8 does not create roots.

Missing/nonexistent root fails closed through the existing Store contract.

### 5.2 Scope key

`scope_key` is the exact directory scope to verify.

It uses the frozen P3 canonical scope-key syntax:

```text
/        valid root scope
/a       valid
/a/b     valid
/a/      invalid
//a      invalid
/a/../b  invalid
\        invalid separator
```

P8 deliberately does **not** use `path.Clean` or silently reinterpret malformed paths.

Rule:

> caller must supply the canonical root-absolute directory scope.

P8 validates with the existing `state.ValidateScopeKey`.

A file path is not automatically converted to its parent directory.

The caller is responsible for choosing the directory whose direct children should be verified.

### 5.3 Reason

Allowed Mutation Hint reasons:

```text
POSSIBLE_CHANGE
DELETE_HINT
MOVE_UNCERTAIN
METADATA_UNCERTAIN
```

If Reason is empty, default to:

```text
POSSIBLE_CHANGE
```

P8 rejects these non-hint reasons:

```text
MANUAL_VERIFY
DRIFT_VERIFY
RETRY
```

Reason is provenance only.

In particular:

> `DELETE_HINT` does not prove deletion and does not authorize removal.

## 6. P8-owned signal fields

P8, not the caller, owns these DirtySignal fields.

### Source

Always:

```text
MUTATION_HINT
```

Caller cannot override source.

### Priority

Always:

```text
HIGH
```

Rationale:

- a trusted caller already knows a mutation occurred;
- hint work may be preferred over routine NORMAL polling;
- priority remains advisory only;
- it never overrides same-root ordering or safety semantics.

P8 does not expose caller-controlled priority in this prototype.

### SeenAt

Always:

```text
accepted_at = now()
```

P8 does not accept caller-provided timestamps.

This prevents caller clock skew from distorting:

- pending_first_seen_at;
- last_seen_at;
- fairness ordering.

### NotBefore

Always:

```text
accepted_at
```

not nil.

This makes a newly created / VERIFIED / ordinary PENDING hint immediately eligible.

It also lets the existing PENDING min-merge semantics bring a future eligibility barrier forward to the hint acceptance time.

Critically, the accepted Store state machine still preserves existing barriers for:

```text
RETRY_WAIT
BLOCKED
SUSPENDED
```

because their merge semantics do not replace pending_not_before.

P8 must not bypass those state-specific protections.

## 7. Exact DirtySignal mapping

For one accepted request:

```go
acceptedAt := now()

sig := state.DirtySignal{
    RootID:    req.RootID,
    ScopeKey:  req.ScopeKey,
    Source:    state.SourceMutationHint,
    Reason:    normalizedReason,
    Priority:  state.PriorityHigh,
    SeenAt:    acceptedAt,
    NotBefore: &acceptedAt,
}
```

Then call:

```go
store.MergeSignal(ctx, sig)
```

exactly once from the service layer.

P8 must not copy `MergeSignal` logic.

## 8. Duplicate / replay semantics

P8 does **not** implement external event-id deduplication.

Repeated identical hints are treated as repeated signals:

```text
same root + same scope + same reason
        ↓
same DirtyScopeWork row
        ↓
signal_seq increments once per accepted call
        ↓
pending provenance sets remain de-duplicated
```

Therefore P8 is:

- row-coalescing;
- lost-wakeup-safe;
- safe for repeated at-least-once notifications;

but **not event-id idempotent**.

Do not claim duplicate hints leave `signal_seq` unchanged.

A future ingress transport may add an explicit idempotency-key design if needed. P8 does not.

## 9. Existing work-state semantics remain authoritative

P8 does not define new transitions.

It relies entirely on existing `MergeSignal`.

### No existing row

ACTIVE root:

```text
-> PENDING
-> signal_seq = 1
-> MUTATION_HINT provenance
```

non-ACTIVE root:

```text
-> SUSPENDED
```

### PENDING

```text
signal_seq++
pending provenance union
priority max
hint may make pending eligibility earlier
state remains PENDING
```

### IN_FLIGHT

```text
signal_seq++
claimed_* unchanged
hint goes only to post-claim pending_*
current attempt remains responsible only for old claimed prefix
```

If the old attempt succeeds:

```text
new hint survives
-> work returns PENDING
```

### VERIFIED

```text
new hint opens a new epoch
signal_seq++
pending_* rebuilt from hint
-> PENDING for ACTIVE root
```

### RETRY_WAIT

```text
signal_seq++
provenance merges
existing retry barrier unchanged
state remains RETRY_WAIT
```

### BLOCKED

```text
signal_seq++
provenance merges
state remains BLOCKED
```

Hint must never auto-repair or clear a block.

### SUSPENDED

```text
signal_seq++
provenance merges
state remains SUSPENDED
```

Hint must never activate a root.

## 10. Watch-state interaction

A Mutation Hint is **not watch policy**.

P8 must not:

- create a ScopeWatchState;
- change HOT/WARM/COLD/DISABLED;
- change cadence;
- change next_due_at;
- change watch priority;
- increment watch poll-attempt/success counters directly.

Existing P3/P4 claim-scoped attribution remains authoritative.

If an execution claim contains only:

```text
MUTATION_HINT
```

and not:

```text
POLL_SCHEDULE
```

then watch polling health bookkeeping must not be updated by that execution.

If a pending epoch coalesces both POLL_SCHEDULE and MUTATION_HINT before claim, existing claim-scoped attribution rules remain unchanged.

## 11. No provider traversal during ingestion

P8 IngestOne performs:

```text
validation
-> MergeSignal
-> return
```

It must not:

- call AList/OpenList;
- call rclone;
- call ScanScope;
- inspect provider directory existence;
- resolve file paths;
- fetch metadata;
- verify whether the hint is true.

Therefore a syntactically valid but provider-nonexistent scope may be accepted as dirty work.

The existing executor later decides whether scoped verification produces:

```text
INVALID_SCOPE
CONFIG_INVALID
provider failure
success
```

This keeps provider access in the existing execution lane.

## 12. No Canonical authority

Mutation Hint is evidence that verification is needed.

It is never Canonical truth.

P8 cannot write:

- CanonicalResource;
- generation;
- Journal;
- removal evidence;
- missing_since;
- Snapshot;
- admission state.

The only mutation is through `DirtyScopeWork`.

## 13. DELETE_HINT is non-destructive

A `DELETE_HINT` means:

> caller suspects a deletion and requests verification.

It does **not** mean:

> delete the Canonical resource.

After DELETE_HINT execution, the accepted P0 scoped path still produces:

```text
TraversalStatus = PARTIAL
CompletenessFlag = PARTIAL
```

Therefore:

```text
scoped absence
!= complete absence evidence
!= removal authority
```

A resource omitted from the scoped refresh remains protected by the frozen Canonical removal rules.

## 14. Trust boundary

P8 accepts calls only from trusted in-process code inside the active writer runtime.

P8 does not authenticate users because it exposes no public transport.

Do not wire P8 into:

- public HTTP;
- browser requests;
- unauthenticated localhost HTTP;
- message queue consumers;
- webhook handlers;
- CLI that writes concurrently with serve.

Those require a separate transport/trust architecture decision.

## 15. Preferred result

P8 may return the existing:

```go
state.DirtyScopeWork
```

directly.

No new persistent Hint history table is authorized.

No separate HintReceipt table is authorized.

No raw provider payload is persisted.

The durable provenance is the accepted DirtyScopeWork source/reason/signal_seq state.

## 16. Expected implementation surface

Preferred production files:

```text
internal/runtime/incrementalhint/service.go
```

Tests:

```text
internal/runtime/incrementalhint/service_test.go
internal/runtime/incrementalhint/integration_test.go
```

Result report:

```text
docs/incremental/P8-MUTATION-HINT-RESULT.md
```

Small changes to documentation are allowed.

Not expected / not authorized production changes:

```text
internal/store/postgres/**
internal/incremental/state/**
internal/runtime/incrementalexec/**
internal/runtime/incrementalorch/**
internal/runtime/scan/**
internal/runtime/app/**
cmd/**
internal/transport/**
internal/kernel/**
internal/query/**
internal/domain/**
internal/collector/**
internal/store/postgres/migrations/**
```

If implementation seems to require these changes, stop for Architect review.

## 17. Required tests

### 17.1 Request validation

Prove:

- empty/malformed scope rejected before Store call;
- trailing slash rejected;
- duplicate slash rejected;
- dot/dotdot rejected;
- backslash rejected;
- canonical `/` and `/a/b` accepted;
- invalid/non-hint reason rejected;
- empty reason defaults to POSSIBLE_CHANGE.

### 17.2 Signal mapping

With fake Store, prove exactly one `MergeSignal` call with:

```text
Source    = MUTATION_HINT
Priority  = HIGH
SeenAt    = injected clock time
NotBefore = injected clock time
Reason    = normalized request reason
```

Caller cannot override source/priority/time/not_before.

### 17.3 Active-root first hint

Real PostgreSQL.

Create ACTIVE root, no work row.

Ingest hint.

Assert:

- one DirtyScopeWork row;
- state PENDING;
- signal_seq=1;
- pending_source_set={MUTATION_HINT};
- pending_reason_set expected;
- pending_priority=HIGH;
- pending_first_seen_at=accepted_at;
- pending_not_before=accepted_at;
- last_seen_at=accepted_at.

### 17.4 Duplicate/replay coalescing

Same request twice.

Assert:

- still one row;
- signal_seq increments twice;
- source set contains MUTATION_HINT once;
- reason set de-duplicated;
- first_seen remains earliest;
- last_seen advances to second acceptance time;
- no second work row.

This test must explicitly document that P8 is coalescing, not external-event-id idempotent.

### 17.5 VERIFIED reopens

Prepare VERIFIED work.

Ingest hint.

Assert:

- state PENDING;
- signal_seq++;
- new epoch pending provenance is only the new hint;
- pending_first_seen_at/last_seen_at reset to new accepted time;
- prior claimed provenance is absent.

### 17.6 IN_FLIGHT lost-wakeup safety

Prepare/claim an existing work item.

Ingest hint while IN_FLIGHT.

Assert:

- claimed_* remains unchanged;
- signal_seq++;
- hint appears only in pending_*;
- current completion success returns Work to PENDING;
- hint survives for the next attempt.

### 17.7 RETRY_WAIT barrier preserved

Prepare RETRY_WAIT with future pending_not_before.

Ingest hint.

Assert:

- signal_seq++;
- hint provenance merges;
- state remains RETRY_WAIT;
- pending_not_before unchanged;
- no auto RetryReady.

### 17.8 BLOCKED preserved

Prepare BLOCKED.

Ingest hint.

Assert:

- signal_seq++;
- state remains BLOCKED;
- no repair/transition;
- hint provenance retained.

### 17.9 SUSPENDED/inactive root preserved

For non-ACTIVE root:

- first hint may create SUSPENDED work through existing MergeSignal behavior;
- subsequent hint remains SUSPENDED;
- no root lifecycle change.

### 17.10 Watch policy untouched

Create a watch for the same scope.

Capture its full scheduling/health fields and version.

Ingest hint only.

Assert:

- watch row is not created/changed by P8;
- existing watch state/cadence/next_due/health/version remain unchanged.

### 17.11 Hint-only execution does not count as poll success

Real PostgreSQL + real P5/P4 + httptest AList/OpenList.

Create ACTIVE root + watch, but do **not** emit POLL_SCHEDULE.

Ingest MUTATION_HINT.

Execute through accepted P5/P4.

Assert:

- claim provenance contains MUTATION_HINT;
- resource verifies normally;
- Work VERIFIED;
- watch polling attempt/success bookkeeping remains unchanged because POLL_SCHEDULE was not claimed.

### 17.12 Coalesced poll + hint attribution

Prepare same pending epoch with:

```text
POLL_SCHEDULE
MUTATION_HINT
```

before claim.

Execute once.

Assert:

- one claim can cover the coalesced epoch;
- claimed_source_set includes both sources;
- existing watch attribution occurs because POLL_SCHEDULE was actually claimed;
- no extra execution is created merely because two sources existed.

### 17.13 No provider I/O during ingestion

Use fake Store/service seam.

Assert IngestOne has no provider/scanner dependency.

The production package must not import:

```text
collector
scan
incrementalexec
incrementalorch
transport
```

### 17.14 Real hint -> accepted execution path

Real PostgreSQL + real P5/P4 + httptest AList/OpenList:

```text
P8 hint
  -> DirtyScopeWork PENDING
  -> P5
  -> P4
  -> ScanScope
  -> one refresh=true
  -> PARTIAL Snapshot
  -> Canonical resource visible
  -> Work VERIFIED
```

Prove provider request count <= selected items and no alternate write lane.

### 17.15 DELETE_HINT does not remove on scoped absence

Seed canonical `/old.txt` as PRESENT through an accepted ingestion path.

Then:

```text
P8 DELETE_HINT on scope /
  -> accepted P5/P4 scoped refresh
  -> provider response omits /old.txt
```

Assert after execution:

- `/old.txt` remains protected by the accepted Canonical truth;
- no removal evidence is created from this PARTIAL scoped absence;
- missing_since remains null;
- complete-missing corroboration does not advance.

### 17.16 Invalid provider scope is deferred to executor

Ingest a syntactically valid scope that does not exist at provider.

Assert:

- hint ingestion itself succeeds;
- no provider request happens at ingress;
- accepted executor later classifies the scoped verification according to existing INVALID_SCOPE behavior;
- no new P8 error class is introduced.

### 17.17 Regression

Required:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
```

Existing P3/P4/P5/P6/P7 tests stay green.

## 18. Acceptance criteria

P8 passes only if:

1. one call creates/merges exactly one Mutation Hint signal;
2. Source is always MUTATION_HINT;
3. caller cannot set source/time/not_before/priority;
4. canonical scope syntax is strictly validated without silent path reinterpretation;
5. only the four accepted mutation reasons are allowed;
6. no migration/state enum change is needed;
7. Store.MergeSignal remains the only durable merge path;
8. duplicate hints coalesce into one row while signal_seq still increments per call;
9. IN_FLIGHT late hints survive older completion;
10. RETRY_WAIT/BLOCKED/SUSPENDED barriers remain intact;
11. watches are not policy-mutated by hint ingestion;
12. hint-only execution does not falsely count as poll-schedule health;
13. no provider traversal occurs during ingestion;
14. P8 does not execute work directly;
15. real hint -> accepted P5/P4/P0 execution proof passes;
16. DELETE_HINT has no destructive authority;
17. no second writer process/CLI/HTTP write API is introduced;
18. no Canonical/Query/Kernel/Journal contract changes occur.

## 19. P8 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_TRUSTED_HINT_TRANSPORT_DESIGN
AUTHORIZE_PRODUCTION_SCHEDULER_DESIGN
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 20. Current authorization

```text
P0 scoped refresh                     ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility      ARCHITECT_ACCEPTED
P2 durable scope-state design         ARCHITECT_ACCEPTED
P3 state persistence prototype        ARCHITECT_ACCEPTED
P4 one-shot dirty executor            ARCHITECT_ACCEPTED
P5 bounded executor loop              ARCHITECT_ACCEPTED
P6 scheduler orchestration            ARCHITECT_ACCEPTED
P7 manual incremental command         ARCHITECT_ACCEPTED
P8 mutation hint ingestion            AUTHORIZED AFTER THIS PLAN MERGES

standalone hint writer CLI             NOT AUTHORIZED
public HTTP write API                  NOT AUTHORIZED
production polling scheduler           NOT AUTHORIZED
ticker/cadence daemon                  NOT AUTHORIZED
continuous executor service            NOT AUTHORIZED
native delta/provider cursor           NOT AUTHORIZED
direct 115 integration                 NOT AUTHORIZED
destructive delta/removal              NOT AUTHORIZED
Gate 5                                 NOT AUTHORIZED
```
