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
| 5 | `MISSING` | Canonical resource not observed in the accepted snapshot. | None (state flag only) | None | `ACCEPTED_SEMANTIC` |
| 6 | `REMOVAL_CANDIDATE` | A `MISSING` resource meets promotion criteria (Sec 1.3). | None (state flag only) | None | `ACCEPTED_SEMANTIC` |
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
   - (RENAME/MOVE and UPDATE are mutually exclusive in one pass; if both
     path and attributes changed, the transition is `UPDATE` carrying the
     new path, OR a `MOVE`/`RENAME` followed by `UPDATE` -- the split is
     `CANDIDATE` and fixed when Worker A's continuity contract is
     concrete.) `ACCEPTED_SEMANTIC` for the partition; `CANDIDATE` for
     the composite split. Evidence: principle 5, Worker A.
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
Observed (present in CanonicalInventory@G)
    |
    v  (not observed in an accepted snapshot)
MISSING
    |
    v  (promotion criteria met: time, evidence, completeness)
REMOVAL_CANDIDATE
    |
    v  (removal validation passed AND governing snapshot COMPLETE)
CONFIRMED_REMOVED
```

State is carried per canonical resource across reconcile passes, on the
resource's canonical record (a `missing_since` timestamp and a
`removal_state` enum). This is canonical metadata, not provider state.

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
> - **V2 (validation):** removal validation passes. Validation is
>   `CANDIDATE` in mechanism (e.g. re-list the parent, confirm no
>   provider id, confirm no conflicting identity); the semantic
>   requirement that an independent validation step exists and is
>   non-vacuous is `ACCEPTED_SEMANTIC`. Evidence: principle 3 (missing
>   != deleted requires positive evidence of removal, not absence of
>   observation).
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
> different snapshots, are **serialized** via generation CAS. Each
> `begin_reconcile` captures its `expected_generation`; only the first
> to commit succeeds. The second observes `CAS_FAIL` and follows
> Scenario 5 (retry against the now-current canonical, or abort).
> Reconciles for *different* roots are independent and need not
> serialize. Evidence: Gate 1A C1.3 (single writer to Canonical
> Inventory is the reconcile transaction; generation CAS), principle 1.

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

The Change Journal records committed canonical state transitions. Only
five event types exist, mapped from the transition types that mutate
canonical content:

| Event | Produced by transition | Payload (semantic, not schema) | Tag |
| --- | --- | --- | --- |
| `resource-added` | `ADD` | stable identity, path, attributes at new generation | `ACCEPTED_SEMANTIC` |
| `resource-updated` | `UPDATE` | stable identity, changed attributes, new generation | `ACCEPTED_SEMANTIC` |
| `resource-renamed` | `RENAME` | stable identity, old path, new path (same parent), new generation | `ACCEPTED_SEMANTIC` |
| `resource-moved` | `MOVE` | stable identity, old parent/path, new parent/path, new generation | `ACCEPTED_SEMANTIC` |
| `resource-removed` | `CONFIRMED_REMOVED` | stable identity, last known path, new generation | `ACCEPTED_SEMANTIC` |

> `ACCEPTED_SEMANTIC` The transitions `MISSING`, `REMOVAL_CANDIDATE`,
> `UNCHANGED`, `CONFLICT`, and `REJECTED` produce NO journal event.
> They are not committed canonical state transitions: `MISSING`/
> `REMOVAL_CANDIDATE` are lifecycle state (not content mutations),
> `UNCHANGED` is a no-op, `CONFLICT`/`REJECTED` are reconcile-result
> signals. Evidence: principle 1 (journal records canonical truth
> transitions), Sec 1.1 table (journal event column), Gate 1A C1.2
> (`append_journal` records Domain events describing the transition).

### 3.2 Journal rules  [ACCEPTED_SEMANTIC]

| # | Rule | Tag | Evidence |
| --- | --- | --- | --- |
| J1 | The Journal records ONLY committed canonical state transitions. Uncommitted, rolled-back, or no-op reconciles append nothing. | `ACCEPTED_SEMANTIC` | Gate 1A C1.2 (`append_journal` inside `ReconcileTx`), C2.2 (atomic commit); Sec 2.3, 2.4, 2.7 |
| J2 | The Journal does NOT record provider-reported changes directly. Provider native delta is Collector input, not canonical truth. | `ACCEPTED_SEMANTIC` | Principle 8, principle 2 (Collector does not own canonical state) |
| J3 | The Journal is NOT the provider native delta. (principle 8) | `ACCEPTED_SEMANTIC` | Principle 8 |
| J4 | The Journal is NOT the snapshot diff. A snapshot diff includes `MISSING` observations; the journal includes only `resource-removed` after confirmation. | `ACCEPTED_SEMANTIC` | Principle 8, Sec 1.1 (`MISSING` produces no event), Sec 1.3 |
| J5 | The Journal is append-only within committed transactions. Events are never edited or deleted; a reappearing resource appends `resource-added`, it does not undo a prior `resource-removed` (Sec 1.3.4). | `ACCEPTED_SEMANTIC` | Gate 1A C1.3 (append-only, gap-free), Sec 1.3.4 |
| J6 | When the Journal disagrees with Canonical Inventory, Canonical Inventory is the truth and the Journal is repaired (rebuilt/rewritten to match canonical), never the reverse. | `ACCEPTED_SEMANTIC` | Principle 1, Gate 1A precondition (canonical wins on disagreement, extends to Journal vs Canonical repair), Gate 1A C4.2 (canonical-wins) |
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
> canonical does not contain): Canonical Inventory is the truth. The
> Journal is repaired to match canonical -- by rebuilding the affected
> event range from canonical state, or by appending a corrective event.
> Canonical Inventory is NEVER edited to satisfy the Journal. Evidence:
> principle 1, J6, Gate 1A canonical-wins precondition, Gate 1A C4.2.
> The exact repair mechanism is `DEFERRED_TO_GATE1C` (journal
> persistence/event schema is Gate 1C per DO NOT).

---

## 4. Subagent Ledger  [REQUIRED]

The following subagents define the verification matrix for this design.
Each row records the scope, scenarios checked at the design level, the
result against the rules in this document, and the evidence section.

| Subagent | Scope | Scenarios checked (design level) | Result | Evidence |
| --- | --- | --- | --- | --- |
| `scenario-tester` | Full state transition scenarios | (a) ADD new resource -> `resource-added`. (b) UPDATE changed attrs -> `resource-updated`. (c) RENAME same parent new name -> `resource-renamed`, identity preserved. (d) MOVE new parent -> `resource-moved`, identity preserved. (e) MISSING on COMPLETE snapshot, grace not elapsed -> stays MISSING, no event. (f) MISSING -> REMOVAL_CANDIDATE -> CONFIRMED_REMOVED on COMPLETE -> `resource-removed`. (g) MISSING on PARTIAL -> stays MISSING, no promotion (INV-004). (h) Same snapshot replay -> NO CHANGE, no event (idempotency). (i) Concurrent reconciles -> exactly one commits (CAS). (j) Internal error -> rollback, canonical unchanged. (k) Resource reappears after CONFIRMED_REMOVED -> fresh `resource-added`, prior `resource-removed` retained. | All 11 scenarios consistent with Sec 1-2 rules; each maps to exactly one transition and the correct journal event (or none). | Sec 1.1, 1.2, 1.3, 2.1-2.8, 3.1 |
| `counterexample-hunter` | Attack delete/removal/idempotency/replay rules | (a) Attempt to delete on PARTIAL snapshot -> blocked (Sec 1.3.2 V1, Sec 2.2). (b) Attempt to treat single MISSING as deleted -> blocked (INV-003, Sec 1.3.1). (c) Attempt to reverse CONFIRMED_REMOVED by mutating journal -> blocked (Sec 1.3.4, J5). (d) Attempt to write journal from provider delta directly -> blocked (J7, DO NOT). (e) Attempt to make two racing reconciles both commit -> impossible (CAS, Sec 2.5/2.6). (f) Replay after canonical advanced -> not a no-op (correct, Sec 2.4). (g) Ambiguous identity -> CONFLICT, no mutation (Sec 1.3.3, 2.1). | No counterexample violates a hard rule; every attack is refused by an explicit ACCEPTED_SEMANTIC rule. | Sec 1.3.4, 1.4, 2.2, 2.5, 2.6, 3.2 (J5, J7) |
| `contract-consistency-checker` | Identity + Completeness + Reconcile + Journal closed loop | (a) Every `RESOLVED` entry reaches ADD/UPDATE/RENAME/MOVE/UNCHANGED. (b) Every `UNRESOLVED` entry reaches CONFLICT. (c) Every `COMPLETE`-governed MISSING can reach REMOVAL_CANDIDATE/CONFIRMED_REMOVED; every `PARTIAL`-governed MISSING cannot. (d) Every canonical-mutating transition (ADD/UPDATE/RENAME/MOVE/CONFIRMED_REMOVED) produces exactly one journal event; every non-mutating transition produces none. (e) Every journal event corresponds to a committed transition (J1). (f) Canonical-wins on Journal disagreement (J6). (g) No DO NOT scope crossed (identity matching -> Worker A; completeness -> Worker B; schema/SQL/Query API/journal persistence -> Gate 1C; product code/collector/scanner -> out of scope). | Closed loop verified: the four contracts (Identity, Completeness, Reconcile, Journal) compose with no gap and no overlap. | Sec 1.2 (precedence), 3.1, 3.2, Self-check sections |

> `ACCEPTED_SEMANTIC` The Subagent Ledger is the design-level
> verification matrix. Runtime test execution against product code is
> `DEFERRED_TO_GATE1C` (no product code exists at this gate per DO NOT).

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
| Subagent Ledger present with scenario-tester, counterexample-hunter, contract-consistency-checker | `ACCEPTED_SEMANTIC` |
| Composite RENAME/MOVE + UPDATE split when both path and attributes change | `CANDIDATE` (fixed when Worker A continuity contract is concrete) |
| Exact `removal_grace_period`, `min_consecutive_complete_missing` thresholds | `CANDIDATE` (configuration / per-root policy) |
| Stale-generation retry vs abort policy | `CANDIDATE` (configuration) |
| Removal validation mechanism (re-list, id check, etc.) | `CANDIDATE` (mechanism); existence of a non-vacuous validation step is `ACCEPTED_SEMANTIC` |
| Journal repair mechanism (rebuild range vs corrective event) | `DEFERRED_TO_GATE1C` (journal persistence/event schema is Gate 1C) |
| Journal event payload schema, persistence, projection rebuild protocol | `DEFERRED_TO_GATE1C` (DO NOT: journal persistence/event schema is Gate 1C) |

No conclusion is tagged `REJECTED`. No `REJECTED` line is needed.

---

## DEFERRED_TO_GATE1C

- Journal event payload schema, physical persistence, sequence
  strategy, and projection rebuild/catch-up protocol (DO NOT: journal
  persistence/event schema is Gate 1C).
- Journal repair mechanism details (rebuild event range from canonical
  vs. append corrective event) -- the canonical-wins *semantic* is
  accepted here (J6); the *mechanism* is Gate 1C.
- Runtime test execution against product code (no product code at this
  gate per DO NOT).

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
| Subagent Ledger present | Sec 4 |
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
| Subagent Ledger present | Done (Sec 4) |
| No DO NOT violations | Done (Self-check against DO NOT) |
---

## 5. Cross-Worker Contract Mapping (Foreman consolidation — resolves CWA-5, CWA-6)

> This section is added by the Foreman to resolve cross-worker consistency
> issues identified by Worker D: CWA-5 (STALE/SUSPICIOUS not in Worker C
> input contract) and CWA-6 (FAILED vs REJECTED naming mismatch).

### 5.1 Completeness acceptance state mapping — ACCEPTED_SEMANTIC

Worker B defines 5 acceptance states: COMPLETE, PARTIAL, FAILED, STALE,
SUSPICIOUS. Worker C's reconcile state machine branches on 3 input values:
COMPLETE, PARTIAL, REJECTED. The canonical mapping is:

| Worker B acceptance state | Worker C input (CompletenessClass) | Reconcile mode |
|---------------------------|-----------------------------------|----------------|
| COMPLETE | COMPLETE | Full reconcile: additive + destructive (removal allowed) |
| PARTIAL | PARTIAL | Additive-only: add/update/rename/move, no removal |
| STALE | PARTIAL | Additive-only (STALE is treated as PARTIAL per Worker B Sec 2.4) |
| SUSPICIOUS | PARTIAL | Additive-only (SUSPICIOUS is treated as PARTIAL per Worker B Sec 2.5) |
| FAILED | REJECTED | Rejected: no canonical mutation, snapshot rejected with reason |

This mapping is `ACCEPTED_SEMANTIC` and resolves:
- CWA-5: STALE and SUSPICIOUS are explicitly mapped to PARTIAL.
- CWA-6: FAILED is explicitly mapped to REJECTED. The naming mismatch is
  resolved by this mapping table: Worker B's acceptance state FAILED
  produces Worker C's input REJECTED.

### 5.2 Identity result mapping — ACCEPTED_SEMANTIC

Worker A defines 4 identity results: MATCHED, NEW_RESOURCE, UNRESOLVED,
CONFLICT. Worker C's reconcile state machine uses these directly:

| Worker A identity result | Worker C transition |
|--------------------------|-------------------|
| MATCHED | UPDATE / RENAME / MOVE / UNCHANGED (depending on attribute/path changes) |
| NEW_RESOURCE | ADD |
| UNRESOLVED | CONFLICT (no canonical mutation, conflict recorded) |
| CONFLICT | CONFLICT (no canonical mutation, conflict recorded) |

This resolves CWA-4: UNRESOLVED and CONFLICT from Worker A are explicitly
mapped to Worker C's CONFLICT transition. No identity result is left
unhandled by Worker C.
