# Gate 1A Worker D -- Independent Boundary Attack

> Role: Architecture Boundary Adversarial Reviewer
> Author: Worker D (w04)
> Date: 2026-09-23
> Baseline: 6190d00
> Status: BOUNDARY_ATTACK_COMPLETE
> Output: `.work/gate1a/W-D-BOUNDARY-ATTACK.md`
>
> Scope rule: this document is an independent adversarial review of the
> boundary designs produced by Worker A (Kernel), Worker B (Collector),
> and Worker C (Store + Consumer). It does NOT design architecture, does
> NOT write product code, does NOT select a Collector, and does NOT
> design algorithms. Every attack point is resolved to exactly one of
> PASS / VIOLATION / WARNING with cited evidence from A/B/C.

---

## 0. Attack methodology

I read all three reports (W-A-KERNEL-BOUNDARY.md,
W-B-COLLECTOR-CONTRACT.md, W-C-STORE-CONSUMER-BOUNDARY.md) in full and
attacked each of the eight mandatory attack surfaces (D1-D8). For each
attack I attempted to construct a concrete scenario in which the accepted
boundary would be violated under a plausible future change (adapter swap,
concurrent collector, partial failure, naming drift, overlapping roots).
A point is PASS only when no scenario produces a violation. A point is
VIOLATION when a current accepted boundary directly contradicts an
accepted principle. A point is WARNING when the current design does not
violate a principle but contains an ambiguity that a future gate or
implementation could resolve into a violation.

---

## D1. Kernel eating Scanner responsibility

### Attack

Worker A accepts "Snapshot Acceptance" (A1.4) and "Safe Reconcile"
(A1.5) as Kernel responsibilities, and in A5-B2 states the Kernel "MAY
still apply additional heuristics" beyond the Collector's completeness
flag. Worker B in B3.3 item 5 lists "expected entry count range" as
Kernel-owned historical context for completeness judgment. I attacked
both as candidate Scanner-side concerns leaking into the Kernel.

### Evidence examined

- W-A A5-B2: "If the flag says complete, the Kernel MAY still apply
  additional heuristics (deferred to Gate 1B)."
- W-B B3.3 item 5: "Historical context (Kernel-owned): prior snapshot
  generation, expected entry count range, root lifecycle state."
- W-A A2 table: "Scanner checkpoint / resume | REJECTED | Collector /
  external", "Provider listing / enumeration | REJECTED | Collector".
- W-A A5-B5: "Scanner checkpoint / resume -- REJECTED. Correct owner:
  Collector / external."

### Verdict: WARNING

The accepted Kernel responsibilities (A1.4 Snapshot Acceptance, A1.5
Safe Reconcile) are correctly proven non-delegable and do not themselves
include traversal, pagination, or checkpoint. A2 and A5-B5 correctly
REJECT scanner checkpoint from the Kernel (principle 10). No VIOLATION.

However, two ambiguities risk becoming scanner-leak in Gate 1B:

1. W-B B3.3 item 5 "expected entry count range" is ambiguous. If it
   means "the entry count of the last committed Canonical Inventory
   partition" it is Kernel-internal state and safe. If it means a
   statistical model of expected counts maintained by the Kernel
   (trend, confidence interval), the Kernel would be performing a
   scanner-statistics role. The phrase "range" leans toward the
   statistical interpretation. Gate 1B must pin this to "derived from
   prior committed canonical state only, no independent statistical
   model".
2. W-A A5-B2 "additional heuristics" is undefined and deferred. If a
   heuristic triggers a re-scan, the Kernel would be invoking Collector
   traversal, crossing the boundary. Gate 1B must constrain completeness
   heuristics to "read-only inspection of submitted evidence + prior
   canonical state; never triggers re-traversal".

No current accepted boundary contradicts an accepted principle. The risk
is in the deferred detail. WARNING.

---

## D2. Collector secretly owning canonical truth

### Attack

Worker B produces SnapshotEntry (B2) and snapshot-level evidence (B3),
and explicitly REJECTs canonical truth ownership in B4. I attacked the
fields that could become de-facto canonical truth: `traversal_status`
(B3.1), `parent_ref` (B2), `adapter_generation` (B3.1), and the
`entry_count`/`byte_count` sanity fields (B3.4).

### Evidence examined

- W-B B3.2: "`success`: ... NOT proof of provider-complete snapshot".
- W-B B3.3: "The Collector does NOT compute or set a `complete=true`
  verdict (B4)."
- W-B B2: "`parent_ref | required | reference to parent entry or root
  (tree reconstruction)`".
- W-B B4: "Canonical `resource_id` assignment | Kernel | REJECTED (from
  Collector)".
- W-B B5.1: AList `traversal_status` is WEAK; "adapter may incorrectly
  report `success`".
- W-B B3.4: "`entry_count` and `byte_count` are weak sanity check
  material ... never sufficient alone to authorize destructive
  reconcile (INV-004)."

### Verdict: WARNING

B4 correctly and exhaustively REJECTs canonical identity, confirmed
removal, generation, change type, completeness acceptance, identity
continuity, conflict resolution, Change Journal, and direct inventory
mutation from the Collector. B3.3 explicitly reserves the `complete=true`
verdict for the Kernel. No VIOLATION of principle 1 or principle 2.

However, two field-level ambiguities risk becoming de-facto truth:

1. W-B B2 `parent_ref` is REQUIRED and described as "reference to parent
   entry or root". The representation is CANDIDATE ("opaque id vs path
   string"). If the opaque id is ever set to the parent's canonical
   `resource_id`, the Collector would be emitting canonical identity,
   contradicting W-B B4 ("Canonical `resource_id` assignment | Kernel").
   B2 does not state "`parent_ref` is Collector-local, NOT canonical
   resource_id". Gate 1B must freeze this to "Collector-local reference
   (entry index or observed path), never canonical resource_id".
2. W-B B5.1 documents that AList may incorrectly report
   `traversal_status = success` when `storage.List` silently swallowed an
   error. This is not Collector owning truth, but Collector mis-reporting
   evidence. The Kernel defense (W-A A1.4, W-A A5-B2 "does NOT blindly
   trust the flag") is in place, so this is a residual robustness risk,
   not a boundary violation. WARNING only.

No current accepted boundary lets the Collector own canonical truth.
WARNING.

---

## D3. Store Interface leaking PostgreSQL detail

### Attack

Worker C states the Store Interface "contains no PostgreSQL term"
(C1.1, C1.3) but then specifies PostgreSQL semantics in C2.2 including
"pessimistic row lock" and "MVCC". I attacked every operation signature
and every semantic requirement for hidden PostgreSQL coupling.

### Evidence examined

- W-C C1.1: "The Store Interface is expressed in Kernel's Domain
  vocabulary (`RootState`, `CanonicalInventory`, `ResourceEntry`,
  `JournalEvent`, `Generation`) and contains no PostgreSQL term (no
  table, row, column, SQL, ORM, dialect, connection)."
- W-C C1.2 operations: `load_root_state`, `load_canonical_inventory`,
  `load_journal`, `begin_reconcile`, `write_canonical_changes`,
  `append_journal`, `commit`, `rollback`.
- W-C C1.3: "Store Interface leaks no PostgreSQL detail."
- W-C C2.2: "Concurrency protection | Optimistic CAS on generation
  (fail on race) and/or pessimistic row lock ...; MVCC so readers do
  not block the writer."
- W-C C2.3: "PostgreSQL is not the Domain model. The schema is an
  implementation detail of the Store, invisible to Kernel and
  Consumer."

### Verdict: PASS

I inspected every operation in C1.2. All parameter types
(`root_id`, `at_generation`, `expected_generation`, `since_generation`)
and return types (`RootState`, `CanonicalInventory`, `JournalEvents`,
`ReconcileTx`, `CommitResult{new_generation}`) are Domain vocabulary
defined by W-A A1.1-A1.6. No table, row, column, SQL type, ORM session,
or connection appears in the interface.

C2.2's "pessimistic row lock" and "MVCC" appear under C2 "PostgreSQL
Role", which is explicitly the engine-implementation contract, not the
Store Interface. C2.2 states these are semantics "the Store
implementation depends on" and that "if a future engine cannot provide
all of them, the Store implementation must synthesize them or the engine
is not a valid substitute." The Store Interface's optimistic-concurrency
semantic (C1.3 "commit succeeds only if the persisted generation still
equals it, then atomically advances it") is engine-agnostic CAS, not a
PostgreSQL term. Replacing PostgreSQL with any engine providing atomic
CAS + transactional commit changes only the Store implementation; Kernel
and Consumer contracts are unchanged.

No leakage. PASS.

---

## D4. Consumer bypassing Query Contract to modify resources

### Attack

Worker C C3.3 prohibits Consumer mutation of Canonical Inventory. I
attacked every Consumer capability in C3.2 for a hidden write path:
"Request a re-sync be scheduled", "Subscribe to / poll the Journal",
"Own projection state", and cross-Consumer reads.

### Evidence examined

- W-C C3.2: capabilities granted to Consumer (read canonical, read at
  generation, subscribe journal, own projection, request re-sync).
- W-C C3.3: "Directly modify Canonical Inventory | INV-008 ...
  ACCEPTED_BOUNDARY prohibition", "Bypass the Query Contract to read
  ... ACCEPTED_BOUNDARY prohibition", "Write through the Store
  Interface ... ACCEPTED_BOUNDARY prohibition", "Treat its projection
  as authoritative ... ACCEPTED_BOUNDARY prohibition", "Initiate or
  run a reconcile ... ACCEPTED_BOUNDARY prohibition".
- W-C C3.1: "Consumer holds no reference to the Store Interface."
- W-C C4.2: "Derived, not authoritative ... never writes back to
  Canonical Inventory", "Canonical wins on disagreement".

### Verdict: PASS

I examined each Consumer capability for a hidden write path:

1. "Request a re-sync be scheduled" (C3.2) -- Consumer may ask, not run.
   The resulting reconcile is driven by Collector input and Kernel
   decision; Consumer cannot control the outcome. Not a bypass.
2. "Subscribe to / poll the Journal" (C3.2) -- read-only. Not a bypass.
3. "Own projection state" (C3.2) -- Consumer mutates its own projection,
   not Canonical Inventory. C4.2 "never writes back" and "Canonical
   wins" close the feedback loop. Not a bypass.
4. Cross-Consumer reads (Consumer B reads Consumer A's projection) --
   this is a Consumer-to-Consumer integration concern outside Worker C's
   Store/Consumer boundary scope, and even if it occurred it would be a
   read of a non-authoritative projection, not a mutation of Canonical
   Inventory. Not a D4 bypass.

C3.3 exhaustively prohibits direct mutation, bypass reads, write-through,
projection-as-authoritative, and reconcile initiation. C3.1 severs the
Consumer-to-Store-Interface reference. No path allows Consumer mutation
of Canonical Inventory. PASS.

---

## D5. Optional hash / id / delta designed as mandatory

### Attack

Principles 6, 7, and 8 require provider capability, hash, and native
delta to be optional. I attacked every REQUIRED field and every
algorithm constraint in A/B/C for a hidden mandatory hash, provider ID,
or delta requirement.

### Evidence examined

- W-A A3 "Explicitly NOT required": "Content hash | OPTIONAL
  (principle 7)", "Provider native delta | NOT INPUT to Kernel",
  "Stable identity | NOT INPUT to Kernel".
- W-A A1.2 algorithm constraint: "must respect principle 5 (path !=
  identity), principle 7 (hash optional), principle 6 (provider
  capability optional)."
- W-B B2 optionality rules: "`provider_object_id` | NO", "`hash` | NO".
- W-B B2: "REJECTED: Making `hash` or `provider_object_id` mandatory."
- W-B B2: "`parent_ref | required`".
- W-B B1: "`CollectorMode` | `full_snapshot` (default) vs `delta_hint`
  (deferred) ... The Kernel treats all input as snapshot evidence; a
  delta hint is an optimization, not a separate truth path."

### Verdict: WARNING

A3 and B2 correctly keep `hash`, `provider_object_id`, and provider
native delta optional/not-input. A1.2 correctly constrains the deferred
identity algorithm to respect hash-optional and capability-optional. B2
explicitly REJECTs making hash or provider_object_id mandatory. No
VIOLATION of principles 6, 7, or 8.

However, W-B B2 `parent_ref` is REQUIRED. Its description ("reference to
parent entry or root (tree reconstruction)") says it is for tree
reconstruction, not identity, and B4 forbids the Collector from
assigning canonical `resource_id`. But B2 does not explicitly state that
`parent_ref` must NOT be a canonical resource_id. If an implementation
chooses the "opaque id" representation (B2 CANDIDATE) and sets it to the
parent's canonical resource_id, then a REQUIRED field would carry
canonical identity, indirectly making identity mandatory on every entry.
This contradicts principle 5 (path != stable identity) and B4.

Gate 1B must freeze `parent_ref` to "Collector-local reference (entry
index within the snapshot, or observed parent path), never canonical
resource_id". Until then, WARNING.

---

## D6. Change Journal conflated with provider native delta

### Attack

Principle 8 requires three distinct concepts: provider native delta,
snapshot diff, and Canonical Change Journal. I attacked every place in
A/B/C where the three could be conflated, including naming, input
channels, and deferred semantics.

### Evidence examined

- W-A A1.6: "DISTINCT from provider native delta and from snapshot diff
  (principle 8)."
- W-A A4: "NOT provider native delta ... NOT snapshot diff ... IS the
  authoritative record of what the Kernel decided changed."
- W-A A2: "Provider native delta consumption | REJECTED | Collector."
- W-A A3: "Provider native delta | NOT INPUT to Kernel."
- W-B B4: "Canonical Change Journal entries | Kernel | REJECTED (from
  Collector)."
- W-B B1: "`CollectorMode = delta_hint` ... The Kernel treats all input
  as snapshot evidence; a delta hint is an optimization, not a separate
  truth path."
- W-B B3.1: "`adapter_generation` | Optional adapter-reported
  generation/cursor if the provider exposes one (e.g. rclone
   ChangeNotify token). Absent for most providers | DEFERRED_POST_MVP_INCREMENTAL".
- W-B D-DEFER-5: "`adapter_generation` / native delta token semantics |
   DEFERRED_POST_MVP_INCREMENTAL | Native delta != Change Journal (INV-012)."

### Verdict: WARNING

A1.6, A4, A2, A3, and B4 all correctly distinguish the Canonical Change
Journal from provider native delta and snapshot diff. A3 closes the
input channel ("Provider native delta | NOT INPUT to Kernel"). B1
correctly states delta_hint is "an optimization, not a separate truth
path". No VIOLATION of principle 8.

However, two naming ambiguities risk conflation in implementation:

1. W-B B3.1 `adapter_generation` carries the word "generation", the
   same term W-A A1.3 uses for the Kernel's canonical Generation/Epoch
   ("a consistency point in canonical state history"). B3.1 and D-DEFER-5
   semantically separate them (`adapter_generation` is provider cursor,
   Kernel `Generation` is canonical epoch), but an implementer could
   pass `adapter_generation` as `expected_generation` to
   `begin_reconcile`, silently treating a provider cursor as canonical
   epoch. Post-MVP Incremental should rename `adapter_generation` to
   `provider_cursor` or `adapter_delta_token` to eliminate the lexical
   overlap.
2. W-B B1 `CollectorMode = delta_hint` uses "delta" in its name. D-DEFER-1
   defers it to Post-MVP Incremental. If an implementer reads "delta hint" as
   "Change Journal input", they would bypass A3's "NOT INPUT to Kernel"
   rule. B1's clarifying sentence ("The Kernel treats all input as
   snapshot evidence") mitigates this, but the name itself is a risk.
   Post-MVP Incremental should rename to `incremental_hint` or `adapter_delta_opt`.

No current accepted boundary conflates the three concepts. The risk is
naming drift in Post-MVP Incremental. WARNING.

---

## D7. Second copy of resource truth

### Attack

Principle 1 requires Canonical Inventory to be the unique resource
truth. I attacked every store, projection, journal, and partition in
A/B/C for a second authoritative copy: Search DB, Catalog DB, CloudSite
DB, Change Journal, and per-Root partitions.

### Evidence examined

- W-A A1.1: "The authoritative, complete resource inventory ... single
  source of resource truth (principle 1)."
- W-A A1.3: "Roots define what scope a snapshot covers; generations
  provide read-consistency points for Consumers."
- W-A A1.6: "The authoritative record of canonical state transitions."
- W-A A2: "Catalog ... REJECTED | Consumer / external", "CloudSite
  business rules ... REJECTED | Consumer / external".
- W-C C4.1: "Search is a projection, not the Canonical Inventory
  (INV-002)."
- W-C C4.2: "Derived, not authoritative ... Canonical wins on
  disagreement."
- W-C C3.2: "Own projection state ... These are Consumer-owned, NOT
  canonical."
- W-C C1.2: `ReconcileTx.commit()` -- "Atomically commit canonical
  changes + journal append + generation bump."

### Verdict: WARNING

A1.1 establishes Canonical Inventory as the single source of resource
truth. C4.1/C4.2 correctly subordinate Search to Canonical. C3.2
correctly labels Consumer projections as "NOT canonical". A2 correctly
REJECTs Catalog and CloudSite as canonical owners. The Change Journal
(A1.6) is an authoritative record of transitions, not a second resource
inventory, and C1.2 commits it atomically with canonical changes in one
transaction. No VIOLATION of principle 1.

However, two gaps risk producing a second copy:

1. W-A A1.3 defines Roots as "the scope boundary of a canonical
   inventory partition" but does NOT state that Root partitions are
   disjoint. If two Roots are configured with overlapping scopes (e.g.
   the same provider root scanned twice with different prefixes that
   intersect), the same resource would appear in two canonical
   partitions, creating two authoritative copies of that resource --
   a second truth. Gate 1B must add the invariant "Root partitions are
   disjoint; a resource belongs to exactly one Root" or define the
   merge rule for overlapping Roots.
2. W-A A1.6 and W-C C1.2 commit Canonical Inventory and Change Journal
   atomically, so they cannot diverge at commit time. But neither A
   nor C states what happens if they diverge at read time due to a
   storage bug: which one is truth? A1.1 says Canonical Inventory is
   "single source of resource truth", implying Canonical wins, but C's
   "Canonical wins" rule (C4.2) is stated only for Search, not for the
   Change Journal. Gate 1B should extend "Canonical wins on
   disagreement" to cover Change Journal vs Canonical Inventory, so a
   repair routine knows which copy to rebuild.

No current accepted boundary creates a second truth. The risk is an
unstated disjointness invariant and an unstated repair precedence.
WARNING.

---

## D8. Adapter swap forcing Kernel rewrite

### Attack

Principle 6 (provider capability optional) and principle 9 (Kernel does
not bind schema/ORM) require the Kernel to be adapter-neutral. I attacked
every Kernel input field, every provenance use, and every deferred
policy for a hidden binding to AList or rclone specifics, then tested
the swap scenarios AList -> rclone and rclone -> direct provider
adapter.

### Evidence examined

- W-A A3: "Provider-specific metadata | OPTIONAL | The Kernel does not
  bind to provider-specific schemas (principle 9). Provider-specific
  metadata may be attached but is not interpreted by Kernel domain
  logic."
- W-A A3: "Source provenance | REQUIRED | Identifies which Collector
  produced this batch and when. Needed for audit, conflict detection,
  and debugging."
- W-A A2: "Provider authentication | REJECTED | Collector", "Provider
  listing / enumeration | REJECTED | Collector", "Pagination | REJECTED
  | Collector", "Provider native delta consumption | REJECTED |
  Collector".
- W-A A5-B3: "Conflict resolution POLICY ... is a design decision
  deferred to Gate 1B."
- W-B B1: "`SourceRef` | provider kind (alist / openlist / rclone /
  webdav / s3 / local / feed / snapshot-file) plus endpoint/address.
  NOT a stable resource identity."
- W-B B2: "`provider_object_id` | optional | DRIVER_DEPENDENT",
  "`hash` | optional | DRIVER_DEPENDENT", "`metadata` | optional |
  provider-specific bag".
- W-B B5: "Adapter fit analysis (NO selection) ... CANDIDATE: Both are
  viable adapter candidates ... Selection is an Architect decision."

### Verdict: PASS

I traced every Kernel input field for adapter binding:

1. `SourceRef` (B1) carries a provider kind, but it is Collector input,
   not Kernel input. The Kernel receives `source_ref` as provenance
   (A3) for audit/conflict detection, and A3 states provider-specific
   metadata "is not interpreted by Kernel domain logic". Conflict
   detection compares Collector identity, not provider kind. No binding.
2. `provider_object_id` and `hash` (B2) are optional and
   DRIVER_DEPENDENT. A1.2 constrains the deferred identity algorithm to
   work without them. Swapping adapters changes which fields are
   populated, not the Kernel algorithm. No rewrite.
3. `metadata` (B2) is an open bag the Kernel does not interpret (A3).
   No binding.
4. A5-B3 conflict resolution policy is deferred to Gate 1B. As long as
   the policy is expressed in terms of Collector identity and
   generation (not provider kind), it remains adapter-neutral. The
   current boundary does not bind it to provider kind. No rewrite.

Swap scenario AList -> rclone: the only behavioral change is
`traversal_status` reliability (B5.1 AList WEAK, B5.2 rclone STRONGER).
The Kernel defense (A1.4 Snapshot Acceptance, A5-B2 "does NOT blindly
trust the flag") is already in place. The Kernel becomes more
conservative with AList (more rejects), not rewritten. Identity
continuity accuracy changes (rclone has `IDer` on more backends), but
A1.2's algorithm constraint requires it to work without `provider_object_id`,
so the Kernel degrades gracefully, not by rewrite.

Swap scenario rclone -> direct provider adapter: the new adapter must
produce SnapshotEntry + evidence per B1-B3. The Kernel contract (A3/A4)
is unchanged. No rewrite.

PASS.

---

## Consolidated verdict table

| Attack | Surface | Verdict | Violated principle | Cited evidence |
|--------|---------|---------|--------------------|----------------|
| D1 | Kernel eating Scanner responsibility | WARNING | none (risk in deferred heuristics) | W-A A5-B2, W-B B3.3 item 5 |
| D2 | Collector owning canonical truth | WARNING | none (risk in `parent_ref` semantics) | W-B B2, W-B B4, W-B B5.1 |
| D3 | Store Interface leaking PostgreSQL | PASS | none | W-C C1.1, C1.2, C1.3, C2.2, C2.3 |
| D4 | Consumer bypassing Query Contract | PASS | none | W-C C3.1, C3.2, C3.3, C4.2 |
| D5 | Optional hash/id/delta made mandatory | WARNING | none (risk in `parent_ref` REQUIRED) | W-A A3, A1.2, W-B B2, B4 |
| D6 | Change Journal vs native delta conflation | WARNING | none (risk in naming) | W-A A1.6, A4, W-B B1, B3.1, D-DEFER-5 |
| D7 | Second copy of resource truth | WARNING | none (risk in Root disjointness) | W-A A1.1, A1.3, A1.6, W-C C1.2, C4.2 |
| D8 | Adapter swap forcing Kernel rewrite | PASS | none | W-A A3, A2, A5-B3, W-B B1, B2, B5 |

Summary: 0 VIOLATION, 6 WARNING, 2 PASS.

---

## WARNING Disposition (derived from WARNINGs)

The 6 WARNINGs are split by responsibility into the appropriate future
phase. None require re-opening an ACCEPTED_BOUNDARY. All are
clarifications of CANDIDATE or DEFERRED items.

### To Gate 1B (core semantics)

1. (D1) Pin "expected entry count range" to "derived from prior
   committed canonical state only; no independent statistical model".
   Constrain completeness heuristics to "read-only inspection of
   submitted evidence + prior canonical state; never triggers
   re-traversal".
2. (D2/D5) Freeze `parent_ref` to "Collector-local reference (entry
   index within the snapshot, or observed parent path), never canonical
   resource_id".
3. (D7) Add invariant "Root partitions are disjoint; a resource belongs
   to exactly one Root" or define the merge rule for overlapping Roots.
   Extend "Canonical wins on disagreement" to cover Change Journal vs
   Canonical Inventory repair precedence.

### To Post-MVP Incremental

4. (D6) Rename `adapter_generation` to `provider_cursor` or
   `adapter_delta_token`. Rename `CollectorMode = delta_hint` to
   `incremental_hint` or `adapter_delta_opt`.

### To Post-MVP Scanner Resume

5. (D1 partial) Scanner checkpoint/resume is not a Gate 1B concern.
   Blueprint section 18 defers it to post-MVP.

---

## Self-check against DO NOT

| Constraint | Status |
|------------|--------|
| Did not do main design | Honored -- adversarial review only |
| Did not write product code | Honored -- document only |
| Did not select Collector | Honored -- D8 PASS, no selection |
| Did not design algorithm | Honored -- D5/D6 reference deferred constraints, no algorithm designed |

## Self-check against QUALITY

| Requirement | Status |
|--------------|--------|
| Every attack point marked PASS / VIOLATION / WARNING | Honored -- 8/8 resolved |
| VIOLATION references specific A/B/C passage | Honored -- 0 VIOLATION; all WARNINGs cite specific passages |

---

## Conclusion

The three boundary designs (A Kernel, B Collector, C Store+Consumer) are
internally consistent with the twelve accepted principles. No accepted
boundary directly violates an accepted principle (0 VIOLATION). The
design holds under the adapter-swap stress test (D8 PASS), the
Store-leakage stress test (D3 PASS), and the Consumer-bypass stress test
(D4 PASS).

The six WARNINGs are all of the same form: a CANDIDATE or DEFERRED item
whose current wording is ambiguous enough that a future gate or
implementation could resolve it into a violation. None require re-opening
an ACCEPTED_BOUNDARY today. All six are eliminable by adopting the
clarifications listed above, split across Gate 1B (core semantics),
Post-MVP Scanner Resume, and Post-MVP Incremental. The boundary freeze
can proceed with these clarifications registered as pre-conditions.
