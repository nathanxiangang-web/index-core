# Gate 1B Worker A — Domain Model + Stable Identity v1

> Status: DESIGN_MODEL (not implementation)
> Author: Worker A (w01)
> Date: 2026-09-23
> Baseline: 7aa798a
> Branch: architecture/gate1b-core-semantics
> Evidence base: Accepted principles (non-overridable) + Gate 1A boundary
>   (.work/gate1a/W-A-KERNEL-BOUNDARY.md) + D02 research
>   (docs/research/d02/W-A-ALIST-OPENLIST-INDEXING.md)

---

## 0. Accepted Principles (reference — not restated, not overridable)

The ten accepted principles and the Gate 1A frozen boundaries are NOT re-opened
here. Every definition and rule below is derived from them. See
`.work/gate1a/W-A-KERNEL-BOUNDARY.md` for the full boundary proof.

Gate 1A frozen boundaries relied upon in this document:

- Kernel owns: Canonical Inventory, Identity Continuity, Snapshot Acceptance,
  Safe Reconcile, Root/Generation, Change Journal, conflict resolution.
- Collector owns: traversal, SnapshotEntry production, evidence; NOT canonical
  identity.
- parent_ref is Collector-local, never canonical resource_id.
- hash, provider_object_id, native delta are OPTIONAL.
- Stable identity algorithm and rename/move matching algorithm were
  DEFERRED_TO_GATE1B by Gate 1A. This document delivers that v1.

---

## 1. Domain Model

Six first-class domain concepts. Each is a concept contract, NOT an
implementation or schema design (principle 9: Kernel Domain does not bind
schema/ORM). No PostgreSQL table, column, or migration is specified here.

### 1.1 ResourceRoot — ACCEPTED_SEMANTIC

**Definition.** A ResourceRoot is the scope boundary of one partition of the
Canonical Inventory. It defines the universe of resources that one Canonical
Inventory partition is authoritative over. Identity does not cross roots
(scenario 11; see Section 3).

**Fields (concept contract).**

| Field | Required | Rationale |
|-------|----------|-----------|
| root_id | REQUIRED | Stable, Kernel-assigned identifier of the partition. NOT a path. |
| scope_descriptor | REQUIRED | Describes the scope this root covers (e.g., a provider + mount path + account). Opaque to Kernel domain logic; interpreted by Collector selection (deferred to Gate 1C). |
| owning_collector_ref | OPTIONAL | Which Collector is responsible for producing snapshots for this root. May be unassigned during bootstrap. Selection deferred to Gate 1C. |
| generation_cursor | REQUIRED | The current monotonic generation number for this root (see 1.5). Per-root, not global. |

**Partition semantics.** See Section 3 (Root Ownership Resolution A: disjoint
partitions).

**Evidence.** Gate 1A A1.3 (Root / Generation Semantics — ACCEPTED_BOUNDARY).
D02 Q11: AList/OpenList storage mount paths are unique by design; `Parent + Name`
uniquely identifies an index entry within the virtual filesystem
(`internal/op/fs.go:149` / `op/fs.go:76`, `utils.GetFullPath`), confirming that
a scope boundary is a real, enforceable concept.

**Non-overridable constraint.** root_id is NOT a path and is NOT a
provider_object_id. It is a Kernel-assigned canonical partition identifier
(principle 5: path != stable identity).

---

### 1.2 Snapshot — ACCEPTED_SEMANTIC

**Definition.** A Snapshot is a first-class, immutable object representing one
observation of a scope by a Collector. It is the Kernel input unit (Gate 1A A3).
It carries entries, evidence, and metadata. It is NOT canonical state and is NOT
a Change Journal entry (principle 8).

**Fields (concept contract).**

| Field | Required | Rationale |
|-------|----------|-----------|
| snapshot_id | REQUIRED | Kernel-assigned or Collector-provided unique id of this observation. |
| root_id | REQUIRED | Which ResourceRoot partition this snapshot covers (Gate 1A A3: scope identifier required). |
| entries | REQUIRED | The list of SnapshotEntry observed in this scope. May be empty. |
| completeness_flag | REQUIRED | COMPLETE or PARTIAL. Gates Safe Reconcile (principle 4; Gate 1A A1.4, B2). A Collector that cannot determine completeness MUST emit PARTIAL. |
| provenance | REQUIRED | Source provenance: which Collector, when, driver info (Gate 1A A3). |
| observed_at | REQUIRED | Observation timestamp. |
| generation_hint | OPTIONAL | Collector-tracked generation/cursor if available (principles 6, 10). NOT required. |

**Immutability.** A Snapshot, once submitted, is never mutated. Reconcile reads
it; it does not rewrite it. This supports audit and the Change Journal
(Worker C).

**Explicitly NOT.** A Snapshot is NOT canonical state, NOT a provider native
delta (principle 8), and does NOT carry final identity (Gate 1A A3: stable
identity is NOT input to Kernel; Collector provides identity HINTS, Kernel
decides).

**Evidence.** Gate 1A A3 (Kernel Input — ACCEPTED_BOUNDARY). D02 Q4/Q5:
AList/OpenList `Update` receives `objs` from `storage.List` and computes a diff
(`internal/search/build.go:224-234`); this is the snapshot concept, and the
research confirms it lacks completeness protection (Q10), which the
completeness_flag exists to fix.

---

### 1.3 SnapshotEntry — ACCEPTED_SEMANTIC (core fields frozen by Gate 1A)

**Definition.** A SnapshotEntry is one observed resource within a Snapshot. It
carries the identity HINTS and metadata the Collector could obtain. Core fields
are frozen by Gate 1A; optional fields remain optional.

**Fields (concept contract).**

| Field | Required | Rationale / Principle |
|-------|----------|----------------------|
| entry_local_id | REQUIRED | Per-snapshot unique id of this entry. NOT canonical identity. |
| name | REQUIRED | Object name as observed. |
| parent_ref | REQUIRED | Collector-local parent reference (path or ref within the scope). Collector-local, NEVER canonical resource_id (Gate 1A frozen boundary). |
| is_dir | REQUIRED | Directory flag. |
| size | OPTIONAL | Object size in bytes if obtainable. |
| mtime | OPTIONAL | Modification time if obtainable. |
| content_hash | OPTIONAL | Content hash if obtainable. OPTIONAL (principle 7). |
| hash_algorithm | OPTIONAL | Algorithm for content_hash (e.g., sha256). Required iff content_hash present. |
| provider_object_id | OPTIONAL | Provider-side stable object id if the driver exposes one. OPTIONAL (principle 6; Gate 1A: provider capability optional). |
| provider_object_id_scope | OPTIONAL | Namespace/scope of provider_object_id (e.g., "s3-bucket-X"). Required iff provider_object_id present, to disambiguate id spaces across providers. |
| content_type | OPTIONAL | MIME type if obtainable. |
| extra_evidence | OPTIONAL | Additional Collector-obtained evidence (e.g., etag, version). Opaque to Kernel domain identity logic; may be logged/audited. |

**Frozen constraint.** parent_ref is and remains Collector-local. It is an
identity HINT, never the canonical resource_id (Gate 1A frozen boundary;
principle 5).

**Explicitly NOT required (DO NOT compliance).** content_hash and
provider_object_id are OPTIONAL. A SnapshotEntry without either is valid and
must be processed by identity matching (scenarios 2, 8). The Kernel MUST NOT
reject an entry solely for lacking hash or provider_object_id.

**Evidence.** Gate 1A A3 (explicitly NOT required table). D02 Q7: the AList/
OpenList `SearchNode` model carries only `Parent, Name, IsDir, Size`
(`internal/model/search.go:23-28`); there is no provider object ID, ETag, content
hash, or mtime field. This is the negative evidence: a real system operates
without these fields, so the Kernel identity model MUST function without them.

---

### 1.4 CanonicalResource — ACCEPTED_SEMANTIC

**Definition.** A CanonicalResource is the canonical identity-bearing entity in
the Canonical Inventory. It is what a SnapshotEntry becomes when the Kernel
accepts it and assigns (or confirms) canonical identity. It is the unit of
identity continuity (Gate 1A A1.2).

**Fields (concept contract).**

| Field | Required | Rationale |
|-------|----------|-----------|
| resource_id | REQUIRED | Stable canonical identity, Kernel-assigned, immutable for the lifetime of the resource. NOT a path, NOT a provider_object_id. Survives rename/move (principle 5). |
| root_id | REQUIRED | The ResourceRoot partition this resource belongs to. Immutable (Section 3: disjoint partitions). |
| introduced_at_generation | REQUIRED | The generation at which this resource first entered canonical state. |
| last_confirmed_generation | REQUIRED | The generation at which this resource was last confirmed present by an accepted snapshot. |
| status | REQUIRED | PRESENT or MISSING. MISSING != DELETED (principle 3). DELETED is a separate explicit transition (Worker C). |
| current_attributes | REQUIRED | Current canonical attributes (name, size, mtime, hash, etc.) as last reconciled. |
| identity_evidence | REQUIRED | The IdentityEvidence supporting this resource's identity claim (see 1.6). |
| canonical_path | OPTIONAL | The canonical path within the root, if the Kernel maintains one. Derived, NOT identity. |

**Identity continuity.** resource_id is stable across path changes. When a
rename/move is recognized (Rule R5/R6), the SAME resource_id persists; only
canonical_path and current_attributes change. This is the direct fix for the
D02-observed failure where rename = delete + add (Q12).

**Evidence.** Gate 1A A1.1 (Canonical Inventory) and A1.2 (Identity Continuity).
D02 Q12: AList/OpenList rename is delete+add (`internal/op/fs.go:364` AList
Move/Rename have no identity-preserving update; OpenList `op/fs.go:420,450` hook
is still name-diff delete+add), losing object identity. CanonicalResource with a
stable resource_id is the Kernel-owned correction.

---

### 1.5 Generation — ACCEPTED_SEMANTIC

**Definition.** A Generation is a consistency point in the Canonical Inventory
history of a single ResourceRoot. Generations are monotonic and per-root. Each
accepted reconcile that mutates canonical state produces a new generation.
Consumers read at a generation for consistency (Gate 1A A1.3).

**Fields (concept contract).**

| Field | Required | Rationale |
|-------|----------|-----------|
| root_id | REQUIRED | The root this generation belongs to. |
| generation_number | REQUIRED | Monotonic integer within the root. NOT global across roots. |
| produced_by | REQUIRED | The snapshot_id and reconcile that produced this generation. |
| produced_at | REQUIRED | Timestamp. |
| summary | REQUIRED | Reconcile result summary: added, marked_missing, unchanged, rejected counts (Gate 1A A4). |

**Monotonicity.** generation_number strictly increases per root. There is no
global generation across roots; roots reconcile independently. This follows from
disjoint partitions (Section 3).

**Non-overridable constraint.** Generation is a Kernel concept, NOT a Collector
cursor and NOT a provider native delta cursor (principles 8, 10). A Collector's
generation_hint (1.2) is a HINT, not the canonical generation.

**Evidence.** Gate 1A A1.3 (Root / Generation Semantics — ACCEPTED_BOUNDARY).

---

### 1.6 IdentityEvidence — ACCEPTED_SEMANTIC

**Definition.** IdentityEvidence is the structured set of signals that support a
canonical identity claim. It is attached to each CanonicalResource and evaluated
by the identity matching rules (Section 2). Evidence is multi-sourced and
versioned: each accepted snapshot may strengthen or revise evidence.

**Fields (concept contract).**

| Field | Required | Rationale |
|-------|----------|-----------|
| provider_object_id | OPTIONAL | Strongest signal when present and stable. OPTIONAL (principle 6). |
| provider_object_id_scope | OPTIONAL | Namespace for the above. Required iff present. |
| content_hash | OPTIONAL | Strong content signal. OPTIONAL (principle 7). |
| hash_algorithm | OPTIONAL | Required iff content_hash present. |
| observed_path | OPTIONAL | Last observed path within root. Weak signal (principle 5). |
| observed_parent_ref | OPTIONAL | Collector-local parent. Weak, Collector-local. |
| size | OPTIONAL | Weak-to-medium signal. |
| mtime | OPTIONAL | Weak-to-medium signal. |
| is_dir | REQUIRED | Directory vs file; affects move matching (Rule R6). |
| evidence_sources | REQUIRED | List of (snapshot_id, collector_ref, observed_at) that contributed this evidence. For audit and conflict detection. |

**Evidence strength model.** Used by the matching rules to decide MATCHED vs
UNRESOLVED vs CONFLICT. Strength is a function of which signals match and how
many candidates match.

| Strength | Condition | Typical result |
|----------|-----------|----------------|
| STRONG | provider_object_id present and matches a single candidate | MATCHED (R1) |
| STRONG | content_hash present and matches a single candidate | MATCHED (R3) |
| MEDIUM | path + size + mtime match a single candidate, no hash/id | MATCHED (R2 fallback) with collision caveat (R7) |
| WEAK | name + size match, no mtime/hash/id | UNRESOLVED unless single unambiguous candidate (R7) |
| AMBIGUOUS | multiple candidates match at any level | CONFLICT (R7, R8) |

**Why evidence is versioned.** A provider_object_id may appear in snapshot N and
disappear in snapshot N+1 (scenario 3). The evidence record retains the history
so the Kernel can mark missing rather than destroy identity (principle 3).

**Evidence.** Gate 1A A1.2 (Identity Continuity). D02 Q7/Q8: the absence of
provider object ID, mtime, and ETag in `SearchNode`
(`internal/model/search.go:23-28`) is precisely why AList/OpenList cannot
maintain identity. IdentityEvidence makes these signals explicit and optional,
so the Kernel can reason about identity with whatever evidence exists.

---

## 2. Stable Identity v1

### 2.1 Identity Result States — ACCEPTED_SEMANTIC

The identity matcher returns exactly one of these results for each SnapshotEntry
against the current Canonical Inventory of the entry's root. UNRESOLVED and
CONFLICT are FIRST-CLASS results, NOT errors. They are valid outcomes that
Safe Reconcile (Worker C) must handle without data loss.

| State | Meaning | Reconcile implication (outline; state machine is Worker C) |
|-------|---------|-------------------------------------------------------------|
| MATCHED | Identity confirmed. The entry corresponds to exactly one existing CanonicalResource. | Update current_attributes and last_confirmed_generation of the matched resource. |
| NEW_RESOURCE | No existing CanonicalResource matches. | Create a new CanonicalResource with a Kernel-assigned resource_id. |
| UNRESOLVED | Ambiguous: evidence is insufficient to determine MATCHED vs NEW_RESOURCE, but no contradiction exists. | Do NOT force-match (R11). Hold the entry; do not mutate canonical state destructively. Worker C defines the hold/staging behavior. |
| CONFLICT | Contradictory evidence: multiple candidates match, or an identity claim contradicts existing canonical state. | Do NOT pick a winner automatically. Record the conflict for resolution (Gate 1A B3; policy deferred). Worker C defines conflict staging. |

**Non-overridable.** UNRESOLVED and CONFLICT MUST be producible by the rules
below. The matcher MUST NOT collapse them into MATCHED or NEW_RESOURCE to
increase match rate (DO NOT: do not force-match to increase match rate).

---

### 2.2 Matching Rules

Rules are evaluated in precedence order. The first rule that fires (returns a
definitive result) wins. R0 is a gate applied before all others. Each rule is
tagged and cites evidence.

#### R0 — Root scope boundary (scenario 11) — ACCEPTED_SEMANTIC

**Rule.** Identity matching is performed ONLY within the same root_id. An
observed entry in root A is NEVER matched against a CanonicalResource in root B.
Cross-root entries are always NEW_RESOURCE (in their own root) or rejected if the
entry's root_id is unknown.

**Evidence.** Principle 5 (path != stable identity, extended: identity does not
cross scope boundaries). Gate 1A A1.3 (Root / Generation Semantics). D02 Q11:
mount paths are unique and `Parent + Name` identifies within one virtual
filesystem (`internal/op/fs.go:149`), confirming scope boundaries are real.

**Scenario covered.** 11 (Root scope boundaries).

---

#### R1 — provider_object_id present and stable (scenario 1) — ACCEPTED_SEMANTIC

**Precondition.** The entry has provider_object_id and provider_object_id_scope.
R0 has passed (same root).

**Rule.** Look up CanonicalResources in the same root with matching
(provider_object_id, provider_object_id_scope).

- Exactly one match -> MATCHED (that resource).
- Zero matches -> proceed to R2 (do not short-circuit to NEW_RESOURCE; a
  content-hash match may recognize a rename even with a new provider id).
- Multiple matches -> CONFLICT (duplicate provider_object_id in canonical state
  is itself a conflict to resolve).

**Evidence.** Gate 1A A1.2 (Identity Continuity). D02 Q7: provider object ID is
the strongest stable identity signal, and AList/OpenList's failure to store it
(`internal/model/search.go:23-28` has no ID field) is the root cause of their
identity loss. When present, it is authoritative.

**Scenario covered.** 1 (provider_object_id present and stable -> MATCHED).

---

#### R2 — provider_object_id absent, fallback matching (scenario 2) — CANDIDATE

**Precondition.** The entry has NO provider_object_id (or R1 found zero matches
and the entry has a provider_object_id not yet seen). R0 has passed.

**Rule.** Evaluate fallback signals in order of strength. This is a CANDIDATE
rule: the exact signal combination and thresholds are v1 candidates subject to
review, but the structure is fixed.

1. If content_hash present -> R3 (content identity).
2. Else -> path + size + mtime heuristic (R4).
3. Else -> name + size heuristic (R7, conservative).

**Why CANDIDATE not ACCEPTED.** The principle that fallback is needed is
ACCEPTED (principle 6: provider capability optional; principle 7: hash optional).
The specific combination and thresholds are CANDIDATE pending Gate 1B review and
Worker B completeness interaction.

**Evidence.** Principle 6 (provider capability optional). D02 Q7/Q8: real
systems operate without provider_object_id, so fallback MUST exist.

**Scenario covered.** 2 (provider_object_id absent -> fallback matching).

---

#### R3 — content_hash present and matches (scenarios 5, 6) — ACCEPTED_SEMANTIC

**Precondition.** The entry has content_hash and hash_algorithm. R0 has passed.
R1 did not fire (no provider_object_id match).

**Rule.** Look up CanonicalResources in the same root with matching
(content_hash, hash_algorithm).

- Exactly one match -> MATCHED. This RECOGNIZES a rename or move: the resource
  is the same canonical resource at a new path. canonical_path and
  current_attributes update; resource_id is unchanged.
- Zero matches -> proceed to R4 (path heuristic; the entry may be a genuinely
  new resource, or a moved resource whose hash we have not seen).
- Multiple matches -> CONFLICT (hash collision or duplicate content; cannot
  auto-resolve).

**Evidence.** Principle 7 (hash optional, but when present it is a strong
content signal). Principle 5 (path != identity: a hash match at a different path
is the same resource). D02 Q12: AList/OpenList rename is delete+add because
there is no content-identity signal (`internal/op/fs.go:364,420`); R3 is the
Kernel-owned fix.

**Scenarios covered.** 5 (path rename, same content, different path) and 6
(path move, same content, different parent).

---

#### R4 — hash absent, path + size + mtime heuristic (scenario 8) — ACCEPTED_SEMANTIC

**Precondition.** The entry has NO content_hash. R0 has passed. R1 did not fire.

**Rule.** This is the degraded path when hash is absent (scenario 8: cannot use
hash for matching).

1. Look up CanonicalResources in the same root at the same observed_path.
2. If exactly one candidate and size matches and mtime matches (or both absent
   consistently) -> MATCHED (MEDIUM strength).
3. If exactly one candidate and size matches but mtime differs -> the content
   may have changed; MATCHED the identity (same path, same size) but flag
   current_attributes for update. (Conservative: same path + same size is
   reasonable identity continuity; mtime change alone does not break identity.)
4. If no candidate at this path -> look for a moved candidate: a resource
   recently marked MISSING in this root whose size and (if available) mtime
   match. If exactly one -> MATCHED (recognized move without hash). If multiple
   -> CONFLICT (R7).
5. If none of the above -> NEW_RESOURCE.

**Evidence.** Principle 7 (hash optional). Principle 5 (path is a hint).
D02 Q5: AList/OpenList name-only diff (`internal/search/build.go:224-234`) is
the degenerate case of this heuristic; R4 is richer (path + size + mtime +
missing-move lookup) but still conservative.

**Scenario covered.** 8 (hash absent).

---

#### R5 — path rename / move with same content (scenarios 5, 6) — ACCEPTED_SEMANTIC

**Precondition.** A resource at old_path was marked MISSING in a recent
generation (within a configurable recency window, v1 candidate). A new entry
appears at new_path with matching content_hash (R3) OR matching (size, mtime)
when hash absent (R4 step 4).

**Rule.** Recognize the move: MATCHED to the missing resource. Update
canonical_path to new_path. This is identity-preserving: resource_id unchanged.

**Note.** R5 is the move-recognition overlay on top of R3/R4. It is listed
separately because the task explicitly requires handling scenarios 5 and 6, and
the recognition depends on the MISSING state (principle 3: missing != deleted),
which is a Kernel canonical-state concept.

**Evidence.** Principle 3 (missing != deleted: a missing resource can be
re-recognized). Principle 5 (path != identity). D02 Q12: rename = delete+add
loses identity; R5 preserves it.

**Scenarios covered.** 5 (path rename), 6 (path move).

---

#### R6 — directory rename / move (scenario 7) — CANDIDATE

**Precondition.** A directory CanonicalResource is recognized as moved by R5
(its identity matched at a new path). The directory has descendant resources.

**Rule.** Propagate the move to descendants: for each descendant at
old_path/child_rel, look for a new entry at new_path/child_rel. If found and
identity-consistent (size matches; hash matches if available) -> MATCHED (the
descendant moved with the directory). If a descendant is not found at the
expected new relative path -> leave it MISSING (do not force-match).

**Why CANDIDATE.** Batch move recognition requires coordination with Safe
Reconcile staging (Worker C) and completeness assessment (Worker B): a partial
snapshot may show the directory moved but not all descendants yet. The
propagation policy is CANDIDATE pending Worker C reconcile design.

**Evidence.** Principle 3 (missing != deleted). D02 Q12: OpenList directory
moves trigger recursive hook (`internal/op/fs.go:836-856`) but still delete+add
by name; R6 is the identity-preserving version.

**Scenario covered.** 7 (directory rename / move).

---

#### R7 — same name + same size + same mtime collision risk (scenario 9) — ACCEPTED_SEMANTIC

**Precondition.** Fallback matching (R2/R4) reaches the name + size (+ mtime)
level with no stronger signal.

**Rule.**

- If multiple candidates match -> CONFLICT (collision: cannot determine which
  canonical resource this entry is).
- If exactly one candidate matches AND the match is at MEDIUM strength (path +
  size + mtime all match) -> MATCHED.
- If exactly one candidate matches but at WEAK strength (name + size only, no
  mtime, no path anchor) -> UNRESOLVED. Do NOT force-match (R11). Same name +
  same size without mtime or path anchor is a collision risk, not an identity
  claim.

**Evidence.** Principle 4 (incomplete input must not authorize destructive
reconcile: a weak match is incomplete evidence). DO NOT constraint (do not
force-match to increase match rate). D02 Q9: name-only set difference
(`internal/search/build.go:232-244`) is exactly the collision-prone heuristic
the Kernel must improve upon.

**Scenario covered.** 9 (same name + same size + same mtime collision risk).

---

#### R8 — old path reused by a different object, imposter (scenario 10) — ACCEPTED_SEMANTIC

**Precondition.** An entry appears at observed_path where a CanonicalResource
already exists, but the evidence does not support identity continuity.

**Rule.**

- If both the entry and the existing resource have provider_object_id and they
  DIFFER -> CONFLICT (two different provider objects claim the same path; needs
  resolution).
- If content_hash present on both and they DIFFER -> the entry is a NEW_RESOURCE
  (imposter) at this path; the existing resource is marked MISSING (it may have
  moved elsewhere; R5 may re-recognize it). The path is reassigned to the
  imposter as a new canonical resource.
- If no strong signal (no hash, no provider_object_id) and size or mtime differ
  -> UNRESOLVED. Cannot determine whether the existing resource changed in place
  or was replaced by an imposter. Do NOT force-match.

**Evidence.** Principle 5 (path != identity: a path can be reused). D02 Q12:
rename = delete+add means AList/OpenList cannot distinguish imposter from
in-place change; R8 makes the distinction explicit and conservative.

**Scenario covered.** 10 (old path reused by a different object, imposter).

---

#### R9 — provider_object_id disappears between snapshots (scenario 3) — ACCEPTED_SEMANTIC

**Precondition.** A CanonicalResource has a provider_object_id. A new accepted
snapshot for the root does NOT contain any entry with that provider_object_id.

**Rule.** Mark the CanonicalResource MISSING. Do NOT delete it (principle 3).
Do NOT immediately reassign its identity. The resource may reappear in a later
snapshot (transient provider issue, pagination, rate limit). R5 may re-recognize
it if it reappears at a new path with matching content.

**Evidence.** Principle 3 (missing != deleted). D02 Q10: AList/OpenList has no
partial-list protection (`internal/search/build.go:232-244`); a partial list
deletes "missing" entries. R9 is the Kernel-owned fix: disappearance -> MISSING,
not deletion.

**Scenario covered.** 3 (provider_object_id disappears between snapshots).

---

#### R10 — provider_object_id changes unexpectedly (scenario 4) — ACCEPTED_SEMANTIC

**Precondition.** A CanonicalResource has identity continuity at a path (R4 has
been matching it across generations). A new snapshot entry at the same path has
a DIFFERENT provider_object_id than the canonical resource's recorded one.

**Rule.**

- If the new provider_object_id matches a DIFFERENT existing CanonicalResource
  -> CONFLICT (two canonical resources claim the same path; needs resolution).
- Else -> UNRESOLVED. Cannot determine whether (a) the same object's
  provider_object_id changed (e.g., provider reissued ids), or (b) a new object
  replaced the old one at this path. Do NOT force-match either way. Worker C
  defines the staging/hold behavior for UNRESOLVED.

**Evidence.** Principle 5 (path != identity: a path's occupant can change).
Principle 6 (provider capability optional: provider ids are not guaranteed
stable). Gate 1A A1.2 (Identity Continuity must handle ambiguous evidence).

**Scenario covered.** 4 (provider_object_id changes unexpectedly).

---

#### R11 — no force-match (DO NOT compliance) — ACCEPTED_SEMANTIC

**Rule.** When the evaluated evidence is insufficient to return MATCHED with at
least MEDIUM strength, and no CONFLICT condition is met, the matcher MUST return
UNRESOLVED. The matcher MUST NOT guess MATCHED or NEW_RESOURCE to increase the
match rate. UNRESOLVED is a valid, first-class result.

**Evidence.** DO NOT constraint (do not force-match to increase match rate).
Principle 4 (incomplete input must not authorize destructive reconcile:
force-matching on weak evidence is a form of destructive authorization).

**Scenario covered.** Underpins all scenarios where evidence is weak (2, 8, 9,
10). This is the rule that makes UNRESOLVED first-class.

---

### 2.3 Rule Precedence — ACCEPTED_SEMANTIC

Rules are evaluated in this order for each SnapshotEntry. The first definitive
result wins; "proceed" means continue to the next rule.

```
R0 (root scope gate)
  -> R1 (provider_object_id strong match)
     -> R2 (fallback dispatcher; provider_object_id absent)
        -> R3 (content_hash match; recognizes rename/move)
           -> R4 (path + size + mtime heuristic; hash absent)
              -> R5 (move recognition overlay on MISSING resources)
                 -> R6 (directory move propagation)
                    -> R7 (collision/weak-signal resolution)
                       -> R8 (imposter at existing path)
                          -> R9 (provider_object_id disappeared -> MISSING)
                          -> R10 (provider_object_id changed -> UNRESOLVED/CONFLICT)
                          -> R11 (no force-match -> UNRESOLVED)
```

R9 and R10 are evaluated against canonical state (resources not in the snapshot),
not against the entry, so they run as part of reconcile, not per-entry. R11 is
the final fallback for any entry that reaches it without a definitive result.

---

## 3. Root Ownership Resolution — ACCEPTED_SEMANTIC (Option A)

**Decision: A. Root partitions are disjoint; a resource belongs to exactly one
Root.**

### Justification

1. **Gate 1A A1.3** defines a Root as the scope boundary of a Canonical Inventory
   partition. A partition that overlaps another is not a clean boundary.

2. **Scenario 11** requires that identity does not cross roots. Disjoint
   partitions make this trivially true and enforceable. Overlapping partitions
   (Option B) would require defining canonical ownership and merge rules for
   every resource in the overlap, which is a significant addition to the Kernel
   and violates the minimal-Kernel principle (Gate 1A A5).

3. **D02 Q11 evidence.** AList/OpenList storage mount paths are unique by design
   (enforced at storage creation time); two storages cannot mount at the same
   virtual path, and `Parent + Name` uniquely identifies an index entry within
   the virtual filesystem (`internal/op/fs.go:149`, `utils.GetFullPath`). This
   is a real-world confirmation that disjoint scope boundaries are practical and
   already the de facto model.

4. **Principle 9.** The Kernel Domain does not bind schema/ORM. Overlapping
   partitions with merge rules would push domain-specific merge semantics into
   the Kernel, increasing coupling. Disjoint partitions keep the Kernel smaller.

5. **What about cross-root references?** If a Consumer needs a unified view
   across roots, that is a Consumer/external projection concern (Gate 1A A2:
   query projection / read-model maintenance is REJECTED from Kernel). The
   Kernel provides per-root canonical state; cross-root views are built on top.

### Rejected alternative

**Option B (overlapping partitions with canonical ownership / merge rules) —
REJECTED.** It expands the Kernel with merge semantics, conflicts with the
minimal-Kernel principle, and has no supporting evidence in D02. If a future use
case requires overlap, it must pass a fresh four-way proof of non-delegability
(Gate 1A A5) and be re-evaluated in a later gate.

### Consequence for the model

- CanonicalResource.root_id is immutable (1.4). A resource never moves between
  roots.
- Generation is per-root (1.5). There is no global generation.
- Identity matching is scoped to one root (R0).
- If a Collector observes a resource that legitimately spans two scopes, the
  deployment must model that as two roots with a Consumer-side projection, OR
  redefine the root boundaries. This is a deployment/scoping decision, not a
  Kernel domain rule.

---

## 4. Scenario Coverage Table

All 11 required scenarios mapped to rules and results.

| # | Scenario | Primary rule(s) | Result | Marker |
|---|----------|-----------------|--------|--------|
| 1 | provider_object_id present and stable | R1 | MATCHED | ACCEPTED_SEMANTIC |
| 2 | provider_object_id absent, fallback | R2 -> R3/R4/R7 | MATCHED / UNRESOLVED per strength | CANDIDATE (structure) / ACCEPTED (principle) |
| 3 | provider_object_id disappears between snapshots | R9 | MISSING (not deleted) | ACCEPTED_SEMANTIC |
| 4 | provider_object_id changes unexpectedly | R10 | UNRESOLVED or CONFLICT | ACCEPTED_SEMANTIC |
| 5 | path rename (same content, different path) | R3, R5 | MATCHED (identity preserved) | ACCEPTED_SEMANTIC |
| 6 | path move (same content, different parent) | R3, R5 | MATCHED (identity preserved) | ACCEPTED_SEMANTIC |
| 7 | directory rename / move | R6 | MATCHED for moved descendants; MISSING for absent | CANDIDATE |
| 8 | hash absent (cannot use hash) | R4 | MATCHED / UNRESOLVED per path+size+mtime | ACCEPTED_SEMANTIC |
| 9 | same name + same size + same mtime (collision risk) | R7 | CONFLICT (multi) or UNRESOLVED (weak) | ACCEPTED_SEMANTIC |
| 10 | old path reused by a different object (imposter) | R8 | CONFLICT / NEW_RESOURCE / UNRESOLVED per evidence | ACCEPTED_SEMANTIC |
| 11 | Root scope boundaries (identity does not cross roots) | R0 | NEW_RESOURCE in own root; never cross-root MATCHED | ACCEPTED_SEMANTIC |

All 11 scenarios are covered. No scenario is force-matched; UNRESOLVED and
CONFLICT are reachable for scenarios 2, 4, 7, 9, 10.

---

## 5. Subagent Ledger

| Subagent | Narrow question | Key result | Worker verification | Decision |
|----------|-----------------|------------|---------------------|----------|
| counterexample-hunter (self-run by Worker A) | Do the identity rules R0-R11 produce a wrong result for any of the 11 scenarios, or allow force-matching? | No scenario is force-matched. UNRESOLVED reachable in 2,4,7,9,10. CONFLICT reachable in 1(multi-id),4,7,9,10. R0 prevents cross-root identity leak. R9 never deletes. R11 enforces conservative fallback. One gap found: R6 directory move propagation depends on Worker C staging; flagged CANDIDATE, not a defect. | VERIFIED (Worker re-checked each rule against its scenario and the evidence citations; R6 dependency on Worker C is explicitly CANDIDATE, not hidden) | ADOPT (rules R0-R5, R7-R11); HOLD (R6 pending Worker C) |
| contract-consistency-checker (self-run by Worker A) | Are the Domain Model (Section 1) and Identity Rules (Section 2) mutually consistent, and consistent with Gate 1A frozen boundaries? | Consistent. resource_id != path != provider_object_id (1.4 vs 1.3). parent_ref remains Collector-local (1.3). hash and provider_object_id remain OPTIONAL (1.3, 1.6, R1-R4). Generation is per-root (1.5) matching disjoint partitions (Section 3). Snapshot is immutable input (1.2) matching Gate 1A A3. CanonicalResource.status MISSING != DELETED (1.4) matching principle 3. No Safe Reconcile state machine designed (deferred to Worker C). No completeness acceptance designed (deferred to Worker B). No Change Journal format designed (deferred to Worker C). | VERIFIED (Worker cross-checked each Section 1 field against Gate 1A A1-A5 and the DO NOT list) | ADOPT |
| evidence-reader (self-run by Worker A) | Do all ACCEPTED_SEMANTIC rules cite a principle, D02 finding, or Gate 1A boundary? | Yes. R0: principle 5 + A1.3 + D02 Q11. R1: A1.2 + D02 Q7. R3: principle 7 + principle 5 + D02 Q12. R4: principle 7 + D02 Q5. R5: principle 3 + D02 Q12. R7: principle 4 + D02 Q9. R8: principle 5 + D02 Q12. R9: principle 3 + D02 Q10. R10: principle 5 + principle 6 + A1.2. R11: DO NOT + principle 4. Section 3: A1.3 + D02 Q11 + principle 9. | VERIFIED (every ACCEPTED_SEMANTIC tag has at least one citation) | ADOPT |

**Note on subagent availability.** The recommended subagents
(evidence-reader, counterexample-hunter, contract-consistency-checker) are
listed in the task as residing in `.codeartsdoer/agents/`. That directory does
not exist in this project. Worker A performed all three roles via self-verification
(recorded above) rather than skipping them. Each self-run is tagged VERIFIED
because the Worker re-checked the specific narrow question against the source
evidence, not by assertion alone.

---

## 6. Quality Markers Summary

| Marker | Count | Items |
|--------|-------|-------|
| ACCEPTED_SEMANTIC | Domain: 6 concepts (1.1-1.6) + Root ownership (Section 3) + Identity Result States (2.1) + Rules R0, R1, R3, R4, R5, R7, R8, R9, R10, R11 + Rule Precedence (2.3) | Core model and conservative identity rules |
| CANDIDATE | R2 (fallback signal combination), R6 (directory move propagation) | Subject to Gate 1B review / Worker C reconcile design |
| DEFERRED | Safe Reconcile state machine (Worker C); completeness acceptance (Worker B); Change Journal format (Worker C); conflict resolution policy (Gate 1A B3, deferred); Collector selection (Gate 1C); Store adapter (Gate 1C) | Out of scope per DO NOT |
| REJECTED | Option B (overlapping root partitions) | Section 3 |

Every identity rule is tagged. Every rule cites evidence (principle, D02
finding, or Gate 1A boundary). UNRESOLVED and CONFLICT are first-class results
(2.1), reachable by R7, R8, R10, and R11, and are NOT errors.

---

## 7. DO NOT Compliance

| Constraint | Status |
|------------|--------|
| Did not require hash or provider_object_id as mandatory | Honored — 1.3 and 1.6 mark both OPTIONAL; R1/R3 are conditional on presence; R4/R7 handle absence |
| Did not design PostgreSQL schema/SQL/migration | Honored — all fields are concept contracts; no table/column/type specified (principle 9) |
| Did not design Safe Reconcile state machine | Honored — 2.1 gives reconcile IMPLICATIONS only as outline; state machine deferred to Worker C |
| Did not design completeness acceptance | Honored — completeness_flag is referenced as Gate 1A A3/A1.4/B2; acceptance criteria deferred to Worker B |
| Did not design Change Journal | Honored — referenced as Gate 1A A1.6; format deferred to Worker C |
| Did not write product code | Honored — design document only |
| Did not select final Collector | Honored — owning_collector_ref is OPTIONAL; selection deferred to Gate 1C |
| Did not design Scanner checkpoint/resume | Honored — not in model; Gate 1A REJECTED it from Kernel (principle 10) |
| Did not design native delta / incremental | Honored — Snapshot is the input unit (principle 8); native delta is NOT Kernel input (Gate 1A A3) |
| Did not force-match to increase match rate | Honored — R11 explicitly forbids it; UNRESOLVED is the required result for weak evidence |

---

## 8. DEFERRED Items (explicit handoff)

| Item | Owner | Reason |
|------|-------|--------|
| Safe Reconcile state machine (staging, atomicity, UNRESOLVED/CONFLICT hold behavior) | Worker C (Gate 1B) | DO NOT constraint; R6/R9/R10 outcomes need reconcile staging |
| Completeness acceptance criteria (beyond the flag) | Worker B (Gate 1B) | DO NOT constraint; interacts with R4/R5 move recognition |
| Canonical Change Journal format | Worker C (Gate 1B) | DO NOT constraint; records the identity-preserving transitions R3/R5/R6 produce |
| Conflict resolution policy (for CONFLICT results) | Gate 1B (Gate 1A B3 deferred) | CONFLICT is produced by R7/R8/R10; resolution policy is a separate design |
| R2 fallback signal combination thresholds | Gate 1B review | CANDIDATE; needs Worker B completeness interaction |
| R6 directory move propagation policy | Worker C (Gate 1B) | CANDIDATE; depends on reconcile staging and partial-snapshot handling |
| Collector selection per root | Gate 1C | owning_collector_ref OPTIONAL until then |
| Store adapter mapping (canonical state to schema) | Gate 1C | principle 9; Kernel Domain must not bind schema/ORM |

---

## 9. DONE WHEN Verification

| Criterion | Met | Where |
|------------|-----|------|
| Domain Model defined with all 6 concepts | Yes | Section 1 (ResourceRoot, Snapshot, SnapshotEntry, CanonicalResource, Generation, IdentityEvidence) |
| Identity v1 rules cover all 11 scenarios | Yes | Section 4 coverage table; rules R0-R11 |
| Identity Result states defined | Yes | Section 2.1 (MATCHED, NEW_RESOURCE, UNRESOLVED, CONFLICT) |
| Root ownership resolved (A or B with justification) | Yes | Section 3 (Option A, disjoint, with 5-point justification) |
| Subagent Ledger present | Yes | Section 5 |
| No DO NOT violations | Yes | Section 7 |

---

## Evidence Index

Citations used in this document, with source locations.

| Citation | Source |
|----------|--------|
| Principle 3 (missing != deleted) | Accepted principles, Gate 1A |
| Principle 4 (incomplete input must not authorize destructive reconcile) | Accepted principles, Gate 1A |
| Principle 5 (path != stable identity) | Accepted principles, Gate 1A |
| Principle 6 (provider capability optional) | Accepted principles, Gate 1A |
| Principle 7 (hash optional) | Accepted principles, Gate 1A |
| Principle 8 (native delta != Change Journal) | Accepted principles, Gate 1A |
| Principle 9 (Kernel Domain does not bind schema/ORM) | Accepted principles, Gate 1A |
| Principle 10 (Scanner checkpoint not Kernel) | Accepted principles, Gate 1A |
| Gate 1A A1.1-A1.6, A3, A5, B2, B3 | .work/gate1a/W-A-KERNEL-BOUNDARY.md |
| D02 Q5 (name-only diff) | docs/research/d02/W-A-ALIST-OPENLIST-INDEXING.md; `internal/search/build.go:224-234` |
| D02 Q7 (no provider object ID) | docs/research/d02/...; `internal/model/search.go:23-28` |
| D02 Q8 (search index != canonical inventory) | docs/research/d02/... |
| D02 Q9 (missing treated as deleted) | docs/research/d02/...; `internal/search/build.go:232-244` |
| D02 Q10 (no partial-list protection) | docs/research/d02/...; `internal/search/build.go:232-244` |
| D02 Q11 (mount paths unique, path identity) | docs/research/d02/...; `internal/op/fs.go:149`, `utils.GetFullPath` |
| D02 Q12 (rename = delete + add) | docs/research/d02/...; `internal/op/fs.go:364,420,450,836-856` |

No discussion-stage idea is written as accepted architecture. All
ACCEPTED_SEMANTIC conclusions are derived from the ten accepted principles and
validated by Gate 1A boundaries and D02 source-code evidence.
---

## 10. Root Lifecycle (Foreman consolidation — resolves VIOLATION scenario 20)

> This section is added by the Foreman to resolve the VIOLATION identified by
> Worker D scenario 20 (root delete/recreate: root lifecycle undefined).
> It is a minimal lifecycle definition consistent with Worker A's ResourceRoot
> (Sec 1.1) and Root Ownership Option A (disjoint partitions, Sec 3).

### 10.1 Root lifecycle states — ACCEPTED_SEMANTIC

| State | Meaning | Reconcile behavior |
|-------|---------|-------------------|
| NEW | Root created, no snapshot received yet | No canonical resources exist for this root |
| ACTIVE | Root is being reconciled normally | Snapshots accepted; reconcile proceeds per Worker C |
| DEPRECATED | Root is being retired; no new snapshots expected | Existing canonical resources preserved; snapshots still accepted if submitted (defensive) |
| DELETED | Root is permanently removed | No new snapshots accepted; canonical resources for this root are not reconciled |

### 10.2 Root lifecycle transitions — ACCEPTED_SEMANTIC

```
NEW -> ACTIVE (first snapshot accepted)
ACTIVE -> DEPRECATED (operator marks root deprecated)
ACTIVE -> DELETED (operator deletes root)
DEPRECATED -> DELETED (operator deletes deprecated root)
```

All other transitions are REJECTED. In particular:
- DELETED -> ACTIVE is REJECTED: a deleted root_id cannot be reused.
- DELETED -> NEW is REJECTED: root_id is immutable and unique forever.

### 10.3 Root deletion and canonical resources — ACCEPTED_SEMANTIC

When a root transitions to DELETED:
1. Its canonical resources remain in the Canonical Inventory at their last
   committed generation (they are NOT automatically removed).
2. No further reconcile is performed for this root.
3. The resources are effectively frozen at their last known state.
4. A Journal event `root-deleted` is emitted (canonical transition).
5. If the same physical source is re-added, it MUST receive a new root_id
   (NEW state). The old root's resources are NOT merged with the new root's
   resources (disjoint partitions, Sec 3 Option A).

### 10.4 Root recreation — ACCEPTED_SEMANTIC

Root recreation is modeled as creating a NEW root with a new root_id. It is
NOT a lifecycle transition of the old root. The old root remains DELETED
forever. The new root starts fresh with no canonical resources.

This resolves Worker D scenario 20: root delete/recreate has defined behavior
(old root frozen + new root created), no orphaned resources (old root's
resources are frozen, not leaked), and no root_id reuse (immutable).

### 10.5 Journal events for root lifecycle — ACCEPTED_SEMANTIC

| Transition | Journal event |
|------------|---------------|
| NEW -> ACTIVE | (none — first reconcile generates resource events) |
| ACTIVE -> DEPRECATED | `root-deprecated` |
| * -> DELETED | `root-deleted` |

These are canonical transition events recorded in the Change Journal, not
provider-reported events (principle 8).
