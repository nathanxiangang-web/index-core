# Gate 1A Worker C -- Store + Consumer Boundary

> Boundary definition, not implementation. No PostgreSQL table, no SQL,
> no migration, no ORM model, no product code appears in this document.
> Every load-bearing conclusion is tagged
> `ACCEPTED_BOUNDARY` / `CANDIDATE` / `DEFERRED` / `REJECTED`.
> `DEFERRED` items are routed to `DEFERRED_TO_GATE1B` or
> `DEFERRED_TO_GATE1C` at the end of the document.

## 0. Context and layering

IndexCore four-layer flow:

```
Collector -> Kernel -> Store -> (durable state)
                                 |
                                 v
                          Query Contract -> Consumer
```

- `Collector` gathers raw provider state (see d02 rclone, d03 fsspec
  research). It produces input for Kernel; it never writes canonical
  state.
- `Kernel` owns the Domain model and the reconcile algorithm. It decides
  what the next Canonical Inventory is. It does not bind schema/ORM
  (principle 2); it depends only on the Store Interface.
- `Store` persists Domain state that Kernel has already computed. It is
  the only writer to Canonical Inventory.
- `Consumer` (CloudSite, Search, Catalog, future services) reads
  canonical state through the Query Contract and maintains its own
  projections. It never writes Canonical Inventory (principle 4,
  INV-008).

Accepted principles carried forward (from task BACKGROUND):

1. Canonical Inventory is the single source of resource truth.
2. PostgreSQL-first, but Kernel Domain does not bind schema/ORM; Kernel
   only depends on the Store Interface.
3. Search is not Canonical Truth (INV-002).
4. Consumer cannot bypass Kernel/Store contract to directly modify
   Canonical Inventory (INV-008).
5. Consumer includes CloudSite, Search, Catalog, and future services.

Two distinct interfaces are defined in this document and must not be
collapsed:

- **Store Interface** -- the Kernel-to-persistence surface (read for
  reconcile + write canonical + append journal + commit/rollback).
- **Query Contract** -- the Consumer-to-canonical surface (read-only
  snapshot + journal subscription). Consumer has no reference to the
  Store Interface.

---

## C1. Store Interface  [ACCEPTED_BOUNDARY]

### C1.1 Responsibility statement

Store is responsible for **persisting Domain state that Kernel has
already defined and validated**. Store does not decide canonical state,
does not run domain invariants, and does not interpret Collector input.
Store only:

- reads persisted Domain state so Kernel can compute a reconcile,
- durably writes the canonical changes and journal that Kernel produced,
- guarantees atomicity, ordering, and generation monotonicity of those
  writes.

> `ACCEPTED_BOUNDARY` Store persists; Kernel decides. The Store
> Interface is expressed in Kernel's Domain vocabulary
> (`RootState`, `CanonicalInventory`, `ResourceEntry`, `JournalEvent`,
> `Generation`) and contains no PostgreSQL term (no table, row, column,
> SQL, ORM, dialect, connection).

### C1.2 Conceptual operations

The Store Interface must support the following conceptual operations.
Method names and exact type signatures are `CANDIDATE` and are fixed in
Gate1B once the Domain types are concrete; the *operation set* is
`ACCEPTED_BOUNDARY`.

Read (Kernel loads current state to compute a reconcile):

| Operation | Returns | Purpose |
| --- | --- | --- |
| `load_root_state(root_id)` | `RootState \| None` | Load the aggregate root: its config, current generation, last reconciled cursor. |
| `load_canonical_inventory(root_id, at_generation?)` | `CanonicalInventory` | Load the canonical inventory (single source of truth, principle 1). Default: current committed generation. |
| `load_journal(root_id, since_generation)` | `JournalEvents` | Read committed journal events for replay, audit, or projection catch-up. |

Write (inside a reconcile transaction):

| Operation | Returns | Purpose |
| --- | --- | --- |
| `begin_reconcile(root_id, expected_generation)` | `ReconcileTx` | Open a reconcile unit of work. `expected_generation` is the generation Kernel observed at load; commit will fail if the persisted generation has advanced (optimistic concurrency). |
| `ReconcileTx.write_canonical_changes(changes)` | `void` | Persist the diff between old and new Canonical Inventory (upserts and deletes of `ResourceEntry`). Kernel already computed `changes`; Store only durably applies them. |
| `ReconcileTx.append_journal(events)` | `void` | Append the Domain events describing this transition (audit, replay, projection rebuild). |
| `ReconcileTx.commit()` | `CommitResult{new_generation}` | Atomically commit canonical changes + journal append + generation bump. |
| `ReconcileTx.rollback()` | `void` | Discard the in-flight reconcile; durable state unchanged. |

> `ACCEPTED_BOUNDARY` operation set: load root, load canonical
> inventory, load journal, begin reconcile, write canonical changes,
> append journal, commit, rollback.

### C1.3 Boundary properties

- **Single writer to Canonical Inventory.** Only `ReconcileTx` (held by
  Kernel) may mutate canonical state. `ACCEPTED_BOUNDARY`.
- **Generation is the concurrency token.** `begin_reconcile` captures
  `expected_generation`; `commit` succeeds only if the persisted
  generation still equals it, then atomically advances it. Two racing
  reconciles cannot both commit. `ACCEPTED_BOUNDARY`.
- **Store does not validate domain invariants.** Kernel validates;
  Store checks only persistence-structural constraints (generation
  monotonicity, journal ordering). `ACCEPTED_BOUNDARY`.
- **Store Interface leaks no PostgreSQL detail.** No type in the
  interface references a table, row, SQL type, ORM session, or
  connection. Swapping the Store engine changes only the Store
  implementation; Kernel and Consumer contracts are unchanged.
  `ACCEPTED_BOUNDARY`.
- **Journal is append-only and gap-free within committed
  transactions.** Projections resume from a generation cursor without
  missing or double-counting. `ACCEPTED_BOUNDARY` (semantic); physical
  sequence strategy is `DEFERRED_TO_GATE1B`.

### C1.4 Explicitly out of scope here

- Exact method signatures and Domain type definitions:
  `DEFERRED_TO_GATE1B` (needs the concrete Domain model).
- PostgreSQL schema, SQL, migration, ORM mapping:
  `DEFERRED_TO_GATE1B` (forbidden by DO NOT; schema is a Gate1B concern
  once the Store Interface is fixed).
- Multi-writer Kernel, Store sharding, replica routing:
  `DEFERRED_TO_GATE1C` (phase 1 is single-writer).

---

## C2. PostgreSQL Role  [ACCEPTED_BOUNDARY]

### C2.1 Why PostgreSQL in phase 1

> `ACCEPTED_BOUNDARY` PostgreSQL is the phase-1 engine behind the Store
> implementation. The Store Interface itself is engine-agnostic; this
> section only justifies the phase-1 choice and the semantics the
> implementation relies on. No schema is designed here.

Reasons:

1. **ACID over canonical + journal in one commit.** A reconcile must
   either fully update the Canonical Inventory and append its Journal
   events and bump the generation, or do none of these. PostgreSQL
   commits a multi-statement transaction atomically, so the durable
   state is never half-reconciled.
2. **Optimistic concurrency via atomic compare-and-set.** The
   generation/epoch counter must be checked and incremented atomically.
   PostgreSQL's single-row `UPDATE ... WHERE generation = :expected`
   with row-level locking gives a race-free CAS; exactly one of two
   racing reconciles succeeds.
3. **MVCC read-during-write.** Phase 1 is single-writer (one Kernel).
   Consumers read while a reconcile is in progress. PostgreSQL MVCC
   gives Consumers a consistent snapshot without blocking the writer
   and without the writer blocking readers.
4. **Durable, gap-free journal ordering.** Journal events must be
   durably ordered with no gaps or duplicates within a committed
   transaction so projections can resume from a generation cursor.
   PostgreSQL identity/sequence + transactional insert provides this.
5. **Operational maturity.** Ad-hoc queryability for ops/debugging,
   well-understood backup/restore, point-in-time recovery, and
   observable replication are required for a phase-1 system of record.
   PostgreSQL provides these without bespoke engineering.

### C2.2 Semantics PostgreSQL must provide

These are the contract the Store implementation depends on. If a future
engine cannot provide all of them, the Store implementation must
synthesize them or the engine is not a valid substitute.

| Semantic | Requirement | Tag |
| --- | --- | --- |
| Transaction | Multi-statement units of work grouping canonical write + journal append + generation bump. | `ACCEPTED_BOUNDARY` |
| Atomic commit | All-or-nothing commit of the reconcile unit; no partial canonical state is ever durable. | `ACCEPTED_BOUNDARY` |
| Rollback | Discard an in-flight reconcile on error with zero durable mutation. | `ACCEPTED_BOUNDARY` |
| Concurrency protection | Optimistic CAS on generation (fail on race) and/or pessimistic row lock for the reconcile critical section; MVCC so readers do not block the writer. | `ACCEPTED_BOUNDARY` |
| Generation consistency | Persisted generation is monotonic; a committed reconcile always observes a strictly newer generation than it began with; no two commits share a generation. | `ACCEPTED_BOUNDARY` |
| Durable ordering | Journal entries are durably ordered and gap-free within committed transactions. | `ACCEPTED_BOUNDARY` |

### C2.3 What PostgreSQL is NOT

> `ACCEPTED_BOUNDARY` PostgreSQL is not the Domain model. The schema is
> an implementation detail of the Store, invisible to Kernel and
> Consumer. The Store Interface is the only boundary Kernel depends on
> (principle 2). Replacing PostgreSQL changes only the Store
> implementation.

### C2.4 Out of scope here

- Schema, SQL, indexes, migration, ORM mapping:
  `DEFERRED_TO_GATE1B` (DO NOT forbids it in this gate).
- Read-replica topology, connection pooling, failover:
  `DEFERRED_TO_GATE1C` (operational, phase >1).

---

## C3. Consumer / Query Contract  [ACCEPTED_BOUNDARY]

### C3.1 Two interfaces, not one

- **Store Interface** (C1) is Kernel-private: read-for-reconcile +
  write canonical + journal + commit/rollback. Returns mutable Domain
  objects to Kernel.
- **Query Contract** (this section) is Consumer-facing: read-only
  snapshot + journal subscription. Returns immutable views to Consumer.

Consumer holds no reference to the Store Interface. The Query Contract
may be backed by the same PostgreSQL via read-only snapshots or by a
read replica; that backing is an implementation detail invisible to
Consumer. `ACCEPTED_BOUNDARY`.

### C3.2 What Consumer MAY do

| Capability | Description | Tag |
| --- | --- | --- |
| Read Canonical Inventory | List resources, get a resource by id, stream a snapshot at a stated generation, through the Query Contract. | `ACCEPTED_BOUNDARY` |
| Read at a generation boundary | Obtain a consistent snapshot at a committed generation so a Consumer never observes a half-committed reconcile. | `ACCEPTED_BOUNDARY` |
| Subscribe to / poll the Journal | Read committed Journal events to incrementally update its own projection (e.g. Search re-index on event). | `ACCEPTED_BOUNDARY` |
| Own projection state | Maintain its own projection stores (Search index, Catalog cache, CloudSite render cache). These are Consumer-owned, NOT canonical. | `ACCEPTED_BOUNDARY` |
| Request a re-sync be scheduled | A Consumer may ask the system to schedule a reconcile; it cannot itself run one. | `ACCEPTED_BOUNDARY` |

### C3.3 What Consumer MUST NOT do

| Prohibition | Rationale | Tag |
| --- | --- | --- |
| Directly modify Canonical Inventory | INV-008. The only mutation path is Collector -> Kernel -> Store. Consumer is not on it. | `ACCEPTED_BOUNDARY` |
| Bypass the Query Contract to read | No direct DB access, no SQL, no ORM, no Store Interface reference. Consumer sees only the Query Contract's typed read API. | `ACCEPTED_BOUNDARY` |
| Write through the Store Interface | Store Interface is Kernel-private. | `ACCEPTED_BOUNDARY` |
| Treat its projection as authoritative | If projection and canonical disagree, canonical wins; the Consumer rebuilds from the journal, never edits canonical to match its projection. | `ACCEPTED_BOUNDARY` |
| Initiate or run a reconcile | Reconcile is a Kernel operation triggered by Collector input. A Consumer may only request one be scheduled. | `ACCEPTED_BOUNDARY` |

> `ACCEPTED_BOUNDARY` The rule, stated once and plainly:
> **Consumer -> Query Contract -> (read-only view of Canonical
> Inventory + Journal). Any mutation of Canonical Inventory by a
> Consumer is a contract violation.**

### C3.4 Out of scope here

- Exact Query Contract read API (method signatures, pagination,
  snapshot/cursor protocol, authz): `DEFERRED_TO_GATE1B`.
- Journal event schema and projection rebuild protocol:
  `DEFERRED_TO_GATE1B`.
- Cross-Consumer fan-out / event bus transport:
  `DEFERRED_TO_GATE1C` (transport is an implementation concern, not a
  boundary concern).

---

## C4. Search Projection  [ACCEPTED_BOUNDARY]

### C4.1 Statement

> `ACCEPTED_BOUNDARY` Search is a projection, not the Canonical
> Inventory (INV-002). Search maintains its own indexed representation
> derived from Canonical Inventory via the Journal. It is a
> read-optimized, possibly transformed and eventually-consistent view.

### C4.2 Properties

| Property | Statement | Tag |
| --- | --- | --- |
| Derived, not authoritative | Search is built/updated from committed Journal events. It never writes back to Canonical Inventory. | `ACCEPTED_BOUNDARY` |
| Eventually consistent | Search may lag Canonical Inventory within a bounded window. Canonical is the truth; Search is a convenience. | `ACCEPTED_BOUNDARY` |
| Canonical wins on disagreement | If Search and Canonical Inventory disagree, Canonical is correct; Search is re-derived. Canonical is never edited to satisfy Search. | `ACCEPTED_BOUNDARY` |
| May transform/drop fields | Search may tokenize, stem, drop binary blobs, etc. It is not required to be a faithful 1:1 copy, which is precisely why it cannot be canonical. | `ACCEPTED_BOUNDARY` |
| Serves query workloads | Search serves full-text, filtered, faceted workloads that Canonical Inventory is not optimized for. Its purpose is query convenience, not system of record. | `ACCEPTED_BOUNDARY` |
| Does not block reconcile | Reconcile writes canonical + journal; Search catches up asynchronously. Search being up to date is not evidence canonical is correct, and Search staleness must not block a reconcile. | `ACCEPTED_BOUNDARY` |

### C4.3 Out of scope here

- Search engine choice (PostgreSQL FTS vs. external index):
  `DEFERRED_TO_GATE1C` (projection implementation detail, not a
  boundary question).
- Index mapping / analyzer config: `DEFERRED_TO_GATE1C`.

---

## 5. Consolidated tags

| Conclusion | Tag |
| --- | --- |
| Store persists; Kernel decides; Store Interface in Domain vocabulary, no PostgreSQL leakage | `ACCEPTED_BOUNDARY` |
| Store operation set (load root, load canonical, load journal, begin reconcile, write canonical, append journal, commit, rollback) | `ACCEPTED_BOUNDARY` |
| Single writer to Canonical Inventory is the reconcile transaction | `ACCEPTED_BOUNDARY` |
| Generation is the optimistic concurrency token | `ACCEPTED_BOUNDARY` |
| Store does not validate domain invariants | `ACCEPTED_BOUNDARY` |
| PostgreSQL is phase-1 Store engine; ACID + atomic commit + rollback + CAS + MVCC + monotonic generation + gap-free journal required | `ACCEPTED_BOUNDARY` |
| PostgreSQL is not the Domain model; schema is Store-internal | `ACCEPTED_BOUNDARY` |
| Query Contract is Consumer-facing read-only; Store Interface is Kernel-private; the two are not collapsed | `ACCEPTED_BOUNDARY` |
| Consumer reads canonical via Query Contract, owns its projections, never writes canonical, never bypasses Query Contract (INV-008) | `ACCEPTED_BOUNDARY` |
| Search is a projection, not canonical (INV-002); eventually consistent; canonical wins | `ACCEPTED_BOUNDARY` |
| Exact Store Interface signatures and Domain types | `CANDIDATE` -> `DEFERRED_TO_GATE1B` |
| PostgreSQL schema / SQL / migration / ORM mapping | `DEFERRED_TO_GATE1B` (forbidden in this gate by DO NOT) |
| Query Contract exact read API, snapshot/cursor protocol, authz | `DEFERRED_TO_GATE1B` |
| Journal event schema and projection rebuild protocol | `DEFERRED_TO_GATE1B` |
| Multi-writer Kernel, Store sharding, replica routing, failover | `DEFERRED_TO_GATE1C` |
| Search engine choice, index mapping, analyzer config | `DEFERRED_TO_GATE1C` |
| Cross-Consumer fan-out / event bus transport | `DEFERRED_TO_GATE1C` |

No conclusion is tagged `REJECTED`. No `REJECTED` line is needed.

---

## DEFERRED_TO_GATE1B

- Concrete Domain types (`RootState`, `CanonicalInventory`,
  `ResourceEntry`, `JournalEvent`, `Generation`) and exact Store
  Interface method signatures.
- PostgreSQL schema, SQL, indexes, migration, ORM mapping (forbidden in
  Gate1A by DO NOT; belongs to the gate that fixes the Store Interface).
- Query Contract exact read API: method signatures, pagination,
  snapshot/cursor protocol, authorization.
- Journal event schema and projection rebuild / catch-up protocol.
- Physical journal sequence strategy (sequence vs. identity vs.
  generation-bucketed) -- semantic gap-freeness is accepted here.

## DEFERRED_TO_GATE1C

- Multi-writer / sharded Kernel, Store failover, read-replica topology,
  connection pooling.
- Search engine choice (PostgreSQL FTS vs. external index), index
  mapping, analyzer configuration.
- Cross-Consumer fan-out and event-bus transport for journal
  subscription.

---

## Self-check against DO NOT

| Constraint | Status |
| --- | --- |
| No PostgreSQL table designed | Met -- no table defined. |
| No SQL written | Met -- no SQL appears. |
| No migration designed | Met -- migration only deferred. |
| No ORM model designed | Met -- ORM only referenced as a forbidden leakage. |
| No product code written | Met -- document only. |
| Store Interface leaks no PostgreSQL detail | Met -- interface in Domain vocabulary; PostgreSQL confined to C2 as the engine. |
| Consumer does not bypass Query Contract | Met -- C3.3 makes it an `ACCEPTED_BOUNDARY` prohibition. |

## Self-check against MUST ANSWER

| Question | Answered in |
| --- | --- |
| C1. Store Interface conceptual operations | C1.2, C1.3 |
| C2. Why PostgreSQL + required semantics | C2.1, C2.2 |
| C3. Consumer read / cannot modify / no bypass | C3.2, C3.3 |
| C4. Search is a projection, not canonical | C4.1, C4.2 |
