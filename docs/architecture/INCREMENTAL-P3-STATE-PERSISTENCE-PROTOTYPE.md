# Incremental P3 — State Persistence Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE**
>
> Parent: Issue #57
>
> Predecessor: P2 Dirty / Hot Scope Durable State Design — Issue #69 / PR #70 — **ARCHITECT_ACCEPTED**
>
> P2 exit decision: **AUTHORIZE_STATE_PERSISTENCE_PROTOTYPE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P2 froze the operational state model. P3 is the first implementation phase and has one narrow question:

> Can the accepted `ScopeWatchState` / `DirtyScopeWork` contract be represented in PostgreSQL and mutated through deterministic CAS/transaction primitives, surviving concurrency and restart without changing Kernel, Query, Collector, or P0 behavior?

P3 **does authorize** an additive migration and Store code.

P3 **does not authorize** the production scheduler or executor that would continuously consume this state.

## 2. P3 authority boundary

### Authorized

- one additive SQL-first migration: `0005_incremental_scope_state.sql`;
- provider-neutral operational state types/validation;
- PostgreSQL persistence for `ScopeWatchState`;
- PostgreSQL persistence for `DirtyScopeWork`;
- CAS/version mutations;
- atomic due-watch -> dirty-work transaction primitive;
- claim / success / failure / defer / retry-ready / suspend-resume persistence transitions;
- stale `IN_FLIGHT` restart-recovery primitive;
- real PostgreSQL deterministic/concurrency tests;
- migration/schema compatibility tests;
- documentation necessary to explain the prototype.

### Not authorized

- production polling scheduler;
- long-running dirty-work executor;
- calling `ScanScope` from a new worker loop;
- `serve` integration;
- command/CLI surface for sync or scheduling;
- Mutation Hint HTTP/API ingress;
- provider-event ingress;
- native delta/provider cursor;
- direct 115 client;
- destructive delta/removal;
- Q1–Q9 changes;
- Canonical Inventory / Journal schema or semantics changes;
- multi-daemon HA, distributed leases, fencing;
- Gate 5.

P3 may persist **operational state only**. It must not create a second Canonical write lane.

## 3. Existing persistence architecture to reuse

P3 must extend, not replace, the current Store realization:

```text
SQL-first migrations
  internal/store/postgres/migrations/*.sql
        ↓
postgres.Migrate()
        ↓
pgx/v5
        ↓
PostgreSQL 18
```

Current migration sequence ends at:

```text
0001_init.sql
0002_removal_independence.sql
0003_missing_last_snapshot.sql
0004_root_adapter_config.sql
```

P3 adds:

```text
0005_incremental_scope_state.sql
```

The existing rules remain:

- migrations embedded and applied lexically;
- each migration in its own transaction;
- `schema_migrations` records versions;
- `SchemaStatus` rejects missing or future/unknown versions;
- PostgreSQL behavior is tested against real PostgreSQL via `testutil.Pool`;
- single active write daemon remains enforced by the existing advisory WriterLock when runtime uses it.

P3 must not introduce ORM, Redis, SQLite, in-memory persistence, or a second migration system.

## 4. Code ownership

Operational state is **not Canonical Domain**.

Preferred shape:

```text
internal/incremental/state/
    model.go
    validate.go
    scope.go

internal/store/postgres/
    incremental_state.go
    incremental_watch.go
    incremental_work.go
    incremental_recovery.go
    *_test.go

internal/store/postgres/migrations/
    0005_incremental_scope_state.sql
```

Exact file split may vary, but these layering rules are mandatory:

- `internal/incremental/state` contains provider-neutral operational types, enums, validation, set normalization, and scope-key validation;
- `internal/store/postgres` owns SQL realization;
- `internal/domain` must not gain scheduler/work-state concepts;
- `internal/runtime/scan` production code must not change in P3;
- no transport/API package changes.

No generic runtime scheduler interface is required yet. That interface belongs to a later phase when a consumer of the state actually exists.

## 5. Scope-key storage contract

Physical and logical scope identity is:

```text
(root_id, scope_key)
```

P3 freezes the stored scope-key representation:

- root scope is exactly `/`;
- non-root scope begins with `/`;
- no trailing slash except `/`;
- no empty component / `//`;
- no `.` or `..` component;
- no backslash path separator;
- exact directory scope only;
- no parent/child collapse.

Invalid scope keys fail before SQL mutation.

P3 must not import runtime `scan` just to reuse its path helper; operational scope validation must live in the provider-neutral incremental state package.

## 6. Physical set representation

P3 resolves the P2 schema-sketch choice.

Use PostgreSQL:

```text
text[]
```

for source/reason sets.

Rules:

- Go-side representation is normalized before every write;
- values are de-duplicated;
- values are stored in deterministic sorted order;
- no NULL elements;
- only the closed P2 enum values are accepted;
- set equality is order-independent;
- a Store read returns normalized sets.

Do not add PostgreSQL custom ENUM types in P3. Use constrained text/text[] so future enum additions do not require type-rewrite migrations.

## 7. Migration 0005 — additive and inert

`0005_incremental_scope_state.sql` creates exactly two operational tables:

```text
index_scope_watch_state
index_dirty_scope_work
```

Both reference `index_root(root_id)`.

No existing Canonical table is altered.

No trigger invokes provider work.

Applying migration 0005 is therefore inert until a later authorized runtime component explicitly uses the Store methods.

### 7.1 index_scope_watch_state

Required physical fields, equivalent names allowed only when semantics are identical:

```text
root_id                    uuid
scope_key                  text

watch_state                text
cadence_class              text
effective_interval_seconds bigint NULL

source_set                 text[] NOT NULL
priority_class             text

last_due_at                timestamptz NULL
last_attempt_started_at    timestamptz NULL
last_attempt_finished_at   timestamptz NULL
last_success_at            timestamptz NULL
next_due_at                timestamptz NULL

consecutive_failures       bigint NOT NULL
last_error_class           text NULL
deferred_until             timestamptz NULL

created_at                 timestamptz NOT NULL
updated_at                 timestamptz NOT NULL
version                    bigint NOT NULL
```

Primary key:

```text
(root_id, scope_key)
```

Required constraints:

- watch_state in HOT/WARM/COLD/DISABLED;
- priority in URGENT/HIGH/NORMAL/LOW;
- version >= 1;
- consecutive_failures >= 0;
- HOT/WARM require positive `effective_interval_seconds` and non-NULL `next_due_at`;
- COLD/DISABLED require NULL effective interval and NULL next_due_at;
- source_set contains only accepted watch-policy sources.

Required due-selection index:

- optimized for HOT/WARM + `next_due_at`;
- root and scope identity available for deterministic selection;
- `deferred_until` remains a predicate, not a new scheduling truth.

### 7.2 index_dirty_scope_work

Required fields:

```text
root_id                    uuid
scope_key                  text

work_state                 text
signal_seq                 bigint

claimed_signal_seq         bigint NULL
claimed_source_set         text[] NULL
claimed_reason_set         text[] NULL
claimed_priority           text NULL
claimed_first_seen_at      timestamptz NULL

pending_source_set         text[] NOT NULL
pending_reason_set         text[] NOT NULL
pending_priority           text NULL
pending_first_seen_at      timestamptz NULL
pending_not_before         timestamptz NULL

last_seen_at               timestamptz NOT NULL
attempt_count              bigint NOT NULL
consecutive_failures       bigint NOT NULL
last_attempt_started_at    timestamptz NULL
last_attempt_finished_at   timestamptz NULL
last_error_class           text NULL
last_verified_at           timestamptz NULL
last_verified_signal_seq   bigint NULL

created_at                 timestamptz NOT NULL
updated_at                 timestamptz NOT NULL
version                    bigint NOT NULL
```

Primary key:

```text
(root_id, scope_key)
```

Required constraints include the P2 bucket invariants:

- work_state in PENDING/IN_FLIGHT/VERIFIED/RETRY_WAIT/BLOCKED/SUSPENDED;
- signal_seq >= 1;
- version >= 1;
- attempt_count/consecutive_failures >= 0;
- claimed_signal_seq <= signal_seq;
- last_verified_signal_seq <= signal_seq when non-NULL;
- claimed_* is present as one group iff IN_FLIGHT;
- VERIFIED => claim absent and pending bucket empty;
- PENDING/RETRY_WAIT/BLOCKED/SUSPENDED => claim absent and pending source bucket non-empty;
- IN_FLIGHT => claimed source bucket non-empty; pending bucket may be empty or contain only post-claim signals;
- empty pending source bucket => empty pending reason set + NULL pending priority/first_seen/not_before;
- non-empty pending source bucket => pending_first_seen_at non-NULL.

Required eligible-work index uses the accepted pending fields, not obsolete pre-P2 names.

## 8. Time and deterministic-test rule

Persistence code must not hide scheduling semantics behind uncontrolled `time.Now()`.

Mutation methods that need logical time accept an explicit `now time.Time` or equivalent clock input.

Tests use fixed times.

Database `created_at` defaults may exist, but state-machine decisions must be based on explicit testable time.

## 9. Version/CAS rule

Version starts at 1 on row creation.

Every logical state mutation increments version exactly once.

### Optimistic mutations

For operations acting on a previously-read row:

```text
UPDATE ...
SET ..., version = version + 1
WHERE root_id = ?
  AND scope_key = ?
  AND version = expected
```

0 affected rows => P3 state CAS conflict.

Do not reuse generation `ErrCASConflict` ambiguously. Introduce a distinct operational-state error, e.g.:

```text
ErrStateCASConflict
```

### Bounded internal retry

Only naturally mergeable operations may internally re-read/retry:

- dirty signal merge;
- completion CAS when the same claim remains valid, as frozen by P2.

Retry must be bounded and must not sleep-spin.

Claim / policy update / explicit transition with a stale expected version fails and requires caller re-read.

## 10. Required Store primitives

Exact Go signatures may vary; behavior may not.

### Watch

- create/get watch;
- CAS update watch policy/state;
- list due watches at explicit `now` with limit;
- due listing only returns ACTIVE-root HOT/WARM watches not blocked by `deferred_until`.

### Dirty signal merge

A provider-neutral Signal contains at least:

```text
root_id
scope_key
source
reason
priority
not_before
seen_at
```

Merge rules are exactly P2:

- absent row => insert PENDING or SUSPENDED according to root lifecycle, signal_seq=1;
- PENDING => pending set union; pending_not_before=min_nonnull;
- IN_FLIGHT => only post-claim pending bucket changes; claimed_* immutable; min_nonnull within post-claim bucket;
- VERIFIED => new epoch rebuild;
- RETRY_WAIT => merge provenance but do not cancel/extend existing backoff;
- BLOCKED/SUSPENDED => merge provenance without changing eligibility or state;
- each trigger increments signal_seq exactly once.

Concurrent merge tests must prove no lost increments and one physical row.

### Atomic due-poll emission

Provide one Store primitive equivalent to:

```text
EmitDuePoll(root_id, scope_key, expected_watch_version, now)
```

One PostgreSQL transaction must:

1. re-read/recheck the watch;
2. require ACTIVE root;
3. require HOT/WARM;
4. require due and not deferred;
5. merge exactly one POLL_SCHEDULE / POSSIBLE_CHANGE signal into DirtyScopeWork;
6. advance watch last_due_at/next_due_at;
7. increment watch version;
8. commit both work merge and watch advance together.

Failure/rollback exposes neither half.

Two concurrent calls using the same watch version must not produce two poll signals.

Use existing PostgreSQL transaction style; no distributed transaction layer.

### Claim

Claim is an explicit Store primitive.

It must:

- require PENDING;
- require eligible `pending_not_before`;
- recheck root ACTIVE;
- if root is no longer ACTIVE, fail closed and preserve the work by transitioning it to SUSPENDED;
- snapshot all pending claim fields into claimed_*;
- clear pending_* atomically;
- increment attempt_count;
- set last_attempt_started_at;
- increment version;
- if claimed_source_set contains POLL_SCHEDULE, update the matching watch's last_attempt_started_at in the same transaction.

No ScanScope call occurs in P3.

### Success completion

Completion is a persistence primitive only.

Given the claim watermark and expected/current version:

- stale/non-owning claim must not write;
- CAS conflict re-reads as frozen by P2;
- successful claimed prefix always records:
  - last_attempt_finished_at;
  - last_verified_at;
  - last_verified_signal_seq;
  - consecutive_failures=0;
  - last_error_class=NULL;
- no newer pending => VERIFIED;
- newer pending => PENDING, keeping only post-claim pending_*;
- release claimed_*;
- if claimed_source_set contained POLL_SCHEDULE, update Watch success/finish/failure reset in the same transaction.

### Failure completion

P3 accepts only the P2 failure classes.

Store maps them to the frozen state transition:

```text
TRANSIENT_PROVIDER / THROTTLED / INTERNAL
    -> RETRY_WAIT

AUTH_OR_PERMISSION / SCOPE_TOO_LARGE / INVALID_SCOPE / CONFIG_INVALID
    -> BLOCKED

ROOT_INACTIVE
    -> SUSPENDED
```

Failure:

- re-coalesces claimed_* + post-claim pending_*;
- preserves oldest first_seen;
- clears claimed_*;
- provider-class failures increment Work failure counters;
- ROOT_INACTIVE is not a provider failure;
- RETRY_WAIT applies bounded caller-supplied retry eligibility and never makes it earlier than the required backoff;
- Watch failure/finish counters change only if POLL_SCHEDULE belonged to claimed_source_set.

### Budget defer

Provide an explicit CAS persistence primitive for a PENDING item:

- may move pending_not_before later;
- does not increment attempt_count;
- does not increment consecutive_failures;
- does not set last_error_class.

### Retry-ready / resume

Persistence must support the accepted state-machine transitions without a scheduler:

- due RETRY_WAIT -> PENDING;
- SUSPENDED -> PENDING only after root is ACTIVE;
- BLOCKED -> PENDING only via an explicit repair transition.

These are primitives/tests only in P3.

## 11. Restart recovery primitive

P3 implements restart recovery of stale persisted work but does not wire it into a daemon startup loop.

A recovery primitive must:

- find persisted IN_FLIGHT rows;
- atomically re-coalesce claimed_* into pending_*;
- preserve signal_seq;
- preserve oldest first_seen age;
- clear claimed_*;
- not count crash as provider failure;
- ACTIVE root => PENDING;
- non-ACTIVE root => SUSPENDED;
- preserve post-claim signals that arrived before crash.

It must be safe to run repeatedly: after the first recovery, a second run is a no-op for that row.

No distributed lease/fencing fields are added.

## 12. Root and Canonical transaction boundaries

P3 operational transactions are separate from Canonical reconcile transactions.

This is intentional.

DirtyScopeWork never writes:

- CanonicalResource;
- generation;
- Journal;
- removal evidence;
- Snapshot;
- admission.

A later executor will:

```text
claim operational work
    ↓
commit claim
    ↓
run existing ScanScope / Kernel path
    ↓
complete operational work
```

P3 does not implement that executor.

Same-root Canonical FIFO is therefore not modified.

## 13. Migration compatibility / rollback consequence

Adding migration 0005 changes the required schema set for the new binary.

Existing compatibility behavior remains frozen:

- new binary + pre-0005 DB => schema missing until `indexcore migrate`;
- migration runs transactionally and is additive;
- old binary against a DB containing 0005 will see an unknown/future migration and must fail closed, as existing Gate-3 schema compatibility requires.

Do **not** weaken SchemaStatus to make rollback convenient.

P3 migration tests must document this consequence.

No down-migration framework is introduced.

## 14. Required PostgreSQL test matrix

Real PostgreSQL 18 is mandatory. In-memory database substitutes are not accepted.

### Migration

- empty DB -> 0001..0005 reproducible;
- migrate rerun idempotent;
- upgrade from a pre-0005 (0001..0004) schema succeeds and preserves existing data;
- 0005 creates only the two operational tables/indexes/constraints;
- old/future schema compatibility behavior remains unchanged.

### Watch

- create/read round-trip;
- stale version CAS fails with zero partial write;
- HOT/WARM due selection;
- COLD/DISABLED exclusion;
- defer exclusion;
- inactive-root exclusion;
- deterministic limit/order.

### Signal merge

- first trigger creates one row at signal_seq=1;
- duplicate/concurrent triggers produce one row and exact signal_seq count;
- every work_state merge rule matches P2;
- VERIFIED opens a clean pending epoch;
- RETRY_WAIT merge neither cancels nor extends backoff;
- invalid scope/source/reason/priority rejected.

### Atomic due-poll

- work merge + watch advance both commit;
- injected failure rolls both back;
- two concurrent emit attempts from the same watch version produce exactly one POLL_SCHEDULE signal.

### Claim/provenance

- atomic pending -> claimed move;
- bucket constraints hold after commit;
- post-claim signal never modifies claimed_*;
- claim on inactive root suspends/fails closed;
- claimed POLL_SCHEDULE updates Watch attempt-start only.

### Success

- normal success -> VERIFIED;
- signal 7 claimed + signal 8 merged -> success 7 => PENDING with only signal-8 pending provenance;
- partial success writes last_verified_signal_seq=7;
- stale claim cannot complete;
- claimed poll success updates Watch; hint-only success does not.

### Failure

- transient/throttle/internal -> RETRY_WAIT;
- auth/oversize/config/invalid -> BLOCKED;
- root inactive -> SUSPENDED without provider-failure increment;
- claimed + post-claim provenance re-coalesces;
- first_seen age preserved;
- claimed poll failure updates Watch; pre-claim poll signal does not retroactively blame earlier hint attempt.

### Recovery

- stale IN_FLIGHT ACTIVE -> PENDING;
- stale IN_FLIGHT non-ACTIVE -> SUSPENDED;
- claim re-coalesced; post-claim signals preserved;
- signal_seq unchanged;
- crash not counted as provider failure;
- repeated recovery is idempotent.

### DB constraints

Direct invalid SQL attempts must demonstrate that critical bucket/state invariants are enforced by the database where specified, not only by Go validation.

### Regression

Required:

```text
gofmt
go vet ./...
go test -p1 ./...
```

No live 115/OpenList test is required in P3.

## 15. Explicit code-change guard

P3 Worker PR may change only the persistence/state prototype surface plus docs/tests.

Expected areas:

```text
internal/incremental/state/**
internal/store/postgres/incremental_*.go
internal/store/postgres/incremental_*_test.go
internal/store/postgres/migrations/0005_incremental_scope_state.sql
internal/store/postgres/migrate*_test.go
internal/store/postgres/schema*_test.go
testutil/**                 # only if a narrowly necessary PG test helper change is justified
docs/**                     # P3 report / state update
```

Production changes to these areas are not authorized:

```text
cmd/**
internal/runtime/app/**
internal/runtime/worker/**
internal/runtime/scan/**    # production files
internal/transport/**
internal/query/**
internal/kernel/**
internal/domain/**          # except no change expected
```

If implementation discovers a required change outside the authorized surface, stop and return to Architect review rather than expanding scope.

## 16. Acceptance report

Worker must submit:

```text
docs/incremental/P3-STATE-PERSISTENCE-RESULT.md
```

It must include:

- migration/schema summary;
- exact Store primitives implemented;
- concurrency/CAS evidence;
- atomic transaction evidence;
- restart recovery evidence;
- test command output;
- list of changed production files;
- statement that no scheduler/executor/API/provider path was added;
- `FROZEN_CONTRACT_CHANGES: NONE`.

## 17. P3 acceptance criteria

P3 is acceptable only if:

1. migration 0005 is additive and reproducible;
2. P2 Watch/Work state is persisted without semantic loss;
3. critical bucket invariants are enforced/validated;
4. scope and enum validation fail closed;
5. CAS conflicts cannot partially mutate state;
6. concurrent signal merges lose no signal;
7. atomic due-poll transaction cannot half-commit;
8. claim isolates claimed_* from later pending_*;
9. success/failure semantics match P2 exactly;
10. Watch attribution is claim-scoped;
11. stale IN_FLIGHT recovery is deterministic and idempotent;
12. all proof uses real PostgreSQL where DB semantics matter;
13. existing tests/vet/gofmt pass;
14. P0 ScanScope, Kernel, Q1–Q9, Journal, root ordering and frozen contracts are unchanged;
15. no scheduler/executor/API/CLI/provider integration is introduced.

## 18. P3 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_ONE_SHOT_DIRTY_EXECUTOR_PROTOTYPE
AUTHORIZE_MUTATION_HINT_PROTOTYPE
AUTHORIZE_HYBRID_SCHEDULER_PROTOTYPE
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 19. Current authorization

```text
P0 scoped refresh                     ACCEPTED
P1 hot-scope polling feasibility      ACCEPTED
P2 durable scope-state design         ACCEPTED
P3 state persistence prototype        AUTHORIZED AFTER THIS PLAN MERGES

0005 migration                        AUTHORIZED IN P3
Store CAS/transaction prototype       AUTHORIZED IN P3
real-PG persistence tests             AUTHORIZED IN P3

production scheduler                  NOT AUTHORIZED
production dirty executor             NOT AUTHORIZED
Mutation Hint API                     NOT AUTHORIZED
native delta/provider cursor          NOT AUTHORIZED
destructive delta/removal             NOT AUTHORIZED
Gate 5                                NOT AUTHORIZED
```
