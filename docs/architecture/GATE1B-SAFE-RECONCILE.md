# Gate 1B Worker C -- Safe Reconcile + Failure Model + Change Journal Semantics

> Semantic definition, not implementation. No PostgreSQL table, no SQL,
> no migration, no ORM model, no product code appears in this document.
> Every load-bearing rule is tagged
> `ACCEPTED_SEMANTIC` / `CANDIDATE` / `DEFERRED` / `REJECTED`
> and cites evidence (accepted principle, invariant, D02/D03 finding,
> or Gate 1A boundary). `DEFERRED` items are routed to
> `DEFERRED_TO_GATE1C` at the end of the document.
>
> This document depends on, and does not redefine, two sibling Gate 1B
> contracts:
> - **Worker A -- Identity Matching**: resolves a snapshot entry to a
>   stable identity (`RESOLVED` / `UNRESOLVED`) and defines rename/move
>   identity continuity. Referenced here, not designed here.
> - **Worker B -- Completeness Acceptance**: classifies a snapshot as
>   `COMPLETE` / `PARTIAL` / `REJECTED` and defines the completeness
>   contract that gates destructive removal. Referenced here, not
>   designed here.

> **Gate roadmap (no Gate 1D):** the formal route is
> `Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC`. There is no Gate 1D.
> Any capability not fixed in Gate 1A/1B/1C is `POST_MVP` or
> `DEFERRED_UNSCHEDULED`, never "Gate 1D". `DEFERRED_TO_GATE1C` items
> in this document are routed to Gate 1C; everything beyond Gate 1C is
> `POST_MVP` / `DEFERRED_UNSCHEDULED`. `ACCEPTED_SEMANTIC`.

## 0. Context and inputs

IndexCore four-layer flow (Gate 1A ACCEPTED_BOUNDARY):

```
Collector -> Kernel -> Store -> (durable canonical state)
                                  |
                                  v
                           Query Contract -> Consumer
```

Reconcile is a **Kernel** operation. Its inputs and outputs:

| Boundary | Artifact | Owner | This doc's use |
| --- | --- | --- | --- |
| Input | `Snapshot` (observed provider state, post-Collector) | Collector | Consumed by Kernel; never writes canonical directly |
| Input | `IdentityResolution` per snapshot entry | Worker A | `RESOLVED` enables ADD/UPDATE/RENAME/MOVE; `UNRESOLVED` -> CONFLICT, no canonical mutation |
| Input | `CompletenessClass` for the snapshot | Worker B | `COMPLETE` authorizes destructive removal; `PARTIAL` -> additive-only; `REJECTED` -> reconcile aborts/stays |
| Input | `CanonicalInventory` at generation G | Store (load) | The current truth the reconcile transitions *from* |
| Output | `ReconcileTransition[]` | Kernel (this doc) | Classified state transitions |
| Output | `JournalEvent[]` (subset of transitions) | Kernel (this doc) | Only committed canonical state transitions |
| Output | `commit` / `rollback` | Store | Atomic durability of canonical + journal + generation |

> `ACCEPTED_SEMANTIC` Reconcile is a pure function of
> `(CanonicalInventory@G, Snapshot, IdentityResolution, CompletenessClass)`
> into a transition set. Kernel decides; Store persists (Gate 1A C1.1).
> Evidence: Gate 1A C1.1 (`Store persists; Kernel decides`), principle 1
> (Canonical Inventory is the unique resource truth).

### Carried-forward, non-overridable principles

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

### Gate 1A frozen boundaries used as evidence

- Kernel owns Safe Reconcile, Change Journal semantics, conflict
  resolution (Gate 1A scope split).
- Store provides `begin_reconcile`, `write_canonical_changes`,
  `append_journal`, `commit`, `rollback` (Gate 1A C1.2).
- Generation is the optimistic concurrency token (CAS at commit)
  (Gate 1A C1.3).
- Single writer to Canonical Inventory is the reconcile transaction
  (Gate 1A C1.3).
- Canonical Inventory authoritative over Journal/projection repair
  (Gate 1A precondition for Gate 1B; C4.2 canonical-wins).
- Atomic commit + rollback + zero durable mutation on rollback
  (Gate 1A C2.2).

---

## 1. Safe Reconcile state machine  [ACCEPTED_SEMANTIC]

### 1.1 Transition types (all 10 defined)

The reconcile classifies each canonical resource and each resolved
snapshot entry into exactly one transition type. The transition set is
the contract between Kernel and Store for one reconcile unit.

| # | Transition | Trigger (semantic) | Canonical mutation? | Journal event? | Tag |
| --- | --- | --- | --- | --- | --- |
| 1 | `ADD` | Snapshot entry `RESOLVED` to an identity NOT present in CanonicalInventory@G. Identity = NEW_RESOURCE. | Insert `ResourceEntry` | `resource-added` | `ACCEPTED_SEMANTIC` |
| 2 | `UPDATE` | Snapshot entry `RESOLVED` to an identity present in canonical; identity unchanged; comparable attributes changed (size, modtime, hash, metadata). | Update `ResourceEntry` | `resource-updated` | `ACCEPTED_SEMANTIC` |
| 3 | `RENAME` | Identity preserved (Worker A continuity); path component (name) changed; parent unchanged; within same root. | Update path on `ResourceEntry` | `resource-renamed` | `ACCEPTED_SEMANTIC` |
| 4 | `MOVE` | Identity preserved; parent changed; within same root. | Update parent + path on `ResourceEntry` | `resource-moved` | `ACCEPTED_SEMANTIC` |
| 5 | `MISSING` | Canonical resource not observed in the accepted snapshot. | None (`RemovalEvidenceState` only; `ResourcePresence` unchanged) | None | `ACCEPTED_SEMANTIC` |
| 6 | `REMOVAL_CANDIDATE` | A `MISSING` resource meets promotion criteria (Sec 1.3). | None (`RemovalEvidenceState` only; `ResourcePresence` unchanged) | None | `ACCEPTED_SEMANTIC` |
| 7 | `CONFIRMED_REMOVED` | A `REMOVAL_CANDIDATE` passes removal validation AND the governing snapshot is `COMPLETE` (Worker B). | Delete `ResourceEntry` | `resource-removed` | `ACCEPTED_SEMANTIC` |
| 8 | `CONFLICT` | Identity `UNRESOLVED` (Worker A), or contradictory evidence (duplicate identity, ambiguous rename/move). | None | None (conflict recorded in reconcile result, not journal) | `ACCEPTED_SEMANTIC` |
| 9 | `UNCHANGED` | Snapshot entry `RESOLVED` to an identity present in canonical; no comparable attribute change. | None | None | `ACCEPTED_SEMANTIC` |
| 10 | `REJECTED` | Snapshot or entry rejected: `CompletenessClass = REJECTED` (Worker B), or entry fails a hard acceptance rule. | None | None (reason recorded in reconcile result) | `ACCEPTED_SEMANTIC` |

> `ACCEPTED_SEMANTIC` The 10 transition types form a partition of the
> reconcile outcome space for a single (canonical resource, snapshot
> entry) pair under a given IdentityResolution and CompletenessClass.
> Evidence: principle 1 (canonical truth), principle 5 (path != identity
> -> RENAME/MOVE are identity-preserving, not new resources), Gate 1A
> C1.1 (Kernel decides transitions).

### 1.2 Classification rules (precedence)

Classification is applied in the following precedence order so each
candidate pair resolves to exactly one transition:

1. If `CompletenessClass = REJECTED` -> `REJECTED` for the whole
   reconcile unit. `ACCEPTED_SEMANTIC`. Evidence: principle 4, Worker B
   contract.
2. For a snapshot entry with `IdentityResolution = UNRESOLVED` ->
   `CONFLICT`. No canonical mutation. `ACCEPTED_SEMANTIC`. Evidence:
   principle 5 (path != identity; unresolved identity cannot be
   matched), Worker A contract.
3. For a `RESOLVED` entry whose identity is NOT in canonical ->
   `ADD`. `ACCEPTED_SEMANTIC`. Evidence: principle 1.
4. For a `RESOLVED` entry whose identity IS in canonical:
   - identity preserved, parent unchanged, name unchanged, no attribute
     change -> `UNCHANGED`.
   - identity preserved, parent unchanged, name changed -> `RENAME`.
   - identity preserved, parent changed -> `MOVE`.
   - identity preserved, attributes changed -> `UPDATE`.
   - (Blocker H resolution -- RENAME/MOVE + UPDATE composite is FROZEN
     as `ACCEPTED_SEMANTIC`, no longer `CANDIDATE`: when both path and
     attributes change in one pass, the reconcile produces an ORDERED
     pair of transitions within the same generation -- first the
     path-change transition (`RENAME` or `MOVE`), then the
     attribute-change transition (`UPDATE`). The two transitions commit
     atomically in one reconcile transaction (Gate 1A C2.2) and produce
     two ordered journal events. Ordering is by an intra-generation
     sequence number assigned by Kernel; Consumer observes both events
     at the same generation, ordered by intra-generation sequence.
     Consumer-visible semantic result: the resource ended the reconcile
     at the new path with the new attributes; both the path change and
     the attribute change are auditable as distinct events. The
     intra-generation sequence numbering scheme is `DEFERRED_TO_GATE1C`
     (persistence detail); the semantic ordering (path-change before
     attribute-change within one generation) is `ACCEPTED_SEMANTIC`
     and fixed here.) `ACCEPTED_SEMANTIC`. Evidence: principle 5
     (path != identity; path change is identity-preserving), principle
     1 (canonical truth; both changes are committed atomically), Gate
     1A C2.2 (atomic commit), Worker A (identity continuity).
5. For a canonical resource with no `RESOLVED` snapshot entry observed:
   - if `CompletenessClass = PARTIAL` -> `MISSING` (recorded only; NOT
     promoted). `ACCEPTED_SEMANTIC`. Evidence: principle 4, INV-004.
   - if `CompletenessClass = COMPLETE` -> `MISSING`, then apply the
     Missing -> Removal lifecycle (Sec 1.3). `ACCEPTED_SEMANTIC`.
6. Contradictory evidence (two snapshot entries `RESOLVED` to the same
   canonical identity, or rename/move continuity contradicts an
   observed add) -> `CONFLICT` for all involved entries. No canonical
   mutation. `ACCEPTED_SEMANTIC`. Evidence: principle 1 (canonical
   truth cannot be made inconsistent), Worker A.

> `ACCEPTED_SEMANTIC` Precedence is total: rules 1-6 cover every input
> shape and are pairwise exclusive at the pair level. A pair never
> receives two transitions in one reconcile.

### 1.3 Missing -> Removal lifecycle  [ACCEPTED_SEMANTIC]

```
[ResourcePresence = PRESENT, RemovalEvidenceState = NONE]
    |
    v  (not observed in an accepted COMPLETE snapshot)
[ResourcePresence = PRESENT, RemovalEvidenceState = MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT]
    |
    v  (promotion criteria met: time, evidence, completeness -- Sec 1.3.1)
[ResourcePresence = PRESENT, RemovalEvidenceState = REMOVAL_CANDIDATE]
    |
    v  (removal validation passed AND governing snapshot COMPLETE -- Sec 1.3.2)
[ResourcePresence = REMOVED]   # ResourceEntry deleted; resource-removed journal event appended
```

The first three rows are `ResourcePresence = PRESENT` with evolving
`RemovalEvidenceState` (Kernel-internal, no journal event). The final
row is the only externally visible canonical state change
(`PRESENT` -> `REMOVED`), and is the only row that produces a journal
event (`resource-removed`).

State carried per canonical resource across reconcile passes is split
into two orthogonal fields. This split is `ACCEPTED_SEMANTIC` and
resolves Blocker F (the prior single-field formulation created a
semantic conflict between "MISSING is a canonical state" and "MISSING
produces no canonical mutation / no journal event").

```
ResourcePresence:               # externally visible canonical state
  PRESENT                       # resource exists in Canonical Inventory
  REMOVED                       # resource deleted (CONFIRMED_REMOVED)

RemovalEvidenceState:           # Kernel-internal lifecycle evidence
  NONE                          # observed in the latest accepted snapshot
  MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT
                                # not observed; >=1 COMPLETE snapshot
                                # has reported absence since missing_since
  REMOVAL_CANDIDATE             # promotion criteria met (Sec 1.3.1)
```

> `ACCEPTED_SEMANTIC` **MISSING is NOT a Consumer-visible Canonical
> Resource State.** The Consumer-visible canonical resource state is
> `ResourcePresence` only (`PRESENT` / `REMOVED`). `RemovalEvidenceState`
> is Kernel-internal lifecycle metadata used to decide when a `PRESENT`
> resource may transition to `REMOVED`; it is not itself a canonical
> resource state and is not surfaced through the Query Contract as a
> resource status. Evidence: principle 1 (Canonical Inventory is the
> unique resource truth; what Consumer sees is presence/absence of a
> `ResourceEntry`), principle 3 (missing != deleted -- missing is
> evidence, not a canonical state), Sec 1.1 table (`MISSING`/
> `REMOVAL_CANDIDATE` produce no canonical mutation and no journal
> event).

> `ACCEPTED_SEMANTIC` **The Journal records ONLY externally visible
> canonical resource transitions** -- i.e. transitions that change
> `ResourcePresence` or the externally visible content of a `PRESENT`
> `ResourceEntry` (path, attributes, identity). `RemovalEvidenceState`
> transitions (`NONE` -> `MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT` ->
> `REMOVAL_CANDIDATE`) are NOT journal events: they are internal
> evidence accumulation, not externally visible canonical mutations.
> This sharpens J1/J4: the Journal is the record of externally visible
> canonical truth transitions, not the record of all canonical-record
> field writes. Evidence: principle 1, principle 3, Sec 3.1, J1, J4.

The canonical record carries `missing_since` (timestamp of first
`MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT`) and `removal_evidence_state`
(the `RemovalEvidenceState` enum above). These are canonical metadata
fields, not provider state, and their mutation is not a journal event.

#### 1.3.1 When does MISSING become REMOVAL_CANDIDATE? (criteria)

> `ACCEPTED_SEMANTIC` A `MISSING` resource becomes `REMOVAL_CANDIDATE`
> when ALL of the following hold:
> - **C1 (completeness):** at least one governing snapshot since the
>   resource became MISSING was classified `COMPLETE` (Worker B). A
>   `PARTIAL` snapshot extends `missing_since` but does NOT promote.
>   Evidence: principle 4, INV-004.
> - **C2 (time):** `now - missing_since >= removal_grace_period`. The
>   exact `removal_grace_period` is `CANDIDATE` (configuration, per-root
>   policy); the semantic requirement that a non-zero grace exists is
>   `ACCEPTED_SEMANTIC`. Evidence: principle 3 (missing != deleted; a
>   single missing observation is not deletion).
> - **C3 (evidence):** the resource has been MISSING across
>   `min_consecutive_complete_missing` consecutive `COMPLETE` snapshots
>   (default `CANDIDATE` >= 1). A reappearance at any point resets the
>   counter and returns the resource to `Observed`. Evidence: principle
>   3, principle 1.

If any criterion fails, the resource stays `MISSING` (state retained,
`missing_since` possibly updated). No canonical content mutation, no
journal event.

#### 1.3.2 When does REMOVAL_CANDIDATE become CONFIRMED_REMOVED? (validation)

> `ACCEPTED_SEMANTIC` A `REMOVAL_CANDIDATE` becomes `CONFIRMED_REMOVED`
> when ALL of the following hold:
> - **V1 (completeness gate):** the governing snapshot is `COMPLETE`
>   (Worker B). A `PARTIAL` snapshot CANNOT confirm removal.
>   `ACCEPTED_SEMANTIC`, hard rule (INV-004). Evidence: principle 4.
> - **V2 (validation):** removal validation passes. Validation evidence
>   is `ACCEPTED_SEMANTIC` in boundary and `CANDIDATE` in concrete
>   mechanism. The boundary is fixed here; the mechanism is
>   `DEFERRED_TO_GATE1C`.
>   - **V2a (evidence sources):** validation evidence MUST come only
>     from (i) Collector-submitted evidence in a later accepted
>     `Snapshot`, (ii) an independently admitted `Snapshot` distinct
>     from the one that first produced the `MISSING` observation, or
>     (iii) already-committed canonical history. `ACCEPTED_SEMANTIC`.
>     Evidence: Gate 1A boundary (Collector -> Kernel -> Store;
>     Kernel does not traverse Provider), principle 2 (Collector does
>     not own canonical state).
>   - **V2b (Kernel does not touch Provider):** Kernel MUST NOT perform
>     Provider traversal as part of validation -- no re-list of parent,
>     no provider id refresh, no Provider API call. Any re-listing is a
>     Collector concern that surfaces as a future `Snapshot`, not a
>     Kernel-initiated read. `ACCEPTED_SEMANTIC`. Evidence: Gate 1A
>     C1.1 (`Kernel decides; Store persists`; Kernel scope is
>     classification + journal semantics, not Provider I/O), Gate 1A
>     scope split.
>   - **V2c (independence):** the validation evidence MUST be
>     independent of the same snapshot/observation that produced the
>     first `MISSING`. Concretely, confirmation requires the resource
>     to be absent in at least one *different* accepted `COMPLETE`
>     `Snapshot` admitted after the one that first observed `MISSING`.
>     Re-using the first missing-observation as its own confirmation
>     is forbidden. `ACCEPTED_SEMANTIC`. The minimum number of
>     independent confirming snapshots is `CANDIDATE` (configuration,
>     per-root policy; default >= 1). Evidence: principle 3 (missing
>     != deleted requires positive evidence of removal, not absence of
>     a single observation), Sec 1.3.1 C3.
>   - **V2d (mechanism deferred):** the concrete validation mechanism
>     (how a later snapshot is scheduled, how independence is checked,
>     how evidence is persisted) is `DEFERRED_TO_GATE1C`. The boundary
>     above (V2a-V2c) is fixed here and MUST NOT be blurred by Gate 1C
>     mechanism choices. `ACCEPTED_SEMANTIC`.
> - **V3 (no reappearance):** the resource has not reappeared in any
>   accepted snapshot between promotion and validation. Evidence:
>   principle 1.

On `CONFIRMED_REMOVED`: the `ResourceEntry` is deleted from Canonical
Inventory and a `resource-removed` journal event is appended, all inside
the same committed reconcile transaction (Gate 1A C1.2, C2.2 atomic
commit).

#### 1.3.3 When does MISSING become CONFLICT? (ambiguous evidence)

> `ACCEPTED_SEMANTIC` A `MISSING` becomes `CONFLICT` (and is NOT
> promoted to `REMOVAL_CANDIDATE`) when evidence is ambiguous:
> - the same stable identity is simultaneously reported by another
>   snapshot entry (duplicate identity), or
> - Worker A's identity continuity cannot distinguish rename/move from
>   delete+add (the entry is `UNRESOLVED`), or
> - two different identities claim the canonical resource's prior path.
>
> No canonical mutation. The conflict is recorded in the reconcile
> result for operator/Worker A review. Evidence: principle 1, principle
> 5, Worker A contract.

#### 1.3.4 Can CONFIRMED_REMOVED be reversed? (resource reappears)

> `ACCEPTED_SEMANTIC` `CONFIRMED_REMOVED` is NOT reversed. If a resource
> reappears with the same stable identity after removal, it is treated
> as a fresh `ADD` at the new generation. The prior `resource-removed`
> journal event remains in the append-only journal; a new
> `resource-added` event is appended. Rationale: reversing removal would
> require mutating append-only journal history and resurrecting a
> deleted canonical record, violating journal append-only (Gate 1A
> C1.3) and canonical-wins repair (Gate 1A precondition). Re-add is the
> safe, auditable path. Evidence: principle 1, Gate 1A C1.3, Gate 1A
> canonical-wins precondition.

### 1.4 Hard rules  [ACCEPTED_SEMANTIC]

| Rule | Statement | Tag | Evidence |
| --- | --- | --- | --- |
| INV-003 | `missing != deleted`: `MISSING` is NOT `CONFIRMED_REMOVED`. A missing observation is a state, not a deletion. | `ACCEPTED_SEMANTIC` | Principle 3, Sec 1.3.1 (promotion requires criteria), Sec 1.3.2 (confirmation requires validation) |
| INV-004 | Incomplete snapshot CANNOT authorize `CONFIRMED_REMOVED`. Only `COMPLETE` snapshots (Worker B) can authorize destructive removal. | `ACCEPTED_SEMANTIC` | Principle 4, Sec 1.3.1 C1, Sec 1.3.2 V1, Worker B contract |
| INV-013 | Any uncommitted reconcile MUST NOT destroy previous canonical truth. | `ACCEPTED_SEMANTIC` | Gate 1A C2.2 (atomic commit + rollback + zero durable mutation), Sec 2.3, Sec 2.7 |

> `ACCEPTED_SEMANTIC` Destructive removal (`CONFIRMED_REMOVED` ->
> `resource-removed`) is the ONLY transition that deletes a canonical
> `ResourceEntry`, and it is gated by both completeness (`COMPLETE`) and
> validation. All other transitions are additive or in-place update.
> Evidence: Sec 1.1 table (canonical mutation column), INV-003, INV-004.

---

## 2. Failure Model  [ACCEPTED_SEMANTIC]

The failure model defines Kernel behavior for seven scenarios. In every
scenario, the hard guarantee is INV-013: an uncommitted reconcile does
not destroy previous canonical truth.

### 2.1 Scenario 1 -- Identity unresolved (UNRESOLVED from Worker A)

> `ACCEPTED_SEMANTIC` For any snapshot entry with
> `IdentityResolution = UNRESOLVED`: no canonical mutation is performed
> for that entry; a `CONFLICT` is recorded in the reconcile result
> (not the journal -- conflicts are not committed canonical transitions).
> The reconcile continues for other, `RESOLVED` entries. If the entry's
> ambiguity blocks classification of another pair, that pair is also
> `CONFLICT`. Evidence: principle 5 (path != identity; unresolved
> identity cannot be safely matched), Sec 1.2 rule 2/6, Worker A
> contract.

### 2.2 Scenario 2 -- Snapshot incomplete (PARTIAL from Worker B)

> `ACCEPTED_SEMANTIC` When `CompletenessClass = PARTIAL`: the reconcile
> is **additive-only**.
> - `ADD`, `UPDATE`, `RENAME`, `MOVE`, `UNCHANGED` are permitted for
>   `RESOLVED` entries.
> - Canonical resources not observed are recorded as `MISSING` (state
>   retained, `missing_since` updated) but are NOT promoted to
>   `REMOVAL_CANDIDATE` and NEVER to `CONFIRMED_REMOVED`.
> - No `resource-removed` journal event is produced in a `PARTIAL`
>   reconcile.
> Evidence: principle 4, INV-004, Sec 1.2 rule 5, Sec 1.3.1 C1, Worker
> B contract.

### 2.3 Scenario 3 -- Reconcile failure (internal error)

> `ACCEPTED_SEMANTIC` On any internal error during transition
> computation or before commit: Kernel calls `Store.rollback()`. The
> in-flight reconcile is discarded; durable canonical state, journal,
> and generation are unchanged (previous canonical truth preserved).
> The error is recorded in the reconcile result. Evidence: Gate 1A
> C1.2 (`rollback` discards in-flight; durable state unchanged), C2.2
> (rollback semantic), INV-013.

### 2.4 Scenario 4 -- Same Snapshot replay (idempotency)

> `ACCEPTED_SEMANTIC` Replaying the same snapshot against the same
> canonical generation yields `NO CHANGE`:
> - every `RESOLVED` entry classifies as `UNCHANGED`;
> - no `ResourceEntry` is inserted, updated, or deleted;
> - no `JournalEvent` is appended;
> - no generation bump is committed (the reconcile is a no-op commit,
>   or equivalently Kernel skips `commit` entirely and releases the
>   transaction without advancing generation).
>
> "Same snapshot" is defined as: same snapshot identity (same observed
> content/cursor, per Collector) AND same canonical generation observed
> at load. If canonical advanced between replays, the replay is against
> a newer canonical state and is not a no-op (it is a normal reconcile
> against the new generation). Evidence: principle 1 (canonical truth),
> Gate 1A C1.3 (generation monotonicity), Sec 1.2 rule 4 (`UNCHANGED`
> produces no mutation and no event).

### 2.5 Scenario 5 -- Stale generation (another reconcile committed first)

> `ACCEPTED_SEMANTIC` Kernel loaded canonical at generation G and
> computed transitions. Before/at commit, another reconcile committed
> generation G+1. `Store.commit()` performs compare-and-set on
> generation; it returns `CAS_FAIL` because the persisted generation no
> longer equals `expected_generation = G`. Kernel then either:
> - **retries**: reload canonical at the new generation, recompute
>   transitions against the new truth, and re-attempt commit; or
> - **aborts**: rolls back and reports the stale-attempt result.
>
> The retry/abort policy is `CANDIDATE` (configuration); the CAS
> semantics and the guarantee that two racing reconciles cannot both
> commit are `ACCEPTED_SEMANTIC`. Evidence: Gate 1A C1.3 (generation is
> the concurrency token; exactly one of two racing reconciles
> succeeds), C2.2 (concurrency protection).

### 2.6 Scenario 6 -- Concurrent Snapshots for same root

> `ACCEPTED_SEMANTIC` Two reconciles for the same root, driven by two
> different snapshots, are ordered by a **per-root Input Ordering**
> (Blocker I resolution: serializable-only is insufficient; the
> ordering semantic is fixed here). The ordering is defined as follows:
> - **IO1 (admission sequence):** every accepted input (`Snapshot` +
>   `IdentityResolution` + `CompletenessClass`) is assigned a
>   Kernel-owned, per-root monotonically increasing admission
>   sequence token at admission time. The token is Kernel-owned, NOT
>   derived from provider wall-clock. `ACCEPTED_SEMANTIC`. Evidence:
>   principle 1 (Kernel decides ordering), Gate 1A C1.1 (Kernel
>   decides).
> - **IO2 (no out-of-order overwrite):** an older input (lower
>   admission sequence) MUST NOT overwrite canonical state already
>   committed by a newer input (higher admission sequence) that was
>   admitted and committed first. `ACCEPTED_SEMANTIC`. Evidence:
>   principle 1 (canonical truth is monotonic), Gate 1A C1.3
>   (generation monotonicity).
> - **IO3 (duplicate snapshot = no-op):** a `Snapshot` with the same
>   snapshot identity (same observed content/cursor, per Collector)
>   as an already-applied input at the current or later generation
>   is a NO-OP (Scenario 4 idempotency). `ACCEPTED_SEMANTIC`.
>   Evidence: Sec 2.4.
> - **IO4 (stale input):** an input whose admission sequence is
>   older than an already-committed input for the same root, AND
>   whose snapshot is not a duplicate of an already-applied input,
>   is classified `STALE_INPUT` (a `REJECTED` sub-case, Sec 1.1
>   row 10): no canonical mutation, reason recorded in the reconcile
>   result. `ACCEPTED_SEMANTIC`. Evidence: principle 1, IO2.
> - **IO5 (later admitted input):** a later-admitted input (higher
>   admission sequence) is processed in admission order against the
>   then-current canonical generation. `ACCEPTED_SEMANTIC`.
> - **IO6 (CAS is enforcement, not ordering):** generation CAS at
>   `Store.commit()` is the Store-level enforcement of IO2/IO4; it is
>   NOT the ordering semantic itself. The ordering semantic is the
>   Kernel-owned admission sequence (IO1). `ACCEPTED_SEMANTIC`.
>   Evidence: Gate 1A C1.3 (CAS is the concurrency token; ordering
>   is a Kernel concern).
> - **IO7 (no provider wall-clock as sole ordering source):**
>   provider wall-clock timestamp MUST NOT be the sole ordering
>   authority. Provider clocks may be skew, non-monotonic, or absent.
>   The Kernel-owned admission sequence is authoritative; provider
>   timestamps may be used only as advisory input to admission.
>   `ACCEPTED_SEMANTIC`. Evidence: principle 6 (provider capability
>   is optional/driver-dependent), principle 1.
>
> Reconciles for *different* roots are independent and need not
> serialize. The concrete admission-sequence persistence and the
> duplicate-detection mechanism are `DEFERRED_TO_GATE1C`; the ordering
> semantic (IO1-IO7) is `ACCEPTED_SEMANTIC` and fixed here. Evidence:
> Gate 1A C1.3 (single writer to Canonical Inventory is the reconcile
> transaction; generation CAS), principle 1, Sec 2.4, Sec 2.5.

### 2.7 Scenario 7 -- Store commit failure

> `ACCEPTED_SEMANTIC` If `Store.commit()` fails for any reason other
> than `CAS_FAIL` (e.g. storage error, durability failure): Kernel
> treats it as a reconcile failure (Scenario 3). `Store.rollback()`
> discards the in-flight transaction; previous canonical truth is
> preserved. No partial canonical state, no partial journal append, no
> partial generation bump is durable (Gate 1A C2.2 atomic commit:
> all-or-nothing). Evidence: Gate 1A C2.2 (atomic commit, rollback),
> INV-013.

### 2.8 Hard guarantee (consolidated)

> `ACCEPTED_SEMANTIC` **Any uncommitted reconcile MUST NOT destroy
> previous canonical truth (INV-013).** This holds in all seven
> scenarios:
> - Scenarios 1, 2, 4 produce no destructive mutation by construction
>   (additive-only / no-op / conflict-only).
> - Scenarios 3, 7 rollback; Store atomicity guarantees zero durable
>   mutation.
> - Scenarios 5, 6 fail at CAS before any durable write; the losing
>   reconcile commits nothing.
>
> Evidence: Gate 1A C1.2, C1.3, C2.2 (atomic commit + rollback + CAS +
> single writer), Sec 1.4, principles 1, 3, 4.

---

## 3. Canonical Change Journal semantics  [ACCEPTED_SEMANTIC]

### 3.1 Semantic event types (5 defined)

The Change Journal records committed, **externally visible** canonical
state transitions (Blocker F resolution: only transitions that change
`ResourcePresence` or the externally visible content of a `PRESENT`
`ResourceEntry` -- path, attributes, identity). `RemovalEvidenceState`
transitions are NOT journal events. Only five event types exist, mapped
from the transition types that mutate externally visible canonical
content:

| Event | Produced by transition | Payload (semantic, not schema) | Tag |
| --- | --- | --- | --- |
| `resource-added` | `ADD` | stable identity, path, attributes at new generation | `ACCEPTED_SEMANTIC` |
| `resource-updated` | `UPDATE` | stable identity, changed attributes, new generation | `ACCEPTED_SEMANTIC` |
| `resource-renamed` | `RENAME` | stable identity, old path, new path (same parent), new generation | `ACCEPTED_SEMANTIC` |
| `resource-moved` | `MOVE` | stable identity, old parent/path, new parent/path, new generation | `ACCEPTED_SEMANTIC` |
| `resource-removed` | `CONFIRMED_REMOVED` | stable identity, last known path, new generation | `ACCEPTED_SEMANTIC` |

> `ACCEPTED_SEMANTIC` The transitions `MISSING`, `REMOVAL_CANDIDATE`,
> `UNCHANGED`, `CONFLICT`, and `REJECTED` produce NO journal event.
> They are not externally visible canonical state transitions:
> `MISSING`/`REMOVAL_CANDIDATE` mutate only `RemovalEvidenceState`
> (Kernel-internal lifecycle evidence; `ResourcePresence` stays
> `PRESENT`), `UNCHANGED` is a no-op, `CONFLICT`/`REJECTED` are
> reconcile-result signals. Evidence: principle 1 (journal records
> externally visible canonical truth transitions), principle 3
> (missing != deleted -- missing is evidence, not a canonical state),
> Sec 1.1 table (journal event column), Sec 1.3 state-field split,
> Gate 1A C1.2 (`append_journal` records Domain events describing the
> transition).

### 3.2 Journal rules  [ACCEPTED_SEMANTIC]

| # | Rule | Tag | Evidence |
| --- | --- | --- | --- |
| J1 | The Journal records ONLY committed canonical state transitions. Uncommitted, rolled-back, or no-op reconciles append nothing. | `ACCEPTED_SEMANTIC` | Gate 1A C1.2 (`append_journal` inside `ReconcileTx`), C2.2 (atomic commit); Sec 2.3, 2.4, 2.7 |
| J2 | The Journal does NOT record provider-reported changes directly. Provider native delta is Collector input, not canonical truth. | `ACCEPTED_SEMANTIC` | Principle 8, principle 2 (Collector does not own canonical state) |
| J3 | The Journal is NOT the provider native delta. (principle 8) | `ACCEPTED_SEMANTIC` | Principle 8 |
| J4 | The Journal is NOT the snapshot diff. A snapshot diff includes `MISSING` observations; the journal includes only `resource-removed` after confirmation. | `ACCEPTED_SEMANTIC` | Principle 8, Sec 1.1 (`MISSING` produces no event), Sec 1.3 |
| J5 | The Journal is append-only within committed transactions. Events are never edited or deleted; a reappearing resource appends `resource-added`, it does not undo a prior `resource-removed` (Sec 1.3.4). | `ACCEPTED_SEMANTIC` | Gate 1A C1.3 (append-only, gap-free), Sec 1.3.4 |
| J6 | When the Journal disagrees with Canonical Inventory, Canonical Inventory is the truth. The canonical Journal history is NEVER rewritten or edited (consistent with J5); repair is performed ONLY by (a) appending a corrective journal event inside a committed reconcile transaction, or (b) rebuilding a *derived projection / read model* (not the canonical Journal itself). Canonical Inventory is NEVER edited to satisfy the Journal. (Blocker G resolution: J5 append-only and J6 repair are reconciled by making repair append-only at the canonical Journal, never by rewriting history.) | `ACCEPTED_SEMANTIC` | Principle 1, J5 (append-only), Gate 1A precondition (canonical wins on disagreement, extends to Journal vs Canonical repair), Gate 1A C4.2 (canonical-wins) |
| J7 | Provider delta MUST NOT directly write the Journal. The only path to a journal event is: Collector -> Kernel (transition classification) -> Store (`append_journal` inside a committed `ReconcileTx`). | `ACCEPTED_SEMANTIC` | Principle 2, principle 8, Gate 1A C1.2/C1.3 (single writer is the reconcile transaction), DO NOT (`Do not let provider delta directly write Journal`) |

### 3.3 What the Journal is NOT (three distinct concepts)  [ACCEPTED_SEMANTIC]

> `ACCEPTED_SEMANTIC` Three concepts that must not be collapsed
> (principle 8):
> 1. **Provider native delta** -- what the provider reports changed
>    (Collector input; backend-specific; e.g. rclone `ChangeNotify`,
>    fsspec `info()` diffs). Optional, driver-dependent (principle 6),
>    not authoritative.
> 2. **Snapshot diff** -- the diff between two snapshots, or between a
>    snapshot and canonical. Includes `MISSING` observations that are
>    not yet removals. An intermediate computation, not the journal.
> 3. **Change Journal** -- the committed, append-only record of
>    canonical state transitions. Authoritative for audit/replay/
>    projection catch-up; subordinate to Canonical Inventory on
>    disagreement (J6).
>
> Evidence: principle 8, J2-J4, J6. D02 finding: rclone `ChangeNotifier`
> is an optional backend feature (`fs/features.go`, D02 Sec 6) -- native
> delta is provider-specific and absent on many backends, confirming it
> cannot be the journal. D03 finding: fsspec has no unified change
> stream; `info()` is per-path state (D03 Sec 1) -- there is no
> provider-native delta to equate the journal to.

### 3.4 Journal repair  [ACCEPTED_SEMANTIC]

> `ACCEPTED_SEMANTIC` If the Journal and Canonical Inventory disagree
> (e.g. a journal event was lost, duplicated, or describes a state that
> canonical does not contain): Canonical Inventory is the truth.
> Repair is constrained by J5 (append-only) -- Blocker G resolution:
> - **The canonical Journal history is NEVER rewritten or edited.**
>   No event is mutated, deleted, or back-dated. J5 append-only and
>   J6 repair are reconciled by making repair append-only at the
>   canonical Journal, not by rewriting history.
> - **Repair path A (corrective event):** append a corrective
>   `resource-*` journal event (e.g. a `resource-removed` if the
>   Journal erroneously still lists a resource canonical has deleted;
>   a re-`resource-added` if the Journal erroneously marks removed a
>   resource canonical still contains). The corrective event is a
>   normal committed canonical transition appended inside a reconcile
>   transaction.
> - **Repair path B (derived projection rebuild):** if the
>   disagreement is in a *derived projection / read model* (e.g. a
>   search index or a catch-up cursor), that projection MAY be
>   rebuilt from canonical state. The canonical Journal itself is
>   not rebuilt; only derived/non-authoritative read models are.
> - Canonical Inventory is NEVER edited to satisfy the Journal.
>
> The choice between path A and path B for a given disagreement, and
> the concrete persistence mechanism, is `DEFERRED_TO_GATE1C`. The
> semantic constraint above (canonical wins; Journal history is
> append-only; only corrective appends or derived-projection rebuilds
> are permitted) is `ACCEPTED_SEMANTIC` and fixed here. Evidence:
> principle 1, J5 (append-only), Gate 1A canonical-wins precondition,
> Gate 1A C4.2.

---

## 4. Worker Scenario Matrix  [REQUIRED]

This section is the Worker's own design-level verification matrix --
scenarios checked, counterexamples considered, and contract-consistency
closed loop. No subagent is invoked; the Worker performs this analysis
inline. Runtime test execution against product code is
`DEFERRED_TO_GATE1C` (no product code exists at this gate per DO NOT).

### 4.1 State transition scenarios  [ACCEPTED_SEMANTIC]

| # | Scenario | Expected outcome | Evidence |
| --- | --- | --- | --- |
| a | ADD new resource | `resource-added` | Sec 1.1 row 1 |
| b | UPDATE changed attrs | `resource-updated` | Sec 1.1 row 2 |
| c | RENAME same parent new name | `resource-renamed`, identity preserved | Sec 1.1 row 3 |
| d | MOVE new parent | `resource-moved`, identity preserved | Sec 1.1 row 4 |
| e | MISSING on COMPLETE snapshot, grace not elapsed | stays MISSING, no event | Sec 1.3.1 C2 |
| f | MISSING -> REMOVAL_CANDIDATE -> CONFIRMED_REMOVED on COMPLETE | `resource-removed` | Sec 1.3.1, 1.3.2 |
| g | MISSING on PARTIAL | stays MISSING, no promotion (INV-004) | Sec 1.3.1 C1, Sec 2.2 |
| h | Same snapshot replay | NO CHANGE, no event (idempotency) | Sec 2.4 |
| i | Concurrent reconciles | exactly one commits; per-root Input Ordering (IO1-IO7) | Sec 2.5, 2.6 |
| j | Internal error | rollback, canonical unchanged | Sec 2.3 |
| k | Resource reappears after CONFIRMED_REMOVED | fresh `resource-added`, prior `resource-removed` retained | Sec 1.3.4 |
| l | RENAME/MOVE + UPDATE in one pass | ordered pair: path-change then UPDATE, same generation | Sec 1.2 rule 4 (Blocker H) |
| m | Older out-of-order input | STALE_INPUT, no mutation | Sec 2.6 IO4 (Blocker I) |
| n | Duplicate snapshot at current/later generation | NO-OP | Sec 2.6 IO3 (Blocker I) |

> `ACCEPTED_SEMANTIC` All 14 scenarios are consistent with Sec 1-2
> rules; each maps to exactly one transition (or the ordered pair
> defined in Sec 1.2 rule 4) and the correct journal event (or none).

### 4.2 Counterexamples considered  [ACCEPTED_SEMANTIC]

| # | Attack | Blocked by | Evidence |
| --- | --- | --- | --- |
| a | Delete on PARTIAL snapshot | Sec 1.3.2 V1, Sec 2.2 | INV-004 |
| b | Treat single MISSING as deleted | INV-003, Sec 1.3.1 | principle 3 |
| c | Reverse CONFIRMED_REMOVED by mutating journal | Sec 1.3.4, J5 | append-only |
| d | Write journal from provider delta directly | J7, DO NOT | principle 2, 8 |
| e | Make two racing reconciles both commit | CAS, Sec 2.5/2.6 | Gate 1A C1.3 |
| f | Replay after canonical advanced -> not a no-op | Sec 2.4 | correct behavior |
| g | Ambiguous identity -> CONFLICT, no mutation | Sec 1.3.3, 2.1 | principle 5 |
| h | Kernel re-lists Provider for removal validation | Sec 1.3.2 V2b (Blocker E) | Gate 1A boundary |
| i | Use first MISSING observation as its own confirmation | Sec 1.3.2 V2c (Blocker E) | principle 3 |
| j | Rewrite Journal history to repair | J5, J6, Sec 3.4 (Blocker G) | append-only |
| k | Provider wall-clock as sole ordering source | Sec 2.6 IO7 (Blocker I) | principle 6 |
| l | Older input overwrites newer committed input | Sec 2.6 IO2/IO4 (Blocker I) | principle 1 |

> `ACCEPTED_SEMANTIC` No counterexample violates a hard rule; every
> attack is refused by an explicit `ACCEPTED_SEMANTIC` rule.

### 4.3 Contract-consistency closed loop  [ACCEPTED_SEMANTIC]

| # | Closed-loop check | Status | Evidence |
| --- | --- | --- | --- |
| a | Every `RESOLVED` entry reaches ADD/UPDATE/RENAME/MOVE/UNCHANGED | covered | Sec 1.2 rule 3/4 |
| b | Every `UNRESOLVED` entry reaches CONFLICT | covered | Sec 1.2 rule 2 |
| c | Every `COMPLETE`-governed MISSING can reach REMOVAL_CANDIDATE/CONFIRMED_REMOVED; every `PARTIAL`-governed MISSING cannot | covered | Sec 1.3.1 C1, INV-004 |
| d | Every externally-visible canonical-mutating transition (ADD/UPDATE/RENAME/MOVE/CONFIRMED_REMOVED) produces exactly one journal event; every non-mutating transition produces none | covered | Sec 3.1, J1, J4 |
| e | Every journal event corresponds to a committed transition | covered | J1 |
| f | Canonical-wins on Journal disagreement; Journal history append-only; repair via corrective append or derived-projection rebuild | covered | J5, J6, Sec 3.4 (Blocker G) |
| g | MISSING/REMOVAL_CANDIDATE are NOT Consumer-visible canonical state; only ResourcePresence is | covered | Sec 1.3 state-field split (Blocker F) |
| h | RENAME/MOVE + UPDATE composite has frozen semantic (ordered pair) | covered | Sec 1.2 rule 4 (Blocker H) |
| i | Per-root Input Ordering defined (IO1-IO7) | covered | Sec 2.6 (Blocker I) |
| j | Removal validation boundary defined (V2a-V2d); Kernel does not touch Provider | covered | Sec 1.3.2 (Blocker E) |
| k | No DO NOT scope crossed (identity -> Worker A; completeness -> Worker B; schema/SQL/Query API/journal persistence -> Gate 1C; product code/collector/scanner -> out of scope) | covered | Self-check against DO NOT |

> `ACCEPTED_SEMANTIC` Closed loop verified: the four contracts
> (Identity, Completeness, Reconcile, Journal) compose with no gap and
> no overlap. The Worker performs this analysis inline; no subagent is
> invoked.

---

## 5. Consolidated tags

| Conclusion | Tag |
| --- | --- |
| Reconcile is a pure function of (canonical@G, snapshot, identity, completeness) into a transition set; Kernel decides, Store persists | `ACCEPTED_SEMANTIC` |
| All 10 transition types defined and partitioning (ADD, UPDATE, RENAME, MOVE, MISSING, REMOVAL_CANDIDATE, CONFIRMED_REMOVED, CONFLICT, UNCHANGED, REJECTED) | `ACCEPTED_SEMANTIC` |
| Classification precedence is total and pairwise exclusive | `ACCEPTED_SEMANTIC` |
| Missing -> Removal lifecycle: MISSING -> REMOVAL_CANDIDATE (criteria C1-C3) -> CONFIRMED_REMOVED (validation V1-V3) | `ACCEPTED_SEMANTIC` |
| CONFIRMED_REMOVED is not reversed; reappearance is a fresh ADD | `ACCEPTED_SEMANTIC` |
| Hard rules INV-003 (missing != deleted), INV-004 (incomplete blocks destructive), INV-013 (uncommitted reconcile preserves truth) | `ACCEPTED_SEMANTIC` |
| Failure Model covers all 7 scenarios; every path preserves previous canonical truth | `ACCEPTED_SEMANTIC` |
| Idempotency: same snapshot replay against same generation = NO CHANGE, no event, no generation bump | `ACCEPTED_SEMANTIC` |
| Change Journal: 5 event types, only committed canonical transitions, append-only, canonical-wins on repair | `ACCEPTED_SEMANTIC` |
| Journal != provider native delta != snapshot diff (three distinct concepts, principle 8) | `ACCEPTED_SEMANTIC` |
| Provider delta must not directly write the Journal (J7) | `ACCEPTED_SEMANTIC` |
| Worker Scenario Matrix present (scenarios, counterexamples, contract-consistency closed loop); no subagent invoked | `ACCEPTED_SEMANTIC` |
| Composite RENAME/MOVE + UPDATE split when both path and attributes change | `ACCEPTED_SEMANTIC` (ordered pair: path-change then UPDATE, same generation; Blocker H) |
| Exact `removal_grace_period`, `min_consecutive_complete_missing` thresholds | `CANDIDATE` (configuration / per-root policy) |
| Stale-generation retry vs abort policy | `CANDIDATE` (configuration) |
| Removal validation mechanism (concrete scheduling, independence check, evidence persistence) | `DEFERRED_TO_GATE1C`; validation boundary (V2a-V2d: evidence sources, Kernel does not touch Provider, independence, mechanism deferred) is `ACCEPTED_SEMANTIC` (Blocker E) |
| Journal repair mechanism (concrete persistence of corrective event / derived-projection rebuild) | `DEFERRED_TO_GATE1C`; semantic constraint (canonical wins; Journal history append-only; repair via corrective append or derived-projection rebuild only) is `ACCEPTED_SEMANTIC` (Blocker G) |
| MISSING is NOT Consumer-visible canonical state; ResourcePresence (PRESENT/REMOVED) vs RemovalEvidenceState (NONE/MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT/REMOVAL_CANDIDATE) split; Journal records only externally visible canonical transitions | `ACCEPTED_SEMANTIC` (Blocker F) |
| Per-root Input Ordering (IO1-IO7): Kernel-owned admission sequence, no out-of-order overwrite, duplicate=no-op, stale=STALE_INPUT, CAS is enforcement not ordering, no provider wall-clock as sole source | `ACCEPTED_SEMANTIC` (Blocker I) |
| Journal event payload schema, persistence, projection rebuild protocol | `DEFERRED_TO_GATE1C` (DO NOT: journal persistence/event schema is Gate 1C) |

No conclusion is tagged `REJECTED`. No `REJECTED` line is needed.

---

## DEFERRED_TO_GATE1C

- Journal event payload schema, physical persistence, sequence
  strategy, and projection rebuild/catch-up protocol (DO NOT: journal
  persistence/event schema is Gate 1C).
- Journal repair mechanism details (concrete persistence of corrective
  event vs. derived-projection rebuild) -- the canonical-wins
  *semantic* and the append-only-history *semantic* are accepted here
  (J5, J6, Sec 3.4, Blocker G); the *mechanism* is Gate 1C. The
  canonical Journal history is never rewritten (ACCEPTED_SEMANTIC);
  only corrective appends or derived-projection rebuilds are permitted.
- Removal validation mechanism details (concrete scheduling of
  independent confirming snapshots, independence check, evidence
  persistence) -- the validation *boundary* (V2a-V2d) is accepted
  here (Blocker E); the *mechanism* is Gate 1C.
- Per-root Input Ordering mechanism details (admission-sequence
  persistence, duplicate-detection mechanism) -- the ordering
  *semantic* (IO1-IO7) is accepted here (Blocker I); the *mechanism*
  is Gate 1C.
- Intra-generation event sequence numbering scheme for the
  RENAME/MOVE + UPDATE ordered pair (Blocker H) -- the semantic
  ordering (path-change before attribute-change) is accepted here;
  the persistence scheme is Gate 1C.
- Runtime test execution against product code (no product code at this
  gate per DO NOT).

---

## POST_MVP / DEFERRED_UNSCHEDULED

There is no Gate 1D. Anything not fixed in Gate 1A/1B/1C is routed
here. Examples (non-exhaustive): cross-root global ordering,
multi-cluster replication semantics, online schema evolution, and
other production-scale concerns are `POST_MVP` / `DEFERRED_UNSCHEDULED`
and out of scope for this document.

---

## Self-check against DO NOT

| Constraint | Status |
| --- | --- |
| Do NOT design identity matching (Worker A) | Met -- identity resolution is consumed as an input; only `RESOLVED`/`UNRESOLVED` branching is defined. |
| Do NOT design completeness acceptance (Worker B) | Met -- completeness is consumed as an input; only `COMPLETE`/`PARTIAL`/`REJECTED` gating is defined. |
| Do NOT design PostgreSQL schema/SQL/migration/transaction implementation | Met -- no schema, SQL, migration, or ORM appears; Store atomicity is cited as a Gate 1A boundary. |
| Do NOT design exact Query API | Met -- Query Contract is not designed here (Gate 1A C3 deferred it to Gate 1B read API; this doc is reconcile/journal semantics only). |
| Do NOT design journal persistence/event schema (Gate 1C) | Met -- journal *semantics* (5 events, rules) are defined; payload schema and persistence are deferred to Gate 1C. |
| Do NOT write product code | Met -- document only. |
| Do NOT select final Collector | Met -- Collector is referenced as the snapshot source; no selection. |
| Do NOT design Scanner checkpoint/resume | Met -- not discussed (principle 10: not Kernel's default). |
| Do NOT let provider delta directly write Journal | Met -- explicitly prohibited as J7 (`ACCEPTED_SEMANTIC`). |

## Self-check against MUST DEFINE

| Requirement | Answered in |
| --- | --- |
| All 10 transition types defined | Sec 1.1 |
| Missing -> Removal lifecycle with criteria | Sec 1.3 (1.3.1, 1.3.2, 1.3.3, 1.3.4) |
| Failure Model covers all 7 scenarios | Sec 2.1-2.7 (consolidated 2.8) |
| Change Journal semantics (5 event types) | Sec 3.1 |
| Hard rules enforced (missing != deleted, incomplete blocks destructive) | Sec 1.4 (INV-003, INV-004, INV-013) |
| Worker Scenario Matrix present (no subagent) | Sec 4 |
| Every rule tagged and evidence-cited | Sec 5 + inline tags throughout |
| Idempotency | Sec 2.4 |
| All failure paths preserve previous canonical truth | Sec 2.8 |

## Self-check against DONE WHEN

| Done-when clause | Status |
| --- | --- |
| All 10 transition types defined | Done (Sec 1.1) |
| Missing -> Removal lifecycle defined with criteria | Done (Sec 1.3) |
| Failure Model covers all 7 failure scenarios | Done (Sec 2.1-2.7) |
| Change Journal semantics defined (5 event types) | Done (Sec 3.1) |
| Hard rules enforced (missing != deleted, incomplete blocks destructive) | Done (Sec 1.4) |
| Worker Scenario Matrix present (no subagent) | Done (Sec 4) |
| No DO NOT violations | Done (Self-check against DO NOT) |
---

## 6. Cross-Worker Contract Mapping (Foreman consolidation — resolves CWA-5, CWA-6)

> This section is added by the Foreman to resolve cross-worker consistency
> issues: CWA-5 (STALE/SUSPICIOUS not in Worker C input contract) and
> CWA-6 (FAILED vs REJECTED naming mismatch). Updated for rework: Worker B
> added freshness and assurance dimensions; Worker C added STALE_INPUT and
> split canonical state into ResourcePresence + RemovalEvidenceState.

### 6.1 Completeness acceptance state mapping — ACCEPTED_SEMANTIC

Worker B defines 5 acceptance states: COMPLETE, PARTIAL, FAILED, STALE,
SUSPICIOUS. Worker C's reconcile state machine branches on 3 input values:
COMPLETE, PARTIAL, REJECTED. The canonical mapping is:

| Worker B acceptance state | Worker C input (CompletenessClass) | Reconcile mode |
|---|---|---|
| COMPLETE | COMPLETE | destructive (add + update + remove) |
| PARTIAL | PARTIAL | additive-only (add + update, NO remove) |
| FAILED | REJECTED | no reconcile |
| STALE | PARTIAL | additive-only (treat as PARTIAL) |
| SUSPICIOUS | PARTIAL | additive-only (treat as PARTIAL) |

### 6.2 Identity result mapping — ACCEPTED_SEMANTIC

Worker A defines 4 identity result states: MATCHED, NEW_RESOURCE,
UNRESOLVED, CONFLICT. Worker C's reconcile state machine consumes these
directly:

| Worker A identity result | Worker C transition |
|---|---|
| MATCHED | UPDATE / RENAME / MOVE / UNCHANGED (Sec 1.2 rule 4) |
| NEW_RESOURCE | ADD (Sec 1.2 rule 3) |
| UNRESOLVED | CONFLICT (Sec 1.2 rule 2) |
| CONFLICT | CONFLICT (Sec 1.2 rule 2/6) |

### 6.3 Canonical state model alignment — ACCEPTED_SEMANTIC

Worker C rework (Blocker F) split the canonical record into:
- ResourcePresence (PRESENT / REMOVED) — Consumer-visible canonical state
- RemovalEvidenceState (NONE / MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT / REMOVAL_CANDIDATE) — Kernel-internal lifecycle evidence

Worker A's CanonicalResource.status (Sec 1.4) aligns: PRESENT maps to
ResourcePresence=PRESENT; MISSING/REMOVAL_CANDIDATE are
RemovalEvidenceState, not ResourcePresence. The Journal records only
ResourcePresence transitions and content mutations, not
RemovalEvidenceState changes.

### 6.4 Move/Removal horizon alignment — ACCEPTED_SEMANTIC

Worker A rework (Blocker A4, Sec 2.4) defines:
removal_grace_period >= move_recognition_horizon.

Worker C's Missing→Removal lifecycle (Sec 1.3) must respect this: a
resource within the move_recognition_horizon MUST NOT reach
CONFIRMED_REMOVED. Worker C's V1 (completeness gate) and C2 (time:
now - missing_since >= removal_grace_period) together enforce this when
the horizon relationship holds.
