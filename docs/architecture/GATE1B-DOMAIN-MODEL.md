# Gate 1B — Domain Model + Stable Identity v1

> Normative Gate 1B architecture contract.
> Finalized by ChatGPT Architect from the Gate 1B design/rework evidence.
> Historical Worker/Architect execution roles are not part of the active project governance.

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
(Safe Reconcile contract).

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
| resource_presence | REQUIRED | `PRESENT` or `REMOVED`. `REMOVED` is a logical canonical tombstone, not physical erasure. Active queries normally exclude REMOVED; exact storage/query mechanics are Gate 1C. |
| removal_evidence_state | REQUIRED | `NONE`, `MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT`, or `REMOVAL_CANDIDATE`. Kernel safety metadata only; it is not provider state and is not equivalent to consumer-visible absence. |
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
| provider_object_id | OPTIONAL | Provider-side object id if the driver exposes one. OPTIONAL (principle 6). NOT identity by itself; only authoritative when provider_identity_assurance = STABLE_WITHIN_SCOPE (A3 rework). |
| provider_object_id_scope | OPTIONAL | Namespace for the above. Required iff present. |
| provider_identity_assurance | REQUIRED | Capability/assurance qualifier for provider_object_id (A3 rework). One of STABLE_WITHIN_SCOPE, UNVERIFIED, UNSTABLE, UNAVAILABLE. Only STABLE_WITHIN_SCOPE qualifies provider_object_id as STRONG identity evidence. UNAVAILABLE when no provider_object_id. Gate 1C decides how AList/rclone/adapters map to these values; Gate 1B does not select Collectors. |
| content_hash | OPTIONAL | Strong content fingerprint/evidence. OPTIONAL (principle 7). NOT canonical identity (A1 rework): two distinct resources may share identical content. |
| hash_algorithm | OPTIONAL | Required iff content_hash present. |
| observed_path | OPTIONAL | Last observed path within root. Weak continuity hint (principle 5). |
| observed_parent_ref | OPTIONAL | Collector-local parent. Weak, Collector-local. |
| size | OPTIONAL | Weak evidence. |
| mtime | OPTIONAL | Weak evidence. Absence of mtime is NOT positive match evidence (A2 rework). |
| is_dir | REQUIRED | Directory vs file; affects move matching (Rule R6). |
| evidence_sources | REQUIRED | List of (snapshot_id, collector_ref, observed_at) that contributed this evidence. For audit and conflict detection. |

**provider_identity_assurance values (A3 rework — ACCEPTED_SEMANTIC).**

| Value | Meaning | Identity weight |
|-------|---------|-----------------|
| STABLE_WITHIN_SCOPE | Collector/Adapter declares that the backend/scope satisfies the stability contract for provider_object_id within this root. | provider_object_id is STRONG identity evidence. |
| UNVERIFIED | provider_object_id field has a value but no stability declaration. | NOT STRONG. Treated as weak evidence only. |
| UNSTABLE | Known unstable (e.g., driver reissues ids). | NOT STRONG. Weak evidence only. |
| UNAVAILABLE | No provider_object_id. | provider_object_id contributes nothing. |

Only STABLE_WITHIN_SCOPE upgrades provider_object_id to STRONG. This closes the
gap where the original text said "present and stable" but the implementation
semantics only checked field presence/match (D02 Q7/Q8: provider_object_id is
DRIVER_DEPENDENT). Gate 1C decides how specific Collectors/adapters map to these
assurances; Gate 1B defines the concept only.

**Evidence strength model.** Used by the matching rules to decide MATCHED vs
UNRESOLVED vs CONFLICT. Strength is a function of which signals match, how many
candidates match, AND whether continuity context exists (A1 rework: hash alone is
not identity).

| Strength | Condition | Typical result |
|----------|-----------|----------------|
| STRONG | provider_identity_assurance = STABLE_WITHIN_SCOPE and provider_object_id matches a single candidate | MATCHED (R1) |
| STRONG_FINGERPRINT | content_hash matches a single candidate AND continuity context exists (A1 rework) | MATCHED (R3) |
| STRONG_FINGERPRINT | content_hash matches a single candidate but NO continuity context (e.g., a copy at a new path with no MISSING candidate in the reconcile horizon) | NEW_RESOURCE or UNRESOLVED (R3) — hash is evidence, not identity |
| MEDIUM | path + size + mtime all present and match a single candidate, no STRONG signal | MATCHED (R2 fallback) with collision caveat (R7) |
| WEAK | name + size match, no mtime/hash/id, or mtime absent | UNRESOLVED unless single unambiguous candidate (R7). Absent mtime is NOT positive evidence (A2 rework). |
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
Safe Reconcile (Safe Reconcile contract) must handle without data loss.

| State | Meaning | Reconcile implication (outline; state machine is Safe Reconcile contract) |
|-------|---------|-------------------------------------------------------------|
| MATCHED | Identity confirmed. The entry corresponds to exactly one existing CanonicalResource. | Update current_attributes and last_confirmed_generation of the matched resource. |
| NEW_RESOURCE | No existing CanonicalResource matches. | Create a new CanonicalResource with a Kernel-assigned resource_id. |
| UNRESOLVED | Ambiguous: evidence is insufficient to determine MATCHED vs NEW_RESOURCE, but no contradiction exists. | Do NOT force-match (R11). Hold the entry; do not mutate canonical state destructively. Safe Reconcile contract defines the hold/staging behavior. |
| CONFLICT | Contradictory evidence: multiple candidates match, or an identity claim contradicts existing canonical state. | Do NOT pick a winner automatically. Record the conflict for resolution (Gate 1A B3; policy deferred). Safe Reconcile contract defines conflict staging. |

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

**Precondition.** The entry has provider_object_id and provider_object_id_scope,
AND provider_identity_assurance = STABLE_WITHIN_SCOPE (A3 rework: only a
qualified, assurance-declared stable id is STRONG identity evidence). R0 has
passed (same root).

If provider_object_id is present but assurance is UNVERIFIED, UNSTABLE, or
UNAVAILABLE, R1 does NOT fire on STRONG grounds; the id is treated as weak
evidence and the matcher proceeds to R2.

**Rule.** Look up CanonicalResources in the same root with matching
(provider_object_id, provider_object_id_scope).

- Exactly one match -> MATCHED (that resource).
- Zero matches -> proceed to R2 (do not short-circuit to NEW_RESOURCE; a
  content-hash match with continuity context may recognize a rename even with a
  new provider id).
- Multiple matches -> CONFLICT (duplicate provider_object_id in canonical state
  is itself a conflict to resolve).

**Evidence.** Gate 1A A1.2 (Identity Continuity). D02 Q7/Q8: provider object ID
is DRIVER_DEPENDENT, and AList/OpenList's failure to store it
(`internal/model/search.go:23-28` has no ID field) is the root cause of their
identity loss. A3 rework: "present and stable" required an explicit assurance
qualifier, not a field-presence check; provider_identity_assurance encodes that
contract. When STABLE_WITHIN_SCOPE, it is authoritative.

**Scenario covered.** 1 (provider_object_id present and stable -> MATCHED).

---

#### R2 — provider_object_id absent, fallback routing (scenario 2) — ACCEPTED_SEMANTIC

**Precondition.** The entry has NO provider_object_id (or R1 found zero matches
and the entry has a provider_object_id not yet seen). R0 has passed.

**Rule.** Route fallback matching by available evidence. R2 itself does not invent additional thresholds; it dispatches to the already frozen rules R3/R4/R7:\n\n1. If `content_hash` is present -> evaluate R3.\n2. Otherwise evaluate the same-path `path + size + mtime` rule R4.\n3. If R4 cannot establish MEDIUM continuity, evaluate R7 only as a collision/weak-evidence guard; WEAK evidence remains UNRESOLVED.\n\nNo fallback path may auto-MATCH using evidence weaker than the accepted R3/R4/R7 semantics.


**Evidence.** Principle 6 (provider capability optional). D02 Q7/Q8: real
systems operate without provider_object_id, so fallback MUST exist.

**Scenario covered.** 2 (provider_object_id absent -> fallback matching).

---

#### R3 — content_hash present and matches (scenarios 5, 6) — ACCEPTED_SEMANTIC

**Precondition.** The entry has content_hash and hash_algorithm. R0 has passed.
R1 did not fire (no STABLE_WITHIN_SCOPE provider_object_id match).

**Frozen principle (A1 rework).** content_hash is a strong fingerprint/evidence,
NOT canonical identity. Two distinct resources may have identical content (a
copy). A hash match alone MUST NOT be treated as MATCHED. A cross-path
rename/move auto-MATCH requires continuity context: the old canonical candidate
is MISSING within the same root's effective reconcile horizon AND no conflict
exists. A newly copied file with identical content at a new path, with no MISSING
candidate to continue, MUST be able to come out NEW_RESOURCE / UNRESOLVED, not
forced MATCHED. Hash is also not required of every Provider (principle 7).

**Rule.** Look up CanonicalResources in the same root with matching
(content_hash, hash_algorithm).

- Zero matches -> proceed to R4 (path heuristic; the entry may be a genuinely
  new resource, or a moved resource whose hash we have not seen).
- Multiple matches -> CONFLICT (hash collision or duplicate content; cannot
  auto-resolve).
- Exactly one match -> evaluate continuity context:
  - If the matched candidate is at the SAME observed_path (in-place re-observation
    of the same canonical resource) -> MATCHED (same path + same content is
    identity continuity of the resource already there).
  - If the matched candidate is at a DIFFERENT path AND that candidate is
    currently MISSING within the effective rename/move recognition horizon
    (Section 2.4, A4 rework) AND no conflict exists -> MATCHED. This RECOGNIZES
    a rename/move: canonical_path and current_attributes update; resource_id is
    unchanged.
  - If the matched candidate is at a DIFFERENT path but is still PRESENT (not
    MISSING) -> CONFLICT (two live resources with same content at different
    paths; cannot auto-merge).
  - If the matched candidate is at a DIFFERENT path, is MISSING, but is OUTSIDE
    the recognition horizon (already eligible for removal confirmation) ->
    UNRESOLVED. The hash overlap may be a copy, not a continuation.
  - If no MISSING candidate exists at all (the hash matches a PRESENT resource at
    another path, or matches nothing in the horizon) -> NEW_RESOURCE. This is
    the copy case: a new file with identical content gets its own resource_id,
    not the existing resource's identity.

**Evidence.** Principle 7 (hash optional, but when present it is a strong
content fingerprint). Principle 5 (path != identity). A1 rework: hash != identity
— a content fingerprint is evidence of content equality, not of canonical
identity; continuity context is required to upgrade a cross-path hash match to
MATCHED. D02 Q12: AList/OpenList rename is delete+add because there is no
content-identity signal (`internal/op/fs.go:364,420`); R3 supplies the signal but
does not over-claim identity from a copy.

**Scenarios covered.** 5 (path rename, same content, different path — via
continuity context) and 6 (path move, same content, different parent — via
continuity context). The copy case (same content, new path, no MISSING
candidate) is explicitly NEW_RESOURCE, not MATCHED.

---

#### R4 — hash absent, path + size + mtime heuristic (scenario 8) — ACCEPTED_SEMANTIC

**Precondition.** The entry has NO content_hash. R0 has passed. R1 did not fire
(no STABLE_WITHIN_SCOPE provider_object_id match).

**Frozen principles (A2 rework).**
- observed_path is a continuity hint, NOT identity (principle 5).
- size and mtime are weak evidence. Absent mtime is NOT positive match evidence
  (two entries both lacking mtime is not a match signal).
- Without a STABLE_WITHIN_SCOPE provider_object_id or a content_hash with
  continuity context, same path + same size alone MUST NOT confirm canonical
  identity.
- After an old resource is deleted, a new file at the same path with the same
  size MUST NOT inherit the old resource_id.

**Rule.** This is the degraded path when hash is absent (scenario 8: cannot use
hash for matching).

1. Look up CanonicalResources in the same root at the same observed_path.
2. If exactly one candidate and size matches AND mtime is present and matches on
   both sides -> MATCHED (MEDIUM strength: path + size + mtime all present and
   agree).
3. If exactly one candidate and size matches but mtime DIFFERS (both present) ->
   UNRESOLVED. The content may have changed in place or the path may have been
   reused by a different object; without hash or stable id, the Kernel cannot
   confirm identity continuity. Do NOT MATCHED on same path + same size alone
   (A2 rework). Safe Reconcile contract defines the hold/staging behavior.
4. If mtime is absent on either side -> UNRESOLVED. Absent mtime is NOT positive
   match evidence (A2 rework). size + path without mtime is insufficient to
   confirm canonical identity.
5. If no candidate at this path -> look for a moved candidate: a resource
   recently marked MISSING in this root, within the effective rename/move
   recognition horizon (Section 2.4, A4 rework), whose size AND mtime (both
   present) match. If exactly one -> MATCHED (recognized move without hash). If
   multiple -> CONFLICT (R7). A candidate outside the horizon is NOT considered
   (it may already be removal-confirmed).
6. If none of the above -> NEW_RESOURCE.

**Evidence.** Principle 7 (hash optional). Principle 5 (path is a hint, not
identity). Principle 4 (incomplete input must not authorize destructive
reconcile: same path + same size without mtime or strong id is incomplete).
A2 rework: resolves the direct conflict with R8 — both now treat same path +
size/mtime divergence as UNRESOLVED when no strong signal exists, and neither
treats absent mtime as a match. D02 Q5: AList/OpenList name-only diff
(`internal/search/build.go:224-234`) is the degenerate case of this heuristic;
R4 is richer but now conservative and consistent with R8.

**Scenario covered.** 8 (hash absent).

---

#### R5 — path rename / move with same content (scenarios 5, 6) — ACCEPTED_SEMANTIC

**Precondition.** A resource at old_path was marked MISSING in a recent
generation, within the effective rename/move recognition horizon (Section 2.4,
A4 rework). A new entry appears at new_path with matching content_hash (R3) OR
matching (size, mtime) when hash absent (R4 step 5).

**Rule.** Recognize the move: MATCHED to the missing resource. Update
canonical_path to new_path. This is identity-preserving: resource_id unchanged.

**Note.** R5 is the move-recognition overlay on top of R3/R4. It is listed
separately because the task explicitly requires handling scenarios 5 and 6, and
the recognition depends on the MISSING state (principle 3: missing != deleted),
which is a Kernel canonical-state concept. R5 MUST respect the Move/Removal
Horizon Invariant (Section 2.4): a resource still inside the recognition horizon
cannot have been CONFIRMED_REMOVED.

**Evidence.** Principle 3 (missing != deleted: a missing resource can be
re-recognized). Principle 5 (path != identity). A4 rework: move recognition
horizon and removal horizon are closed (Section 2.4). D02 Q12: rename = delete+add
loses identity; R5 preserves it.

**Scenarios covered.** 5 (path rename), 6 (path move).

---

#### R6 — directory rename / move (scenario 7) — ACCEPTED_SEMANTIC

**Decision (A5 rework): v1 = NO_BATCH_DESCENDANT_PROPAGATION.** This closes the
gate: R6 is no longer CANDIDATE. Directory Move v1 is frozen as the conservative
option.

**Precondition.** A directory CanonicalResource is recognized as moved by R5
(its identity matched at a new path). The directory has descendant resources.

**Rule (v1 — NO_BATCH_DESCENDANT_PROPAGATION).**
- The directory itself MAY MATCH via R5 (its own content_hash, or its own
  path + size + mtime continuity). Directory identity is the directory's own
  resource_id.
- Descendants are NOT batch-propagated. Each descendant entry is matched
  independently by the normal Identity v1 rules (R0-R11) against the canonical
  inventory of the root. A child at old_path/child_rel is NOT auto-matched to a
  new entry at new_path/child_rel merely because the parent directory moved.
- A descendant that cannot be matched by its own evidence -> UNRESOLVED or
  UNOBSERVED (left MISSING), per R11. The Kernel does NOT force-match descendants
  to preserve directory coherence.
- Rationale: v1 prioritizes safety. Batch descendant propagation would require
  coordination with Safe Reconcile staging (Safe Reconcile contract) and completeness assessment
  (Completeness contract), and a partial snapshot could mis-recognize descendants. v1 does
  not guarantee that all descendant identity is automatically preserved across a
  directory move; it guarantees that no descendant is wrongly force-matched.
  Batch propagation is POST_MVP.

**Evidence.** Principle 3 (missing != deleted). Principle 4 (incomplete input
must not authorize destructive reconcile: batch propagation on partial evidence
is destructive). A5 rework: closes the CANDIDATE gap; v1 frozen as the
conservative option. D02 Q12: OpenList directory moves trigger recursive hook
(`internal/op/fs.go:836-856`) but still delete+add by name; R6 v1 does not
replicate that batch behavior, deliberately.

**Scenario covered.** 7 (directory rename / move) — directory identity
preserved; descendants matched individually, not batch-propagated.

---

#### R7 — same name + same size + same mtime collision risk (scenario 9) — ACCEPTED_SEMANTIC

**Precondition.** Fallback matching (R2/R4) reaches the name + size (+ mtime)
level with no stronger signal.

**Rule.**

- If multiple candidates match -> CONFLICT (collision: cannot determine which
  canonical resource this entry is).
- If exactly one candidate matches AND the match is at MEDIUM strength (path +
  size + mtime all PRESENT and match) -> MATCHED. Absent mtime does not qualify
  as MEDIUM (A2 rework).
- If exactly one candidate matches but at WEAK strength (name + size only, no
  mtime, no path anchor, or mtime absent) -> UNRESOLVED. Do NOT force-match
  (R11). Same name + same size without mtime or path anchor is a collision risk,
  not an identity claim. Absent mtime is NOT positive evidence (A2 rework).

**Evidence.** Principle 4 (incomplete input must not authorize destructive
reconcile: a weak match is incomplete evidence). DO NOT constraint (do not
force-match to increase match rate). A2 rework: absent mtime is not a match
signal. D02 Q9: name-only set difference (`internal/search/build.go:232-244`) is
exactly the collision-prone heuristic the Kernel must improve upon.

**Scenario covered.** 9 (same name + same size + same mtime collision risk).

---

#### R8 — old path reused by a different object, imposter (scenario 10) — ACCEPTED_SEMANTIC

**Precondition.** An entry appears at observed_path where a CanonicalResource
already exists, but the evidence does not support identity continuity.

**Rule.**

- If both the entry and the existing resource have a STABLE_WITHIN_SCOPE
  provider_object_id and they DIFFER -> CONFLICT (two different provider objects
  claim the same path; needs resolution).
- If content_hash present on both and they DIFFER -> the entry is a NEW_RESOURCE
  (imposter) at this path; the existing resource is marked MISSING (it may have
  moved elsewhere; R5 may re-recognize it). The path is reassigned to the
  imposter as a new canonical resource. The imposter does NOT inherit the old
  resource_id (A2 rework).
- If no STRONG signal (no hash, no STABLE_WITHIN_SCOPE provider_object_id) and
  size or mtime differ, OR mtime is absent on either side -> UNRESOLVED. Cannot
  determine whether the existing resource changed in place or was replaced by an
  imposter. Do NOT force-match. This is consistent with R4 step 3/4 (A2 rework):
  same path + size/mtime divergence without a strong signal is UNRESOLVED in both
  rules; the previous R4 step 3 / R8 conflict is closed.

**Evidence.** Principle 5 (path != identity: a path can be reused). Principle 4
(incomplete input must not authorize destructive reconcile). A2 rework: unifies
R4 and R8 — both treat same path + weak evidence divergence as UNRESOLVED, and
neither lets a new object inherit an old resource_id. D02 Q12: rename = delete+add
means AList/OpenList cannot distinguish imposter from in-place change; R8 makes
the distinction explicit and conservative.

**Scenario covered.** 10 (old path reused by a different object, imposter).

---

#### R9 — provider_object_id disappears between snapshots (scenario 3) — ACCEPTED_SEMANTIC

**Precondition.** A CanonicalResource has a STABLE_WITHIN_SCOPE provider_object_id.
A new accepted snapshot for the root does NOT contain any entry with that
provider_object_id.

**Rule.** Mark the CanonicalResource MISSING. Do NOT delete it (principle 3).
Do NOT immediately reassign its identity. The resource may reappear in a later
snapshot (transient provider issue, pagination, rate limit). R5 may re-recognize
it if it reappears at a new path with matching content. Transition to
CONFIRMED_REMOVED (if defined by Safe Reconcile contract) MUST respect the Move/Removal Horizon
Invariant (Section 2.4, A4 rework): a resource still inside the rename/move
recognition horizon cannot be CONFIRMED_REMOVED.

**Evidence.** Principle 3 (missing != deleted). A4 rework: removal horizon is
closed with the move recognition horizon (Section 2.4). D02 Q10: AList/OpenList
has no partial-list protection (`internal/search/build.go:232-244`); a partial
list deletes "missing" entries. R9 is the Kernel-owned fix: disappearance ->
MISSING, not deletion.

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
  replaced the old one at this path. Do NOT force-match either way. Safe Reconcile contract
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

### 2.4 Move/Removal Horizon Invariant — ACCEPTED_SEMANTIC (A4 rework)

**Problem closed.** The original R5 move-recognition recency window and the
removal grace period (R9 -> CONFIRMED_REMOVED) were unrelated. This allowed two
violations: (a) a resource still inside a valid rename/move continuity
recognition horizon could already be CONFIRMED_REMOVED; (b) the move recognition
window could expire first, permanently losing a real move's identity, and then
the old resource be deleted.

**Frozen hard invariant.**

> A resource that is still within the legitimate rename/move continuity
> recognition horizon MUST NOT already be CONFIRMED_REMOVED.

Equivalently: removal confirmation (transition MISSING -> CONFIRMED_REMOVED,
which Safe Reconcile contract may define) MUST NOT occur before the rename/move recognition
horizon for that resource has expired.

**Definitions (concept-level; exact durations are runtime config, Gate 1C).**

- `move_recognition_horizon`: the interval after a resource is marked MISSING
  during which R5/R3 may still re-recognize it as a rename/move of the same
  canonical identity.
- `removal_grace_period`: the interval after a resource is marked MISSING during
  which it MUST NOT be confirmed removed (retained as MISSING for audit and
  potential re-recognition).

**Required relationship (frozen at Gate 1B).**

```
removal_grace_period >= move_recognition_horizon
```

That is, the removal grace period MUST be at least as long as the move
recognition horizon. A resource eligible for move re-recognition is never
simultaneously eligible for removal confirmation. This closes both violations:

- (a) cannot happen: removal confirmation requires the removal grace period to
  have elapsed, which is >= the move horizon, so the move horizon has expired
  first.
- (b) cannot happen: the move horizon expires no later than the removal grace
  period, so a real move is re-recognizable for the full recognition window
  before any removal confirmation is permitted.

**Boundary cases.**
- If `move_recognition_horizon = 0` (move recognition disabled), the invariant
  reduces to "removal grace period >= 0", trivially satisfied; resources may be
  confirmed removed after the grace period with no move re-recognition.
- If both are infinite (never confirm removal), resources stay MISSING forever;
  the invariant holds but audit/retention grows unbounded — a deployment
  trade-off, not a Kernel domain violation.

**Evidence.** Principle 3 (missing != deleted: a MISSING resource may be
re-recognized). Principle 4 (incomplete input must not authorize destructive
reconcile: premature removal confirmation is destructive). A4 rework: closes the
R5/R9 horizon gap. D02 Q12: rename = delete+add loses identity because the
system has no horizon relationship; this invariant is the Kernel-owned fix.

**Scope.** This section defines the relationship only. Exact durations, the
MISSING -> CONFIRMED_REMOVED transition state machine, and retention policy are
Safe Reconcile contract / runtime config (Gate 1C). Gate 1B freezes the invariant and the
relationship.

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
| 1 | provider_object_id present and stable (STABLE_WITHIN_SCOPE) | R1 | MATCHED | ACCEPTED_SEMANTIC |
| 2 | provider_object_id absent, fallback | R2 -> R3/R4/R7 | MATCHED / NEW_RESOURCE / UNRESOLVED / CONFLICT per frozen downstream rules | ACCEPTED_SEMANTIC |
| 3 | provider_object_id disappears between snapshots | R9 | MISSING (not deleted); removal respects horizon (2.4) | ACCEPTED_SEMANTIC |
| 4 | provider_object_id changes unexpectedly | R10 | UNRESOLVED or CONFLICT | ACCEPTED_SEMANTIC |
| 5 | path rename (same content, different path) | R3, R5 | MATCHED only with continuity context; copy -> NEW_RESOURCE | ACCEPTED_SEMANTIC |
| 6 | path move (same content, different parent) | R3, R5 | MATCHED only with continuity context; copy -> NEW_RESOURCE | ACCEPTED_SEMANTIC |
| 7 | directory rename / move | R6 | directory MATCHED; descendants matched individually (NO_BATCH_DESCENDANT_PROPAGATION); unmatched descendants UNRESOLVED/MISSING | ACCEPTED_SEMANTIC |
| 8 | hash absent (cannot use hash) | R4 | MATCHED (path+size+mtime all present) / UNRESOLVED otherwise | ACCEPTED_SEMANTIC |
| 9 | same name + same size + same mtime (collision risk) | R7 | CONFLICT (multi) or UNRESOLVED (weak/absent mtime) | ACCEPTED_SEMANTIC |
| 10 | old path reused by a different object (imposter) | R8 | CONFLICT / NEW_RESOURCE / UNRESOLVED per evidence; no resource_id inheritance | ACCEPTED_SEMANTIC |
| 11 | Root scope boundaries (identity does not cross roots) | R0 | NEW_RESOURCE in own root; never cross-root MATCHED | ACCEPTED_SEMANTIC |

All 11 scenarios are covered. No scenario is force-matched; UNRESOLVED and
CONFLICT are reachable for scenarios 2, 4, 7, 9, 10. A1 rework: a content copy
(scenarios 5/6 without continuity context) is NEW_RESOURCE, not MATCHED. A2
rework: absent mtime is never positive evidence. A4 rework: move recognition and
removal horizons are closed (Section 2.4). A5 rework: R6 frozen as
NO_BATCH_DESCENDANT_PROPAGATION.

---

## 5. Contract Self-Check

> Rework note: the original "legacy verification wrapper" wrapper (self-run
> counterexample-hunter / contract-consistency-checker / evidence-reader
> temporary helper agents) is removed. The valuable analysis is retained as direct Worker
> self-checks. No temporary helper agent organization layer remains.

| Check | Question | Result | Verification | Decision |
|-------|----------|--------|--------------|----------|
| Scenario counterexample check | Do the identity rules R0-R11 produce a wrong result for any of the 11 scenarios, or allow force-matching? | No scenario is force-matched. UNRESOLVED reachable in 2,4,7,9,10. CONFLICT reachable in 1(multi-id),4,7,9,10. R0 prevents cross-root identity leak. R9 never deletes. R11 enforces conservative fallback. R6 v1 (NO_BATCH_DESCENDANT_PROPAGATION) does not force-match descendants. A1 rework: copy -> NEW_RESOURCE, not MATCHED. A2 rework: absent mtime never positive. A4 rework: horizons closed (2.4). | VERIFIED (Worker re-checked each rule against its scenario and the evidence citations) | ADOPT (rules R0-R5, R7-R11); R6 ADOPTED as v1 NO_BATCH_DESCENDANT_PROPAGATION |
| Contract consistency check | Are the Domain Model (Section 1) and Identity Rules (Section 2) mutually consistent, and consistent with Gate 1A frozen boundaries? | Consistent. resource_id != path != provider_object_id (1.4 vs 1.3). parent_ref remains Collector-local (1.3). hash and provider_object_id remain OPTIONAL (1.3, 1.6, R1-R4). provider_identity_assurance qualifies provider_object_id (1.6, R1). Generation is per-root (1.5) matching disjoint partitions (Section 3). Snapshot is immutable input (1.2) matching Gate 1A A3. CanonicalResource separates `resource_presence` from `removal_evidence_state` (1.4), matching principle 3. R4 and R8 now consistent (A2 rework). R3 requires continuity context (A1 rework). Move/Removal horizon invariant defined (2.4, A4 rework). No Safe Reconcile state machine designed (deferred to Safe Reconcile contract). No completeness acceptance designed (deferred to Completeness contract). No Change Journal format designed (deferred to Safe Reconcile contract). | VERIFIED (The contract review cross-checked each Section 1 field against Gate 1A A1-A5 and the DO NOT list) | ADOPT |
| Evidence citation check | Do all ACCEPTED_SEMANTIC rules cite a principle, D02 finding, or Gate 1A boundary? | Yes. R0: principle 5 + A1.3 + D02 Q11. R1: A1.2 + D02 Q7/Q8 + A3 rework. R3: principle 7 + principle 5 + D02 Q12 + A1 rework. R4: principle 7 + principle 5 + principle 4 + D02 Q5 + A2 rework. R5: principle 3 + D02 Q12 + A4 rework. R6: principle 3 + principle 4 + D02 Q12 + A5 rework. R7: principle 4 + D02 Q9 + A2 rework. R8: principle 5 + principle 4 + D02 Q12 + A2 rework. R9: principle 3 + D02 Q10 + A4 rework. R10: principle 5 + principle 6 + A1.2. R11: DO NOT + principle 4. Section 2.4: principle 3 + principle 4 + A4 rework. Section 3: A1.3 + D02 Q11 + principle 9. | VERIFIED (every ACCEPTED_SEMANTIC tag has at least one citation) | ADOPT |

---

## 6. Quality Markers Summary

| Marker | Count | Items |
|--------|-------|-------|
| ACCEPTED_SEMANTIC | Domain: 6 concepts (1.1-1.6) + Root ownership (Section 3) + Identity Result States (2.1) + Rules R0, R1, R3, R4, R5, R6, R7, R8, R9, R10, R11 + Rule Precedence (2.3) + Move/Removal Horizon Invariant (2.4) + Root Lifecycle (Section 10) | Core model and conservative identity rules |
| DEFERRED | Safe Reconcile state machine (Safe Reconcile contract); completeness acceptance (Completeness contract); Change Journal format (Safe Reconcile contract); conflict resolution policy (Gate 1A B3, deferred); Collector selection (Gate 1C); Store adapter (Gate 1C); exact horizon durations (Gate 1C runtime config) | Out of scope per DO NOT |
| POST_MVP | Identity v2; cross-root dedup; batch descendant propagation for directory move (R6 v2) | Deferred unscheduled; not in Gate 1C scope |
| REJECTED | Option B (overlapping root partitions) | Section 3 |

Every identity rule is tagged. Every rule cites evidence (principle, D02
finding, or Gate 1A boundary). UNRESOLVED and CONFLICT are first-class results
(2.1), reachable by R7, R8, R10, and R11, and are NOT errors. R6 is frozen as
ACCEPTED_SEMANTIC (NO_BATCH_DESCENDANT_PROPAGATION); batch propagation is POST_MVP.

---

## 7. DO NOT Compliance

| Constraint | Status |
|------------|--------|
| Did not require hash or provider_object_id as mandatory | Honored — 1.3 and 1.6 mark both OPTIONAL; R1/R3 are conditional on presence; R4/R7 handle absence |
| Did not design PostgreSQL schema/SQL/migration | Honored — all fields are concept contracts; no table/column/type specified (principle 9) |
| Did not design Safe Reconcile state machine | Honored — 2.1 gives reconcile IMPLICATIONS only as outline; state machine deferred to Safe Reconcile contract |
| Did not design completeness acceptance | Honored — completeness_flag is referenced as Gate 1A A3/A1.4/B2; acceptance criteria deferred to Completeness contract |
| Did not design Change Journal | Honored — referenced as Gate 1A A1.6; format deferred to Safe Reconcile contract |
| Did not write product code | Honored — design document only |
| Did not select final Collector | Honored — owning_collector_ref is OPTIONAL; selection deferred to Gate 1C |
| Did not design Scanner checkpoint/resume | Honored — not in model; Gate 1A REJECTED it from Kernel (principle 10) |
| Did not design native delta / incremental | Honored — Snapshot is the input unit (principle 8); native delta is NOT Kernel input (Gate 1A A3) |
| Did not force-match to increase match rate | Honored — R11 explicitly forbids it; UNRESOLVED is the required result for weak evidence |
| A1: did not equate hash with identity | Honored — R3 requires continuity context for cross-path hash MATCH; a copy yields NEW_RESOURCE; hash not required of every Provider (1.6, R3) |
| A2: did not let same path + same size alone confirm identity | Honored — R4 step 3/4 and R8 require mtime present+match or a STRONG signal; absent mtime is not positive evidence; no resource_id inheritance on path reuse |
| A3: did not treat provider_object_id field presence as STRONG | Honored — provider_identity_assurance qualifies the id; only STABLE_WITHIN_SCOPE is STRONG (1.6, R1) |
| A4: did not leave move and removal horizons unrelated | Honored — Section 2.4 freezes removal_grace_period >= move_recognition_horizon |
| A5: did not leave R6 as CANDIDATE | Honored — R6 frozen as ACCEPTED_SEMANTIC, NO_BATCH_DESCENDANT_PROPAGATION; batch propagation is POST_MVP |
| Rework: did not use temporary helper agents / legacy verification wrapper | Honored — Section 5 is Contract Self-Check; no temporary helper agent organization layer |
| Formal gate-route check | Honored — unscheduled capabilities use POST_MVP / DEFERRED_UNSCHEDULED rather than inventing another gate |

---

## 8. DEFERRED Items (explicit handoff)

> Rework note: the formal gate route is clean. Unscheduled items use POST_MVP or
> DEFERRED_UNSCHEDULED. Formal route: Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC.

| Item | Owner | Reason |
|------|-------|--------|
| Safe Reconcile state machine (staging, atomicity, UNRESOLVED/CONFLICT hold behavior, MISSING -> CONFIRMED_REMOVED transition) | Safe Reconcile contract (Gate 1B) | DO NOT constraint; R6/R9/R10 outcomes need reconcile staging; must respect Move/Removal Horizon Invariant (2.4) |
| Completeness acceptance criteria (beyond the flag) | Completeness contract (Gate 1B) | DO NOT constraint; interacts with R4/R5 move recognition |
| Canonical Change Journal format | Safe Reconcile contract (Gate 1B) | DO NOT constraint; records the identity-preserving transitions R3/R5/R6 produce |
| Conflict resolution policy (for CONFLICT results) | Gate 1B (Gate 1A B3 deferred) | CONFLICT is produced by R7/R8/R10; resolution policy is a separate design |
| Exact move_recognition_horizon and removal_grace_period durations | Gate 1C runtime config | Section 2.4 freezes the relationship; durations are deployment config |
| Collector selection per root | Gate 1C | owning_collector_ref OPTIONAL until then |
| provider_identity_assurance mapping per Collector/Adapter | Gate 1C | Section 1.6 defines the concept; Gate 1C maps AList/rclone/adapters |
| Store adapter mapping (canonical state to schema) | Gate 1C | principle 9; Kernel Domain must not bind schema/ORM |
| Batch descendant propagation for directory move (R6 v2) | POST_MVP | R6 v1 frozen as NO_BATCH_DESCENDANT_PROPAGATION; batch is unscheduled |
| Identity v2 | POST_MVP | Beyond Identity v1 scope; unscheduled |
| Cross-root dedup | POST_MVP | Disjoint partitions (Section 3) forbid cross-root identity; dedup is a Consumer/post-MVP concern |
| Consumer default visibility of DEPRECATED/DELETED roots | Gate 1C Query semantics | Section 10 defines lifecycle; query hiding is Gate 1C |

---

## 9. DONE WHEN Verification

| Criterion | Met | Where |
|------------|-----|------|
| Domain Model defined with all 6 concepts | Yes | Section 1 (ResourceRoot, Snapshot, SnapshotEntry, CanonicalResource, Generation, IdentityEvidence) |
| Identity v1 rules cover all 11 scenarios | Yes | Section 4 coverage table; rules R0-R11 |
| Identity Result states defined | Yes | Section 2.1 (MATCHED, NEW_RESOURCE, UNRESOLVED, CONFLICT) |
| Root ownership resolved (A or B with justification) | Yes | Section 3 (Option A, disjoint, with 5-point justification) |
| Contract Self-Check present (rework: was legacy verification wrapper) | Yes | Section 5 |
| Move/Removal Horizon Invariant defined (A4 rework) | Yes | Section 2.4 |
| No DO NOT violations | Yes | Section 7 |
| Formal gate-route check | Yes | Section 8 uses POST_MVP / DEFERRED_UNSCHEDULED |

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

## 10. Root Lifecycle — ACCEPTED_SEMANTIC

> This section resolves the previously identified root delete/recreate gap and is part of the final Gate 1B contract. It is consistent with the ResourceRoot model
> (Sec 1.1) and Root Ownership Option A (disjoint partitions, Sec 3).
> Rework (Issue #30): wording corrected per Architect Review. DELETED is a
> logical retirement, not physical erasure; retention is an explicit policy,
> not a leak fix.

### 10.1 Root lifecycle states — ACCEPTED_SEMANTIC

| State | Meaning | Reconcile behavior |
|-------|---------|-------------------|
| NEW | Root created, no snapshot received yet | No canonical resources exist for this root |
| ACTIVE | Root is being reconciled normally | Snapshots accepted; reconcile proceeds per Safe Reconcile contract |
| DEPRECATED | Root is being retired; no new snapshots expected | Existing canonical resources preserved; snapshots still accepted if submitted (defensive) |
| DELETED | Root is logically retired / tombstoned. The root_id and its canonical partition are retired in the Canonical Inventory. This is NOT "the database row physically does not exist" — it is a logical state. | No new snapshots accepted; canonical resources for this root are not reconciled |

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
3. The old root's Canonical Partition is retained for audit/history. It no
   longer participates in new reconcile. This is an explicit retention policy,
   not a leak fix (rework: the original "frozen, so no leak" wording is
   replaced — retention is a deliberate policy decision, not a side effect of
   freezing).
4. A Journal event `root-deleted` is emitted (canonical transition).
5. If the same physical source is re-added, it MUST receive a new root_id
   (NEW state). The old root's resources are NOT merged with the new root's
   resources (disjoint partitions, Sec 3 Option A).

### 10.4 Root recreation — ACCEPTED_SEMANTIC

Root recreation is modeled as creating a NEW root with a new root_id. It is
NOT a lifecycle transition of the old root. The old root remains DELETED
forever. The new root starts fresh with no canonical resources.

This resolves Adversarial review scenario 20: root delete/recreate has defined behavior
(old root logically retired + new root created), explicit retention of the old
partition for audit/history, and no root_id reuse (immutable). Consumer default
visibility of DEPRECATED/DELETED roots is a Gate 1C Query semantics decision,
not defined here.

### 10.5 Journal events for root lifecycle — ACCEPTED_SEMANTIC

| Transition | Journal event |
|------------|---------------|
| NEW -> ACTIVE | (none — first reconcile generates resource events) |
| ACTIVE -> DEPRECATED | `root-deprecated` |
| * -> DELETED | `root-deleted` |

These are canonical transition events recorded in the Change Journal, not
provider-reported events (principle 8).
