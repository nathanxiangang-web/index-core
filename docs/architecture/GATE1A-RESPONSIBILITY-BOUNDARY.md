# Gate 1A -- Responsibility Boundary & Contract Skeleton

> Phase: Gate 1A
> Branch: `architecture/gate1a-boundary-contracts`
> Baseline: `6190d00`
> Date: 2026-09-23
> Status: READY_FOR_REVIEW
>
> This document consolidates the four-layer responsibility boundary for
> IndexCore, derived from the 10 accepted principles and validated by
> D01/D02/D03 research. It is a design-boundary document: no product
> code, no algorithm, no schema, no final Collector selection.
>
> Worker outputs: W-A (Kernel), W-B (Collector), W-C (Store+Consumer),
> W-D (Adversarial attack). See companion documents:
> - `GATE1A-COLLECTOR-CONTRACT-SKELETON.md` (Collector contract detail)
> - `GATE1A-STORE-QUERY-CONTRACT-SKELETON.md` (Store/Query contract detail)
> - `GATE1A-BOUNDARY-ATTACK-REPORT.md` (Adversarial review)

---

## 1. Four-Layer Architecture

```
Provider (external)
      |
Collector / Scanner
      |  Snapshot + evidence
Index Kernel
      |  Canonical Inventory + Change Journal
Store (PostgreSQL)
      |
      +-- Query Contract --> Consumer (CloudSite, Search, Catalog, ...)
```

**Layering rule**: each layer talks only to its adjacent layer through
a defined contract. No layer reaches across (e.g., Collector never
writes to Store; Consumer never calls Store Interface).

---

## 2. Accepted Principles (non-overridable)

1. Canonical Inventory is the unique resource truth.
2. Collector does not own Canonical State.
3. missing != deleted.
4. Incomplete input must not authorize destructive reconcile.
5. path != stable identity.
6. Provider capability is optional / driver-dependent.
7. hash is optional.
8. native delta != Change Journal (three distinct concepts).
9. PostgreSQL-first, but Kernel Domain does not bind schema/ORM.
10. Scanner checkpoint/resume does not default belong to Kernel.

---

## 3. Kernel Responsibility Boundary

### 3.1 Kernel OWNS (ACCEPTED_BOUNDARY)

| # | Responsibility | Marker | Evidence |
|---|----------------|--------|----------|
| K1 | Canonical Inventory -- the unique resource truth | ACCEPTED_BOUNDARY | Principle 1; D02 Q7/Q8 (search index is NOT canonical) |
| K2 | Resource Identity Continuity (responsibility) | ACCEPTED_BOUNDARY | Principle 5; D02 Q12 (rename = delete+add) |
| K3 | Root / Generation Semantics | ACCEPTED_BOUNDARY | Canonical-state-level concept |
| K4 | Snapshot Acceptance -- gate between observation and truth | ACCEPTED_BOUNDARY | Principles 3,4; D02 Q9/Q10 (no partial-list protection) |
| K5 | Safe Reconcile (responsibility) | ACCEPTED_BOUNDARY | Principles 3,4; D02 Q14 (no staging/atomic reconcile) |
| K6 | Canonical Change Journal | ACCEPTED_BOUNDARY | Principle 8; D02 Q5 (name-only diff is NOT a journal) |
| K7 | Syntactic boundary validation of input | ACCEPTED_BOUNDARY | Defense in depth |
| K8 | Completeness-based safety gating | ACCEPTED_BOUNDARY | Principle 4 |
| K9 | Concurrent-batch conflict resolution (responsibility) | ACCEPTED_BOUNDARY | Kernel owns canonical state |

Each item has a four-way non-delegability proof (not Collector, not
Store, not Consumer, not external-tools). See
`GATE1A-COLLECTOR-CONTRACT-SKELETON.md` section B4 for the Collector
side of the boundary.

### 3.2 Kernel does NOT own (REJECTED)

| Responsibility | Correct Owner | Reason |
|----------------|---------------|--------|
| Provider authentication | Collector | Driver-specific |
| Provider listing / enumeration | Collector | Transport concern |
| Pagination | Collector | Driver-specific |
| Retry / backoff | Collector | Transport concern |
| Cache refresh (provider cache) | Collector / external | Collector optimization |
| Remote URL generation | Consumer / external | Presentation |
| Storage API differences / schema adaptation | Store | Principle 9 |
| UI / presentation | Consumer | Not kernel domain |
| Search ranking | Consumer / external | Query-time concern |
| Catalog | Consumer / external | Presentation layer |
| CloudSite business rules | Consumer / external | Application-specific |
| Scanner checkpoint / resume | Collector / external | Principle 10 |
| Provider native delta consumption | Collector | Principles 6, 8 |
| Query projection / read-model maintenance | Store / Consumer | Kernel exposes canonical; optimization is Store/Consumer |
| Full-text search index management | Store / Consumer / external | Projection over canonical |

### 3.3 Kernel Input (concept contract)

| Property | Required? | Rationale |
|----------|-----------|-----------|
| Scope identifier (root) | REQUIRED | Which root/partition this batch covers |
| Resource entries | REQUIRED | Observed resources with identity/metadata hints |
| Completeness flag | REQUIRED | Gates Safe Reconcile (principle 4) |
| Source provenance | REQUIRED | Audit, conflict detection, debugging |
| Optional generation / cursor | OPTIONAL | Principles 6, 10 |
| Content hash | OPTIONAL | Principle 7 |
| Provider native delta | NOT INPUT | Principle 8; Kernel receives snapshots, not deltas |
| Stable identity | NOT INPUT | Kernel maintains identity (K2); Collector provides hints only |
| Provider-specific metadata | OPTIONAL | Principle 9; Kernel does not interpret |

### 3.4 Kernel Output (concept contract)

| Output | Consumer | Description |
|--------|----------|-------------|
| Canonical state (read) | Store, Consumer | Authoritative inventory at a generation |
| Reconcile result | Collector (feedback), Consumer (audit) | Added / marked-missing / unchanged / rejected |
| Canonical Change Journal | Consumer (events), Store (persistence) | Kernel-decided transitions; NOT provider delta, NOT snapshot diff |
| Query-visible state (truth contract) | Consumer | Consistent with canonical at a generation; mechanism is Store/Consumer |

---

## 4. Collector / Scanner Responsibility Boundary

### 4.1 Collector OWNS

| Responsibility | Marker |
|----------------|--------|
| Traversal / pagination of external provider | ACCEPTED_BOUNDARY |
| Producing SnapshotEntry records (candidate schema) | ACCEPTED_BOUNDARY |
| Producing Snapshot-level evidence | ACCEPTED_BOUNDARY |
| Provider error capture | ACCEPTED_BOUNDARY |
| Refresh / cache-bypass policy execution | ACCEPTED_BOUNDARY |

### 4.2 Collector does NOT own

Canonical identity, confirmed removal, generation, change type,
completeness acceptance, identity continuity, conflict resolution,
Change Journal, direct inventory mutation. All REJECTED from Collector,
assigned to Kernel.

### 4.3 Collector Output -- SnapshotEntry (CANDIDATE, not frozen)

```
SnapshotEntry (CANDIDATE):
  provider_object_id   : optional   -- DRIVER_DEPENDENT
  parent_ref           : required   -- Collector-local reference (NOT canonical resource_id)
  path                 : required   -- observed path; NOT stable identity
  name                 : required   -- leaf name
  is_dir               : required   -- directory flag
  size                 : optional   -- byte size
  mtime                : optional   -- modification time
  hash                 : optional   -- DRIVER_DEPENDENT; never mandatory
  metadata             : optional   -- provider-specific bag
```

See `GATE1A-COLLECTOR-CONTRACT-SKELETON.md` for full detail including
snapshot-level evidence, optionality rules, and adapter fit analysis.

---

## 5. Store Responsibility Boundary

### 5.1 Store OWNS

| Responsibility | Marker |
|----------------|--------|
| Persisting Domain state Kernel has computed | ACCEPTED_BOUNDARY |
| Atomic commit (canonical + journal + generation bump) | ACCEPTED_BOUNDARY |
| Optimistic concurrency via generation CAS | ACCEPTED_BOUNDARY |
| Gap-free journal ordering | ACCEPTED_BOUNDARY |

### 5.2 Store does NOT own

Deciding canonical state, validating domain invariants, interpreting
Collector input. Store persists; Kernel decides.

### 5.3 Store Interface (conceptual operations)

```
Read (Kernel loads current state):
  load_root_state(root_id) -> RootState | None
  load_canonical_inventory(root_id, at_generation?) -> CanonicalInventory
  load_journal(root_id, since_generation) -> JournalEvents

Write (inside reconcile transaction):
  begin_reconcile(root_id, expected_generation) -> ReconcileTx
  ReconcileTx.write_canonical_changes(changes) -> void
  ReconcileTx.append_journal(events) -> void
  ReconcileTx.commit() -> CommitResult{new_generation}
  ReconcileTx.rollback() -> void
```

Store Interface is expressed in Domain vocabulary; no PostgreSQL term
leaks through. See `GATE1A-STORE-QUERY-CONTRACT-SKELETON.md` for full
detail.

---

## 6. Consumer Responsibility Boundary

### 6.1 Consumer MAY

| Capability | Marker |
|------------|--------|
| Read Canonical Inventory via Query Contract | ACCEPTED_BOUNDARY |
| Read at a generation boundary | ACCEPTED_BOUNDARY |
| Subscribe to / poll the Journal | ACCEPTED_BOUNDARY |
| Own projection state (Search index, Catalog cache) | ACCEPTED_BOUNDARY |
| Request a re-sync be scheduled | ACCEPTED_BOUNDARY |

### 6.2 Consumer MUST NOT

| Prohibition | Rationale |
|-------------|-----------|
| Directly modify Canonical Inventory | INV-008 |
| Bypass the Query Contract | No direct DB access |
| Write through the Store Interface | Store Interface is Kernel-private |
| Treat projection as authoritative | Canonical wins on disagreement |
| Initiate or run a reconcile | Reconcile is Kernel-only |

### 6.3 Search Projection

Search is a projection, not Canonical Inventory (INV-002). Derived from
Journal, eventually consistent, canonical wins on disagreement. See
`GATE1A-STORE-QUERY-CONTRACT-SKELETON.md` section C4.

---

## 7. Adversarial Boundary Attack Summary

Worker D independently attacked all 8 boundary surfaces. Results:

| Attack | Surface | Verdict |
|--------|---------|---------|
| D1 | Kernel eating Scanner responsibility | WARNING |
| D2 | Collector owning canonical truth | WARNING |
| D3 | Store Interface leaking PostgreSQL | PASS |
| D4 | Consumer bypassing Query Contract | PASS |
| D5 | Optional hash/id/delta made mandatory | WARNING |
| D6 | Change Journal vs native delta conflation | WARNING |
| D7 | Second copy of resource truth | WARNING |
| D8 | Adapter swap forcing Kernel rewrite | PASS |

**0 VIOLATION, 6 WARNING, 2 PASS.** No accepted boundary directly
violates any principle. All 6 WARNINGs are CANDIDATE/DEFERRED
ambiguities. See `GATE1A-BOUNDARY-ATTACK-REPORT.md` for full detail.

---

## 8. Gate 1B Pre-conditions

These 4 clarifications, if adopted in Gate 1B, eliminate all 6 WARNINGs
without re-opening any ACCEPTED_BOUNDARY:

1. **(D1)** Pin "expected entry count range" to "derived from prior
   committed canonical state only; no independent statistical model".
   Constrain completeness heuristics to "read-only inspection of
   submitted evidence + prior canonical state; never triggers
   re-traversal".
2. **(D2/D5)** Freeze `parent_ref` to "Collector-local reference (entry
   index within the snapshot, or observed parent path), never canonical
   resource_id".
3. **(D6)** Rename `adapter_generation` to `provider_cursor` or
   `adapter_delta_token`. Rename `CollectorMode = delta_hint` to
   `incremental_hint` or `adapter_delta_opt`.
4. **(D7)** Add invariant "Root partitions are disjoint; a resource
   belongs to exactly one Root" or define the merge rule for overlapping
   Roots. Extend "Canonical wins on disagreement" to cover Change
   Journal vs Canonical Inventory repair precedence.

---

## 9. Deferred Items

### DEFERRED_TO_GATE1B

- Stable identity algorithm (constraint: respect principles 5, 6, 7)
- Rename/move matching algorithm (constraint: no mandatory hash/ID)
- Safe Reconcile state machine (constraint: enforce principles 3, 4)
- Detailed snapshot acceptance criteria
- Canonical Change Journal format
- Conflict resolution policy for concurrent batches
- Final SnapshotEntry schema (field names, types, parent_ref representation)
- Completeness acceptance algorithm (complete=true decision)
- PostgreSQL schema, SQL, migration, ORM mapping
- Query Contract exact read API, snapshot/cursor protocol, authz
- Journal event schema and projection rebuild protocol

### DEFERRED_TO_GATE1C

- Store adapter mapping (canonical state to schema)
- Query projection strategies
- Collector contract details (transport, batching)
- Multi-writer / sharded Kernel, Store failover, read-replica topology
- Search engine choice (PostgreSQL FTS vs. external index)
- Cross-Consumer fan-out / event bus transport
- Final Collector selection (AList vs rclone vs direct vs combination)

---

## 10. DO NOT Compliance

| Constraint | Status |
|------------|--------|
| Did not design stable identity algorithm | Honored -- deferred to Gate 1B |
| Did not design rename/move matching algorithm | Honored -- deferred to Gate 1B |
| Did not design PostgreSQL table structure | Honored -- deferred to Gate 1B |
| Did not design Safe Reconcile state machine | Honored -- deferred to Gate 1B |
| Did not write product code | Honored -- design document only |
| Did not choose final Collector | Honored -- deferred to Gate 1C |
| Did not make optional hash/id/delta mandatory | Honored -- explicitly optional |
| Did not conflate Change Journal with provider native delta | Honored -- principle 8 enforced |
| Did not put Scanner checkpoint in Kernel | Honored -- REJECTED (principle 10) |