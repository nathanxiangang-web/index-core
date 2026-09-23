# Gate 1B Worker D -- Adversarial Semantic Attack

> Role: INDEPENDENT ADVERSARIAL REVIEWER
> Author: Worker D (w04)
> Task-ID: gate1b-wd
> Date: 2026-09-23
> Baseline: 7aa798a
> Status: ADVERSARIAL_REVIEW_COMPLETE
> Output: `.work/gate1b/W-D-ADVERSARIAL-CASES.md`
>
> Scope rule: this document attacks the semantic designs of Worker A
> (Identity), Worker B (Completeness), and Worker C (Reconcile + Failure +
> Journal). It does NOT design the primary solution, does NOT write product
> code, does NOT select Collector, does NOT design algorithm, does NOT design
> PostgreSQL schema, does NOT design Scanner checkpoint/resume. Every scenario
> is resolved to exactly one of PASS / WARNING / VIOLATION with cited evidence
> from A/B/C.

---

## 0. Attack methodology

I read all three Gate 1B design documents in full:

- W-A-DOMAIN-IDENTITY.md (Worker A: Domain Model + Stable Identity v1)
- W-B-SNAPSHOT-COMPLETENESS.md (Worker B: Snapshot + Completeness Acceptance)
- W-C-RECONCILE-FAILURE-JOURNAL.md (Worker C: Safe Reconcile + Failure + Journal)

For each of the 24 mandatory scenarios, I constructed concrete adversarial
inputs and traced them through the identity rules (Worker A R0-R11),
completeness classification (Worker B C-1 through C-10), reconcile state
machine (Worker C transitions 1-10), and journal rules (Worker C J1-J7).
A scenario is PASS only when no adversarial input produces a violation of
an accepted principle. A scenario is VIOLATION when a current design
directly contradicts an accepted principle or leaves a mandatory scenario
without a defined outcome. A scenario is WARNING when the design does not
violate a principle but contains an ambiguity, CANDIDATE dependency, or
deferred item that a future gate or implementation could resolve into a
violation.

I also performed cross-worker consistency attacks (Section 6) targeting
the composition boundaries between A/B/C, since each worker self-verified
in isolation but the three contracts must compose safely.

---

## 1. Identity scenarios (1-8)

### Scenario 1: File rename (same content, different path) -> identity preserved?

- Identity Result: Worker A R3 (content_hash match at new path) + R5 (move
  recognition overlay on MISSING) -> MATCHED. resource_id unchanged;
  canonical_path updated. (W-A Sec 2.2 R3, R5; Sec 4 scenario 5)
- Completeness Result: If snapshot is COMPLETE (Worker B C-9), old path ->
  MISSING -> eligible for REMOVAL_CANDIDATE promotion. If PARTIAL (C-3/C-10),
  old path -> MISSING, not promoted. Either way, R5 recognizes the move
  before promotion. (W-B Sec 4.1 eligibility table)
- Allowed Canonical Action: RENAME transition (W-C Sec 1.1 row 3). Update
  path on ResourceEntry, preserve resource_id. Under PARTIAL: RENAME
  permitted (W-C Sec 2.2: "ADD, UPDATE, RENAME, MOVE, UNCHANGED are
  permitted for RESOLVED entries").
- Forbidden Action: MUST NOT treat rename as delete+add (principle 5; D02
  Q12). MUST NOT assign new resource_id. MUST NOT remove the old-path
  resource before R5 recognizes the move.
- Journal Result: `resource-renamed` event (W-C Sec 3.1 row 3).
- Verdict: WARNING
- Evidence: W-A R3/R5 ACCEPTED_SEMANTIC; W-C Sec 1.1 row 3 ACCEPTED_SEMANTIC.
  Attack: R5 requires the old-path resource was "marked MISSING in a recent
  generation (within a configurable recency window, v1 candidate)." If the
  rename occurs and the next snapshot arrives AFTER the recency window
  expires, R5 does not fire. The new-path entry falls to R4 step 4 (move
  lookup for recently MISSING resources) -- but "recently" is the same
  recency window. If both windows have expired, the entry is NEW_RESOURCE
  and identity is lost. The recency window is CANDIDATE (W-A Sec 2.2 R5:
  "within a configurable recency window, v1 candidate"). No ACCEPTED bound
  is given. This is safe under principle 3 (MISSING != DELETED -- the old
  resource is not deleted, just unmatched), but identity continuity is not
  guaranteed across long rename gaps. WARNING.

### Scenario 2: File move (same content, different parent) -> identity preserved?

- Identity Result: Worker A R3 + R5 -> MATCHED. parent_ref changes,
  resource_id unchanged. (W-A Sec 2.2 R3, R5; Sec 4 scenario 6)
- Completeness Result: Same as Scenario 1. COMPLETE or PARTIAL both permit
  RENAME/MOVE for RESOLVED entries. (W-B Sec 4.1; W-C Sec 2.2)
- Allowed Canonical Action: MOVE transition (W-C Sec 1.1 row 4). Update
  parent + path on ResourceEntry, preserve resource_id.
- Forbidden Action: MUST NOT treat move as delete+add. MUST NOT assign new
  resource_id. MUST NOT cross-root match (R0 prevents this).
- Journal Result: `resource-moved` event (W-C Sec 3.1 row 4).
- Verdict: PASS
- Evidence: W-A R3/R5 ACCEPTED_SEMANTIC; W-C Sec 1.1 row 4 ACCEPTED_SEMANTIC.
  Attack attempted: cross-root move (different parent in different root).
  R0 (W-A Sec 2.2 R0) prevents cross-root identity matching. The resource
  is NEW_RESOURCE in the new root and MISSING in the old root. This is
  correct per Section 3 (disjoint partitions, ACCEPTED_SEMANTIC). Identity
  is not preserved across roots, which is the design decision. Within-root
  move with content_hash present is fully covered. PASS.

### Scenario 3: Directory rename/move -> child identities preserved?

- Identity Result: Worker A R6 (directory move propagation) -> CANDIDATE.
  R6 propagates move to descendants: for each descendant at old_path/child_rel,
  look for new entry at new_path/child_rel. If found and identity-consistent
  -> MATCHED. If not found -> MISSING. (W-A Sec 2.2 R6)
- Completeness Result: If PARTIAL, not all descendants may be in the
  snapshot. R6 says "If a descendant is not found at the expected new
  relative path -> leave it MISSING (do not force-match)." (W-A R6)
- Allowed Canonical Action: MOVE for the directory; per-descendant MATCHED
  or MISSING. (W-C Sec 1.1 rows 4, 5)
- Forbidden Action: MUST NOT force-match absent descendants. MUST NOT
  batch-delete descendants not found at new path.
- Journal Result: `resource-moved` for directory; `resource-moved` or
  `resource-renamed` per matched descendant. MISSING descendants produce
  no journal event. (W-C Sec 3.1)
- Verdict: WARNING
- Evidence: W-A R6 CANDIDATE. Attack: R6 is explicitly CANDIDATE and
  depends on Worker C reconcile staging and Worker B completeness (W-A
  Sec 2.2 R6: "Batch move recognition requires coordination with Safe
  Reconcile staging (Worker C) and completeness assessment (Worker B)").
  Worker C does not explicitly address directory move propagation in its
  state machine. If R6 is never implemented, directory move falls back to
  per-child R3/R5 recognition. Children with content_hash are recognized
  individually. Children WITHOUT hash (and without mtime) fall to R4/R7
  -> UNRESOLVED or NEW_RESOURCE -> identity lost per child. The fallback
  is safe (no false match) but does not preserve child identities in the
  hash-absent case. WARNING.

### Scenario 4: Same path reused by different object -> imposter detected?

- Identity Result: Worker A R8 (imposter at existing path). If content_hash
  present on both and DIFFER -> NEW_RESOURCE (imposter); existing resource
  marked MISSING. If provider_object_id present on both and DIFFER ->
  CONFLICT. If no strong signal and size/mtime differ -> UNRESOLVED.
  (W-A Sec 2.2 R8)
- Completeness Result: If COMPLETE, the MISSING old resource can be
  promoted to REMOVAL_CANDIDATE. If PARTIAL, MISSING stays. (W-B Sec 4.1)
- Allowed Canonical Action: If hash differs -> NEW_RESOURCE (imposter) +
  existing -> MISSING. If UNRESOLVED -> CONFLICT (W-C Sec 1.2 rule 2), no
  canonical mutation.
- Forbidden Action: MUST NOT force-match the new entry to the existing
  resource when evidence contradicts identity (R11). MUST NOT silently
  overwrite the existing resource's attributes.
- Journal Result: If NEW_RESOURCE: `resource-added` for imposter. If
  existing -> CONFIRMED_REMOVED (later): `resource-removed`. If CONFLICT:
  no journal event. (W-C Sec 3.1)
- Verdict: WARNING
- Evidence: W-A R8 ACCEPTED_SEMANTIC. Attack 1: If the imposter has the
  SAME content_hash as the existing resource (hash collision or identical
  content different object), R3 fires with exactly one match -> MATCHED.
  The imposter is falsely matched to the existing resource. R3 says
  "Multiple matches -> CONFLICT" but does not address the single-match
  false-positive case. This is the hash collision limitation -- not a
  principle violation (principle 7: hash is optional, not guaranteed
  unique), but the design does not flag the risk. Attack 2: If hash is
  absent on the imposter and size/mtime differ -> UNRESOLVED -> CONFLICT.
  The old resource stays PRESENT, the new entry is CONFLICT. The canonical
  path still reflects the old resource, not reality. Safe but stale.
  WARNING.

### Scenario 5: Provider ID disappears between snapshots -> identity continuity?

- Identity Result: Worker A R9 (provider_object_id disappeared -> MISSING,
  not deleted). "Do NOT delete it. Do NOT immediately reassign its
  identity. The resource may reappear." (W-A Sec 2.2 R9)
- Completeness Result: If COMPLETE, MISSING -> eligible for REMOVAL_CANDIDATE
  (W-C Sec 1.3.1 C1). If PARTIAL, MISSING stays, not promoted (W-C Sec 1.3.1
  C1: "A PARTIAL snapshot extends missing_since but does NOT promote").
- Allowed Canonical Action: MISSING (state flag only, no canonical content
  mutation). If later COMPLETE + criteria met -> REMOVAL_CANDIDATE ->
  CONFIRMED_REMOVED. (W-C Sec 1.3)
- Forbidden Action: MUST NOT delete on disappearance (principle 3). MUST
  NOT immediately reassign identity. MUST NOT produce `resource-removed`
  journal event on first MISSING observation.
- Journal Result: No journal event for MISSING. `resource-removed` only
  after CONFIRMED_REMOVED. (W-C Sec 3.1: "MISSING ... produce NO journal
  event")
- Verdict: WARNING
- Evidence: W-A R9 ACCEPTED_SEMANTIC; W-C Sec 1.3 ACCEPTED_SEMANTIC.
  Attack: R9 and R5 interaction. If the provider_object_id disappears AND
  the resource reappears at a new path with matching content, R5 should
  recognize the move (MATCHED to the MISSING resource). But R9 says "Do
  NOT immediately reassign its identity." R5 says "MATCHED to the missing
  resource." These are evaluated at different phases: R5 is per-entry (the
  new-path entry), R9 is per-canonical-resource (the old resource not in
  snapshot). If R5 fires first, the resource is MATCHED (identity preserved,
  path updated). If R9 fires first, the resource is MISSING. The reconcile
  ordering between per-entry rules (R5) and per-canonical-resource rules
  (R9) is not specified. W-A Sec 2.3 says "R9 and R10 are evaluated against
  canonical state (resources not in the snapshot), not against the entry,
  so they run as part of reconcile, not per-entry." This implies R5 (per-entry)
  runs first, then R9 (reconcile phase) runs for resources NOT matched by
  any entry. If R5 matched the new-path entry to the missing resource, the
  resource is no longer "not in the snapshot" -- it was matched. So R9 does
  not fire. This is correct. But the ordering is implicit, not explicitly
  specified as a hard rule. WARNING.

### Scenario 6: Provider ID changes unexpectedly -> identity handling?

- Identity Result: Worker A R10 (provider_object_id changed -> UNRESOLVED
  or CONFLICT). If new ID matches a different existing resource -> CONFLICT.
  Else -> UNRESOLVED. (W-A Sec 2.2 R10)
- Completeness Result: UNRESOLVED -> CONFLICT (W-C Sec 1.2 rule 2). No
  canonical mutation. Completeness class does not affect this -- CONFLICT
  regardless of COMPLETE/PARTIAL.
- Allowed Canonical Action: No canonical mutation. CONFLICT recorded in
  reconcile result. (W-C Sec 1.1 row 8; Sec 2.1)
- Forbidden Action: MUST NOT force-match to either the old or new identity.
  MUST NOT silently update the provider_object_id on the existing resource.
- Journal Result: No journal event (CONFLICT produces no event, W-C Sec 3.1).
- Verdict: WARNING
- Evidence: W-A R10 ACCEPTED_SEMANTIC; W-C Sec 2.1 ACCEPTED_SEMANTIC.
  Attack: Provider ID scheme migration. If the provider reissues all IDs
  (e.g., switching from inode to object version), every resource's
  provider_object_id changes. R10 fires for every resource -> all UNRESOLVED
  -> all CONFLICT. The entire canonical inventory is frozen -- no mutations
  permitted until every conflict is manually resolved. This is safe
  (principle 1: canonical truth preserved) but operationally catastrophic
  for large inventories. The design provides no bulk resolution path for
  provider ID scheme changes. WARNING.

### Scenario 7: Same name + same size + same mtime -> collision not forced-match?

- Identity Result: Worker A R7 (collision/weak-signal resolution). If
  multiple candidates match -> CONFLICT. If exactly one candidate at MEDIUM
  strength (path + size + mtime all match) -> MATCHED. If exactly one
  candidate at WEAK strength (name + size only, no mtime, no path anchor)
  -> UNRESOLVED. R11 (no force-match) enforces UNRESOLVED as first-class.
  (W-A Sec 2.2 R7, R11)
- Completeness Result: UNRESOLVED -> CONFLICT (W-C Sec 1.2 rule 2). No
  canonical mutation.
- Allowed Canonical Action: CONFLICT (no mutation) or MATCHED (only at
  MEDIUM strength with path anchor).
- Forbidden Action: MUST NOT force-match at WEAK strength (R11). MUST NOT
  collapse UNRESOLVED into MATCHED or NEW_RESOURCE to increase match rate.
- Journal Result: No event for CONFLICT. `resource-updated` or
  `resource-renamed`/`resource-moved` if MATCHED at MEDIUM.
- Verdict: PASS
- Evidence: W-A R7 ACCEPTED_SEMANTIC; R11 ACCEPTED_SEMANTIC. The scenario
  asks "collision not forced-match?" R7 explicitly returns UNRESOLVED for
  WEAK strength (name + size only, no mtime, no path anchor). R11 explicitly
  forbids force-matching. The design is correct for the stated scenario.
  PASS.

### Scenario 8: Hash missing -> matching still works without hash?

- Identity Result: Worker A R4 (path + size + mtime heuristic, hash absent).
  Step 1: lookup by path. Step 2: if size + mtime match -> MATCHED (MEDIUM).
  Step 3: if size matches, mtime differs -> MATCHED with attribute update.
  Step 4: if no candidate at path -> look for moved candidate (size + mtime
  match). Step 5: none -> NEW_RESOURCE. (W-A Sec 2.2 R4)
- Completeness Result: Matching proceeds regardless of completeness class.
  Identity resolution is independent of completeness. (W-A/W-B separation)
- Allowed Canonical Action: MATCHED (MEDIUM) -> UPDATE/UNCHANGED. NEW_RESOURCE
  -> ADD. UNRESOLVED -> CONFLICT. (W-C Sec 1.2)
- Forbidden Action: MUST NOT reject the entry for lacking hash (W-A Sec 1.3:
  "The Kernel MUST NOT reject an entry solely for lacking hash or
  provider_object_id"). MUST NOT require hash as mandatory.
- Journal Result: Per transition: `resource-added`, `resource-updated`, or
  no event for CONFLICT.
- Verdict: WARNING
- Evidence: W-A R4 ACCEPTED_SEMANTIC; W-A Sec 1.3 DO NOT compliance.
  Attack: R4 step 2 says "size matches and mtime matches (or both absent
  consistently)." The phrase "both absent consistently" is ambiguous. If
  the entry has NO size and NO mtime (as in D02 Q7: SearchNode has only
  Parent, Name, IsDir, Size -- no mtime), and the candidate also has no
  mtime, do they match? If size is present on both and matches, but mtime
  is absent on both, is that "both absent consistently" -> MATCHED? The
  strength table (W-A Sec 1.6) says MEDIUM = "path + size + mtime match."
  If mtime is absent, is the strength MEDIUM or WEAK? R7 says WEAK = "name
  + size only, no mtime, no path anchor." But R4 has a path anchor. The
  interaction between R4's "both absent consistently" and R7's WEAK
  definition is unclear. A real system (D02 Q7: SearchNode with no mtime)
  hits this ambiguity. WARNING.

---

## 2. Completeness scenarios (9-14)

### Scenario 9: Partial scan -> destructive removal blocked?

- Identity Result: Identity resolution proceeds normally for observed
  entries. Unobserved canonical resources are not identity-matched.
- Completeness Result: Worker B C-3 (traversal_status = partial -> PARTIAL)
  ACCEPTED_SEMANTIC. Destructive reconcile BLOCKED (W-B Sec 4.1).
  Additive-only permitted.
- Allowed Canonical Action: ADD, UPDATE, RENAME, MOVE, UNCHANGED for
  RESOLVED entries. MISSING for unobserved (state retained, not promoted).
  (W-C Sec 2.2)
- Forbidden Action: MUST NOT remove entries absent from the snapshot
  (INV-004). MUST NOT promote MISSING to REMOVAL_CANDIDATE under PARTIAL
  (W-C Sec 1.3.1 C1).
- Journal Result: `resource-added`, `resource-updated`,
  `resource-renamed`, `resource-moved` for applicable transitions. No
  `resource-removed` events. (W-C Sec 2.2)
- Verdict: WARNING
- Evidence: W-B C-3 ACCEPTED_SEMANTIC; W-C Sec 2.2 ACCEPTED_SEMANTIC.
  Attack: Silent partial scan. The Collector reports traversal_status =
  success but the scan is actually partial (e.g., AList swallows errors
  when virtualFiles non-empty, D02 section 4). If all other dimensions are
  positive (fresh, no skips, no entry count decline), Worker B C-9 fires
  -> COMPLETE -> destructive reconcile ALLOWED. Entries missing from the
  silently-partial snapshot are removed from canonical state. This is the
  irreducible ambiguity acknowledged by Worker B Sec 5.1: "the Kernel
  cannot be more certain than the evidence allows." The design accepts
  this risk only when ALL other dimensions are positive, but a Collector
  that silently truncates AND reports success AND provides positive
  freshness evidence defeats the gate. WARNING.

### Scenario 10: Permission denied subtree -> evidence captured, removal blocked?

- Identity Result: Entries in the permission-denied subtree are not
  observed. No identity resolution for them.
- Completeness Result: Worker B C-4 (error_summary non-empty -> PARTIAL)
  ACCEPTED_SEMANTIC. Sec 5.3: "any permission_denied in error_summary
  forces PARTIAL regardless of traversal_status." Destructive BLOCKED.
- Allowed Canonical Action: Additive-only. Entries in denied subtree stay
  in canonical state as MISSING (not promoted). (W-C Sec 2.2)
- Forbidden Action: MUST NOT remove entries from the denied subtree
  (INV-003: unknown territory). MUST NOT treat denied subtree as empty.
- Journal Result: No `resource-removed` for denied-subtree entries.
- Verdict: WARNING
- Evidence: W-B C-4 ACCEPTED_SEMANTIC; W-B Sec 5.3 ACCEPTED_SEMANTIC.
  Attack: The classification depends on the Collector reporting
  permission_denied in error_summary. W-B Sec 5.3 Note: "AList may
  silently swallow in virtualFiles scenarios" (D03 Q5). If AList swallows
  the permission error, error_summary is empty. If all other dimensions
  are positive, C-9 fires -> COMPLETE -> destructive reconcile ALLOWED.
  The denied-subtree entries are removed from canonical state. The design
  is robust IF the Collector reports the error, but AList may not. This is
  a Collector contract gap -- the Kernel cannot detect an unreported
  permission denial. WARNING.

### Scenario 11: Stale cache -> treated as partial/stale?

- Identity Result: Identity resolution proceeds for observed entries.
  Cached entries may be ghost (deleted at provider but still in cache).
- Completeness Result: Worker B C-6 (cached data, not bypassed, age exceeds
  threshold -> STALE) ACCEPTED_SEMANTIC. Sec 2.4: "STALE is treated as
  PARTIAL unless freshness evidence proves the cache is current."
  Destructive BLOCKED.
- Allowed Canonical Action: Additive-only. Ghost entries (in cache but
  deleted at provider) are NOT removed from canonical state. (W-C Sec 2.2)
- Forbidden Action: MUST NOT remove based on stale data (INV-003). MUST
  NOT treat cached data as fresh without positive evidence.
- Journal Result: No `resource-removed` under STALE.
- Verdict: WARNING
- Evidence: W-B C-6 ACCEPTED_SEMANTIC; W-B Sec 2.4 ACCEPTED_SEMANTIC.
  Attack: The classification trusts Collector-reported freshness_evidence.
  If the Collector reports cache_bypassed = true but the data is actually
  stale (bug, misconfiguration, or adversarial Collector), C-9 fires ->
  COMPLETE -> destructive reconcile ALLOWED. Ghost entries are removed.
  The design has no independent verification of freshness -- it trusts the
  Collector's self-report. This is consistent with principle 2 (Collector
  does not own canonical state) and the Gate 1A boundary (Kernel never
  reaches back to provider), but it means a buggy Collector can defeat the
  stale-cache gate. WARNING.

### Scenario 12: Silent truncation suspicion -> conservative handling?

- Identity Result: Identity resolution proceeds for observed (truncated)
  entries. Entries beyond the truncation point are not observed.
- Completeness Result: Worker B C-7 (entry count decline exceeds threshold
  -> SUSPICIOUS) ACCEPTED_SEMANTIC. Treated as PARTIAL. Destructive BLOCKED.
  (W-B Sec 2.5, Sec 5.8)
- Allowed Canonical Action: Additive-only. Entries beyond truncation stay
  in canonical state as MISSING. (W-C Sec 2.2)
- Forbidden Action: MUST NOT remove entries beyond the truncation point.
  MUST NOT treat SUSPICIOUS as COMPLETE.
- Journal Result: No `resource-removed` under SUSPICIOUS.
- Verdict: WARNING
- Evidence: W-B C-7 ACCEPTED_SEMANTIC; W-B Sec 5.8 ACCEPTED_SEMANTIC.
  Attack 1: Small truncation. If the truncation drops fewer entries than
  the threshold (e.g., truncation drops 5% but threshold is 10%), no
  decline signal fires. C-9 fires -> COMPLETE -> destructive ALLOWED.
  The threshold is CANDIDATE (W-B Sec 10 item 1). Small truncations evade
  detection. Attack 2: Truncation with padding. If the provider truncates
  but the remaining entries are padded to maintain count (adversarial
  provider or bug), no decline signal. C-9 fires -> COMPLETE. The design
  acknowledges this: "the Kernel cannot prove truncation occurred. The
  Kernel cannot prove it did not." (W-B Sec 5.8). WARNING.

### Scenario 13: Empty provider anomaly (provider returns empty) -> previous truth preserved?

- Identity Result: No entries to resolve. All canonical resources are
  unobserved.
- Completeness Result: Worker B C-8 (entry_count = 0, prior canonical
  non-empty -> SUSPICIOUS) ACCEPTED_SEMANTIC. Destructive BLOCKED.
  Sub-case 7a: prior canonical empty or root is new -> C-9 -> COMPLETE
  (empty root is legitimate). (W-B Sec 5.7)
- Allowed Canonical Action: If SUSPICIOUS: additive-only, all resources
  -> MISSING (not promoted). If COMPLETE (new root): no-op reconcile.
  (W-C Sec 2.2, Sec 2.4)
- Forbidden Action: MUST NOT remove all canonical resources on empty
  result (INV-004). MUST NOT treat empty-for-non-empty as mass deletion
  without confirmation.
- Journal Result: No `resource-removed` under SUSPICIOUS. No events for
  no-op COMPLETE.
- Verdict: WARNING
- Evidence: W-B C-8 ACCEPTED_SEMANTIC; W-B Sec 5.7 ACCEPTED_SEMANTIC.
  Attack: Deprecated root. W-B Sec 3.1 lists "root lifecycle state (active
  / deprecated / new)" as an input, and Sec 10 item 5 says "The root
  lifecycle state machine (new / active / deprecated) is Worker A scope."
  But Worker A does NOT define root lifecycle states or transitions
  (W-A Sec 1.1 ResourceRoot has no lifecycle field). If a deprecated root
  returns empty, should it be SUSPICIOUS or COMPLETE? The design does not
  address this. A deprecated root returning empty could legitimately be
  COMPLETE (all resources were removed before deprecation), but the
  current rules would classify it as SUSPICIOUS (prior canonical non-empty
  -> C-8). This is safe (conservative) but may prevent legitimate cleanup
  of deprecated roots. WARNING.

### Scenario 14: Entry count significant decline -> not sole evidence for rejection?

- Identity Result: Identity resolution proceeds normally for observed
  entries.
- Completeness Result: Worker B R-EV-3 (entry_count decline alone does NOT
  reject) ACCEPTED_SEMANTIC. C-7 fires -> SUSPICIOUS. Snapshot is accepted
  for additive-only reconcile. Decline blocks COMPLETE promotion, not
  acceptance. (W-B Sec 5.6)
- Allowed Canonical Action: Additive-only reconcile. ADD, UPDATE, RENAME,
  MOVE, UNCHANGED permitted. MISSING for unobserved (not promoted).
  (W-C Sec 2.2)
- Forbidden Action: MUST NOT reject the snapshot solely for entry count
  decline (R-EV-3). MUST NOT use decline alone to authorize destructive
  reconcile (R-EV-3, INV-004).
- Journal Result: `resource-added`, `resource-updated`, etc. for observed
  entries. No `resource-removed`.
- Verdict: PASS
- Evidence: W-B R-EV-3 ACCEPTED_SEMANTIC; W-B Sec 5.6 ACCEPTED_SEMANTIC.
  The scenario asks "not sole evidence for rejection?" R-EV-3 explicitly
  forbids using entry_count decline alone to reject. The snapshot is
  accepted (SUSPICIOUS -> additive-only). The decline blocks COMPLETE
  promotion, not acceptance. This is correct. Attack attempted: 100%
  decline (all entries gone). C-8 fires (entry_count = 0 for non-empty
  root -> SUSPICIOUS). Not rejected. Additive-only. All resources stay as
  MISSING. PASS.

---

## 3. Reconcile / failure scenarios (15-21)

### Scenario 15: Same Snapshot replay -> idempotent (NO CHANGE)?

- Identity Result: Same entries -> same identity resolution -> MATCHED to
  same canonical resources.
- Completeness Result: Same evidence -> same completeness classification
  (W-B R-LC-2: "deterministic given the same evidence and prior canonical
  state").
- Allowed Canonical Action: W-C Sec 2.4: "Replaying the same snapshot
  against the same canonical generation yields NO CHANGE." Every RESOLVED
  entry classifies as UNCHANGED. No ResourceEntry inserted, updated, or
  deleted. No JournalEvent appended. No generation bump.
- Forbidden Action: MUST NOT produce duplicate journal events. MUST NOT
  bump generation. MUST NOT re-apply transitions.
- Journal Result: No events. (W-C Sec 2.4, J1)
- Verdict: WARNING
- Evidence: W-C Sec 2.4 ACCEPTED_SEMANTIC. Attack: "Same snapshot" is
  defined as "same snapshot identity (same observed content/cursor, per
  Collector) AND same canonical generation observed at load" (W-C Sec 2.4).
  Snapshot identity/dedup is CANDIDATE (W-B R-LC-6: "dedup mechanism is
  an implementation detail; the dedup key is CANDIDATE pending Worker A
  identity work"). If the Collector resubmits the same content with a
  different snapshot_id, is it the "same snapshot"? The definition depends
  on the unresolved dedup key. If dedup fails, the replay is treated as a
  new snapshot -> full reconcile -> transitions re-applied -> generation
  bump. Not idempotent. WARNING.

### Scenario 16: Stale generation (concurrent commit) -> reject/retry, no overwrite?

- Identity Result: Identity resolution is computed against the loaded
  generation G. If another reconcile committed G+1, the transitions may
  be stale.
- Completeness Result: Completeness classification is computed against
  prior canonical at G. If G+1 changed the canonical state, the
  classification may be stale (e.g., prior canonical was non-empty at G
  but empty at G+1 -> different C-8/C-9 outcome).
- Allowed Canonical Action: W-C Sec 2.5: Store.commit() performs CAS on
  generation. Returns CAS_FAIL. Kernel retries (reload at new generation,
  recompute, re-attempt) or aborts (rollback, report stale). No overwrite.
  (W-C Sec 2.5 ACCEPTED_SEMANTIC)
- Forbidden Action: MUST NOT overwrite the committed G+1 state. MUST NOT
  commit without CAS success. MUST NOT silently merge stale transitions.
- Journal Result: No events on CAS_FAIL (uncommitted). On retry success:
  events for the recomputed transitions.
- Verdict: WARNING
- Evidence: W-C Sec 2.5 ACCEPTED_SEMANTIC; Gate 1A C1.3 (generation CAS).
  Attack: Infinite retry loop. If concurrent commits are constant (high-
  contention root), the retry loop never succeeds. W-C Sec 2.5: "The
  retry/abort policy is CANDIDATE (configuration)." If the policy is
  retry-only (no abort, no bound), the Kernel loops indefinitely. No
  ACCEPTED bound on retry count or backoff is specified. WARNING.

### Scenario 17: Concurrent snapshots for same root -> deterministic ordering?

- Identity Result: Each reconcile computes identity resolution independently
  against its loaded generation.
- Completeness Result: Each snapshot is independently classified (W-B R-LC-7:
  "each Snapshot is independently evaluated; ordering and conflict
  resolution are deferred" to Worker C).
- Allowed Canonical Action: W-C Sec 2.6: "serialized via generation CAS.
  Each begin_reconcile captures its expected_generation; only the first to
  commit succeeds. The second observes CAS_FAIL and follows Scenario 5
  (retry or abort)." (W-C Sec 2.6 ACCEPTED_SEMANTIC)
- Forbidden Action: MUST NOT allow both reconciles to commit. MUST NOT
  split-brain canonical state.
- Journal Result: Only the winning (first-to-commit) reconcile's events
  are appended. The losing reconcile appends nothing (CAS_FAIL).
- Verdict: WARNING
- Evidence: W-C Sec 2.6 ACCEPTED_SEMANTIC; Gate 1A C1.3.
  Attack: "Deterministic ordering." The scenario asks for deterministic
  ordering. CAS provides serializability (exactly one wins, no split-brain),
  but the WINNER is determined by timing (who commits first), not by
  snapshot content, identity, or any deterministic key. Two concurrent
  snapshots A and B: if A commits first, the final state reflects A then
  B-retried-against-A. If B commits first, the final state reflects B
  then A-retried-against-B. These may produce different final canonical
  states. The ordering is serializable but NOT deterministic (timing-
  dependent). WARNING.

### Scenario 18: Reconcile failure -> previous canonical truth preserved?

- Identity Result: Identity resolution may fail (UNRESOLVED/CONFLICT) for
  some entries. These produce no canonical mutation (W-C Sec 2.1).
- Completeness Result: If completeness classification itself fails (e.g.,
  internal error in classify function), the snapshot is not reconciled.
- Allowed Canonical Action: W-C Sec 2.3: "On any internal error during
  transition computation or before commit: Kernel calls Store.rollback().
  The in-flight reconcile is discarded; durable canonical state, journal,
  and generation are unchanged." (W-C Sec 2.3 ACCEPTED_SEMANTIC)
- Forbidden Action: MUST NOT leave partial canonical state. MUST NOT
  append partial journal events. MUST NOT bump generation. (INV-013)
- Journal Result: No events (rollback discards in-flight).
- Verdict: PASS
- Evidence: W-C Sec 2.3 ACCEPTED_SEMANTIC; Gate 1A C2.2 (atomic commit +
  rollback + zero durable mutation); INV-013.
  Attack attempted: rollback itself fails. Gate 1A C2.2 guarantees "zero
  durable mutation on rollback" -- the in-flight changes were never
  durably written, so rollback is a no-op on durable state. A rollback
  "failure" cannot corrupt durable state because there is nothing to
  undo. PASS (assuming Store implements C2.2 correctly, which is a Gate
  1C implementation concern).

### Scenario 19: Store commit failure -> previous canonical truth preserved?

- Identity Result: Identity resolution was computed but not committed.
- Completeness Result: Completeness classification was computed but not
  committed.
- Allowed Canonical Action: W-C Sec 2.7: "If Store.commit() fails for any
  reason other than CAS_FAIL: Kernel treats it as a reconcile failure
  (Scenario 3). Store.rollback() discards the in-flight transaction;
  previous canonical truth is preserved. No partial canonical state, no
  partial journal append, no partial generation bump is durable (Gate 1A
  C2.2 atomic commit: all-or-nothing)." (W-C Sec 2.7 ACCEPTED_SEMANTIC)
- Forbidden Action: MUST NOT leave partial canonical + journal + generation.
  MUST NOT commit canonical without journal. MUST NOT commit journal
  without canonical.
- Journal Result: No events (atomic commit failed, nothing durable).
- Verdict: PASS
- Evidence: W-C Sec 2.7 ACCEPTED_SEMANTIC; Gate 1A C2.2 (atomic commit,
  all-or-nothing); INV-013.
  Attack attempted: commit partially succeeds (canonical written, journal
  not). Gate 1A C2.2 guarantees atomic commit -- all-or-nothing. If the
  Store does not implement atomicity, this is a Store implementation bug
  (Gate 1C concern), not a Kernel design gap. PASS for the design.

### Scenario 20: Root delete/recreate -> handled correctly?

- Identity Result: Worker A Sec 1.1 ResourceRoot has root_id (REQUIRED,
  stable, Kernel-assigned) and generation_cursor. No lifecycle states
  defined. CanonicalResource.root_id is immutable (W-A Sec 3: "A resource
  never moves between roots").
- Completeness Result: Worker B Sec 3.1 lists "root lifecycle state (active
  / deprecated / new)" as an input but Sec 10 item 5 says "The root
  lifecycle state machine is Worker A scope." Worker A does not define it.
- Allowed Canonical Action: NOT DEFINED. No worker defines what happens
  to canonical resources when a root is deleted. No worker defines whether
  a deleted root's root_id can be reused. No worker defines root lifecycle
  transitions (new -> active -> deprecated -> deleted).
- Forbidden Action: Unknown -- without a defined lifecycle, forbidden
  actions are unspecified.
- Journal Result: Unknown -- no root-level journal events defined.
- Verdict: VIOLATION
- Evidence: W-A Sec 1.1 (no lifecycle field on ResourceRoot); W-B Sec 3.1
  (references root lifecycle state as input) + Sec 10 item 5 (defers to
  Worker A); W-C (no root lifecycle handling). Attack: A root is deleted
  (removed from the system). Its canonical resources have root_id = X
  (immutable). If the root is recreated with the same root_id X, new
  snapshots reconcile against the old canonical resources. If the
  recreated root has different content, old resources are MISSING ->
  eventually CONFIRMED_REMOVED. But if the root_id is NOT reused (new
  root_id Y), the old resources are orphaned -- their root_id X no longer
  exists, no snapshot covers them, they are never reconciled, never
  removed. This is a resource leak. The scenario "Root delete/recreate ->
  handled correctly?" has NO defined handling in any of the three worker
  outputs. Worker B references root lifecycle but defers it to Worker A.
  Worker A does not define it. This is a gap in the composed design.
  VIOLATION.

### Scenario 21: Overlapping roots (if allowed) -> ownership resolved?

- Identity Result: Worker A Sec 3: "Decision: A. Root partitions are
  disjoint." Option B (overlapping) REJECTED. R0 prevents cross-root
  identity matching.
- Completeness Result: Each root is independently classified. No cross-root
  completeness interaction.
- Allowed Canonical Action: Overlapping roots are NOT ALLOWED (W-A Sec 3
  REJECTED). If two roots accidentally overlap (misconfiguration), each
  root independently reconciles. The same physical resource appears in
  both roots with different resource_ids. (W-A Sec 3: "If a Collector
  observes a resource that legitimately spans two scopes, the deployment
  must model that as two roots with a Consumer-side projection")
- Forbidden Action: MUST NOT allow cross-root identity matching (R0).
  MUST NOT merge canonical state across roots.
- Journal Result: Each root produces independent journal events. Duplicate
  events for the same physical resource (different resource_ids).
- Verdict: WARNING
- Evidence: W-A Sec 3 ACCEPTED_SEMANTIC (Option A, disjoint). Attack: The
  design explicitly rejects overlapping roots, which is correct. But the
  scenario asks "if allowed -> ownership resolved?" Since overlapping is
  rejected, the scenario is N/A by design. However, there is no detection
  mechanism for accidental overlap (misconfiguration). Two roots with
  overlapping scope_descriptors produce duplicate canonical state without
  error. The Kernel does not validate scope_descriptor overlap (W-A Sec
  1.1: scope_descriptor is "Opaque to Kernel domain logic"). This is safe
  (no identity leak, no cross-root match) but produces silent duplication.
  WARNING.

---

## 4. Journal scenarios (22-24)

### Scenario 22: Journal disagrees with Canonical Inventory -> Canonical wins?

- Identity Result: Not directly relevant (identity is canonical state).
- Completeness Result: Not directly relevant.
- Allowed Canonical Action: W-C J6 (Sec 3.2): "When the Journal disagrees
  with Canonical Inventory, Canonical Inventory is the truth and the
  Journal is repaired (rebuilt/rewritten to match canonical), never the
  reverse." (W-C J6 ACCEPTED_SEMANTIC). W-C Sec 3.4: "The Journal is
  repaired to match canonical -- by rebuilding the affected event range
  from canonical state, or by appending a corrective event."
- Forbidden Action: MUST NOT edit Canonical Inventory to satisfy the
  Journal. MUST NOT treat Journal as authoritative over Canonical.
- Journal Result: Journal repaired (rebuild or corrective event).
- Verdict: WARNING
- Evidence: W-C J6 ACCEPTED_SEMANTIC; W-C Sec 3.4 ACCEPTED_SEMANTIC.
  Attack: J5 (W-C Sec 3.2) says "The Journal is append-only within
  committed transactions. Events are never edited or deleted." Sec 3.4
  offers two repair mechanisms: (a) "rebuilding the affected event range
  from canonical state" -- this REWRITES journal events, contradicting J5
  (append-only, never edited or deleted). (b) "appending a corrective
  event" -- this preserves J5. The document offers both but does not
  specify which. If mechanism (a) is chosen, J5 is violated. If mechanism
  (b) is chosen, J5 is preserved but the journal contains both the wrong
  event and the corrective event (consumers must interpret both). The
  repair mechanism is DEFERRED_TO_GATE1C (W-C Sec 3.4). The semantic
  contradiction between J5 (never edit/delete) and Sec 3.4 option (a)
  (rebuild = rewrite) is unresolved. WARNING.

### Scenario 23: Provider delta does NOT directly write Journal?

- Identity Result: Not relevant.
- Completeness Result: Not relevant.
- Allowed Canonical Action: W-C J7 (Sec 3.2): "Provider delta MUST NOT
  directly write the Journal. The only path to a journal event is:
  Collector -> Kernel (transition classification) -> Store (append_journal
  inside a committed ReconcileTx)." (W-C J7 ACCEPTED_SEMANTIC)
- Forbidden Action: MUST NOT write journal events from provider delta.
  MUST NOT bypass Kernel transition classification.
- Journal Result: Journal events only from committed Kernel transitions.
- Verdict: PASS
- Evidence: W-C J7 ACCEPTED_SEMANTIC; principle 8; principle 2.
  Attack attempted: Collector bug bypasses Kernel and writes directly to
  Store/Journal. The design prohibits this (J7), but enforcement is an
  implementation concern (Store must not accept writes outside
  ReconcileTx). The design is correct. PASS.

### Scenario 24: Uncommitted reconcile does NOT appear in Journal?

- Identity Result: Not relevant.
- Completeness Result: Not relevant.
- Allowed Canonical Action: W-C J1 (Sec 3.2): "The Journal records ONLY
  committed canonical state transitions. Uncommitted, rolled-back, or
  no-op reconciles append nothing." (W-C J1 ACCEPTED_SEMANTIC). W-C Sec
  3.1: "MISSING, REMOVAL_CANDIDATE, UNCHANGED, CONFLICT, and REJECTED
  produce NO journal event."
- Forbidden Action: MUST NOT append journal events for uncommitted
  transitions. MUST NOT append events for MISSING/REMOVAL_CANDIDATE/
  UNCHANGED/CONFLICT/REJECTED.
- Journal Result: No events for uncommitted reconcile.
- Verdict: PASS
- Evidence: W-C J1 ACCEPTED_SEMANTIC; W-C Sec 3.1 ACCEPTED_SEMANTIC;
  Gate 1A C2.2 (atomic commit).
  Attack attempted: commit succeeds for canonical but fails for journal.
  Gate 1A C2.2 guarantees atomic commit (all-or-nothing). Both canonical
  and journal are in the same ReconcileTx. If commit fails, neither is
  durable. PASS for the design.

---

## 5. Cross-worker consistency attacks

These attacks target the composition boundaries between A/B/C. Each worker
self-verified in isolation, but the three contracts must compose safely.

### CWA-1: R5 recency window vs Worker C removal grace period

- Attack: Worker A R5 (move recognition) uses a "configurable recency
  window" (CANDIDATE). Worker C Sec 1.3.1 C2 uses a "removal_grace_period"
  (CANDIDATE). These are different time windows with different purposes:
  R5's recency window limits how long after a resource goes MISSING it can
  be recognized as moved. Worker C's grace period limits how long a
  resource stays MISSING before promotion to REMOVAL_CANDIDATE.
- Conflict: If R5's recency window is SHORTER than Worker C's grace period,
  a resource that was moved may lose move-recognition (R5 window expired)
  before it is safe from removal (grace period not yet elapsed). The
  resource is then promoted to REMOVAL_CANDIDATE and eventually
  CONFIRMED_REMOVED -- identity is lost despite the resource still being
  alive at a new path.
- If R5's recency window is LONGER than the grace period, a resource could
  be CONFIRMED_REMOVED while still within R5's recognition window. A later
  snapshot recognizing the move would try to MATCH to a deleted resource.
  W-C Sec 1.3.4 says "CONFIRMED_REMOVED is NOT reversed. If a resource
  reappears with the same stable identity after removal, it is treated as
  a fresh ADD." So the move is not recognized -- identity is lost.
- Verdict: WARNING. Both windows are CANDIDATE and their relationship is
  unspecified. The design requires: R5 recency window > Worker C grace
  period + min_consecutive_complete_missing * snapshot_interval. This
  invariant is not stated.
- Evidence: W-A Sec 2.2 R5 (CANDIDATE); W-C Sec 1.3.1 C2 (CANDIDATE).

### CWA-2: RENAME/MOVE + UPDATE composite split

- Attack: Worker C Sec 1.2 rule 4: "RENAME/MOVE and UPDATE are mutually
  exclusive in one pass; if both path and attributes changed, the
  transition is UPDATE carrying the new path, OR a MOVE/RENAME followed
  by UPDATE -- the split is CANDIDATE and fixed when Worker A's continuity
  contract is concrete."
- Conflict: A file that is both moved AND modified in the same snapshot
  has ambiguous transition classification. If the split is "UPDATE
  carrying the new path," the journal has one `resource-updated` event
  with the new path -- the move is not explicitly recorded. If the split
  is "MOVE/RENAME followed by UPDATE," the journal has two events
  (`resource-moved` then `resource-updated`) -- but this requires two
  generations or a multi-event transaction. The journal format (W-C Sec
  3.1) defines 5 event types but does not address multi-event
  transactions for a single resource in one reconcile.
- Verdict: WARNING. The composite split is CANDIDATE and affects journal
  accuracy. Consumers reading the journal may not distinguish "moved +
  modified" from "modified in place" if the split is "UPDATE carrying
  new path."
- Evidence: W-C Sec 1.2 rule 4 (CANDIDATE); W-C Sec 3.1.

### CWA-3: Worker B C-9 requires cache_bypassed=true; absent freshness -> PARTIAL forever

- Attack: Worker B C-9 (the only path to COMPLETE) requires
  "freshness_evidence.cache_bypassed = true." If the Collector does not
  provide freshness_evidence at all (the field is OPTIONAL, W-B Sec 1.3
  lists it as "candidate"), C-9 cannot fire. C-10 fires (conservative
  default -> PARTIAL). The snapshot is always PARTIAL -> destructive
  reconcile is always BLOCKED.
- Conflict: A Collector that does not implement freshness evidence can
  never achieve COMPLETE, even for a full, fresh, error-free scan. The
  canonical inventory can never remove deleted resources -- they
  accumulate as MISSING forever. This is safe (principle 4: conservative
  gating) but operationally degenerate. The design does not provide an
  alternative path to COMPLETE for Collectors without freshness evidence.
- Verdict: WARNING. The requirement is safe but may render the system
  non-functional for Collectors that cannot report freshness. The
  freshness_evidence field is "candidate" in W-B Sec 1.3 but REQUIRED in
  C-9. This inconsistency should be resolved.
- Evidence: W-B Sec 1.3 (freshness_evidence candidate); W-B C-9
  (requires cache_bypassed = true).

### CWA-4: Worker A R6 (directory move) depends on Worker C, but Worker C does not address it

- Attack: Worker A R6 (directory move propagation) is CANDIDATE and
  "requires coordination with Safe Reconcile staging (Worker C) and
  completeness assessment (Worker B)" (W-A Sec 2.2 R6). Worker C's state
  machine (Sec 1.1-1.2) defines 10 transition types but none specifically
  address directory move propagation. Worker C's scenario matrix check
  (Sec 4) tested "(d) MOVE new parent -> resource-moved, identity
  preserved" but this is a single-resource move, not directory
  propagation.
- Conflict: R6 has no counterpart in Worker C. If Worker C's state
  machine processes one entry at a time, directory move propagation
  (batch recognition of all children) has no mechanism. The fallback is
  per-child R3/R5 recognition, which works for children with hash but
  not for children without hash (Scenario 3).
- Verdict: WARNING. R6 is CANDIDATE in Worker A and unaddressed in Worker
  C. The composed design does not guarantee directory move propagation.
- Evidence: W-A Sec 2.2 R6 (CANDIDATE); W-C Sec 1.1-1.2 (no directory
  move transition).

### CWA-5: Worker B SUSPICIOUS/STALE states vs Worker C reconcile input contract

- Attack: Worker B defines 5 acceptance states: COMPLETE, PARTIAL, FAILED,
  STALE, SUSPICIOUS. Worker C's input contract (Sec 0) says
  "CompletenessClass" is "COMPLETE / PARTIAL / REJECTED." Worker C Sec
  1.2 rule 1 says "If CompletenessClass = REJECTED -> REJECTED." Worker C
  Sec 1.2 rule 5 says "if CompletenessClass = PARTIAL -> MISSING (not
  promoted)" and "if CompletenessClass = COMPLETE -> MISSING, then apply
  lifecycle."
- Conflict: Worker C's input contract lists COMPLETE/PARTIAL/REJECTED but
  Worker B produces COMPLETE/PARTIAL/FAILED/STALE/SUSPICIOUS. Worker C
  does not explicitly handle STALE or SUSPICIOUS as input states. Worker B
  Sec 2.4 says "STALE is treated as PARTIAL" and Sec 2.5 says "SUSPICIOUS
  is treated as PARTIAL." But Worker C's state machine does not reference
  STALE or SUSPICIOUS -- it only branches on COMPLETE, PARTIAL, REJECTED.
  If Worker C receives STALE or SUSPICIOUS (not mapped to PARTIAL), the
  behavior is undefined. Worker B says they are "treated as PARTIAL" but
  Worker C's contract does not include this mapping.
- Verdict: WARNING. The mapping from Worker B's 5 states to Worker C's 3
  input states is stated by Worker B but not acknowledged by Worker C.
  The composed contract should explicitly map: STALE -> PARTIAL,
  SUSPICIOUS -> PARTIAL, FAILED -> REJECTED.
- Evidence: W-B Sec 2.4/2.5 (STALE/SUSPICIOUS treated as PARTILE); W-C
  Sec 0 (input: COMPLETE/PARTIAL/REJECTED); W-C Sec 1.2 (branches on
  COMPLETE/PARTIAL/REJECTED only).

### CWA-6: Worker B FAILED vs Worker C REJECTED naming

- Attack: Worker B defines a FAILED acceptance state (Sec 2.3). Worker C
  defines a REJECTED transition type (Sec 1.1 row 10) and references
  "CompletenessClass = REJECTED" (Sec 1.2 rule 1). Worker B's lifecycle
  (Sec 1.1) uses REJECTED as a Snapshot state, not an acceptance state.
  Worker B's acceptance states are COMPLETE/PARTIAL/FAILED/STALE/
  SUSPICIOUS. There is no "REJECTED" acceptance state in Worker B.
- Conflict: Worker C expects "CompletenessClass = REJECTED" but Worker B
  produces "FAILED" (which maps to the REJECTED lifecycle state, but the
  acceptance state is named FAILED). The naming mismatch could cause a
  composition error: Worker C checks for "REJECTED" but Worker B sends
  "FAILED." Worker B Sec 1.5 says "FAILED -> REJECTED" (the snapshot
  state becomes REJECTED when the acceptance state is FAILED), but the
  acceptance state itself is FAILED, not REJECTED.
- Verdict: WARNING. The naming is inconsistent: Worker B's acceptance
  state FAILED maps to Worker B's lifecycle state REJECTED, but Worker C
  expects CompletenessClass = REJECTED. The contract should use a single
  name.
- Evidence: W-B Sec 2.3 (acceptance state: FAILED); W-B Sec 1.1
  (lifecycle state: REJECTED); W-C Sec 1.2 rule 1 (input:
  CompletenessClass = REJECTED).

---

## 6. Worker Self-Check (Foreman cross-check, no subagent)

| Check | Narrow question | Key result | Worker verification | Decision |
|----------|-----------------|------------|---------------------|----------|
| Counterexample check #1 (identity continuity) | Do Worker A's identity rules R0-R11 produce identity loss for any of scenarios 1-8 under adversarial inputs (long rename gap, hash collision, universally absent attributes, provider ID scheme change)? | Scenario 1: R5 recency window CANDIDATE -> identity loss across long gaps. Scenario 4: R3 single-match hash collision -> false MATCHED (imposter not detected). Scenario 6: provider ID scheme change -> all CONFLICT -> system frozen. Scenario 8: R4 "both absent consistently" ambiguous for universally absent attributes. No principle violation found; all gaps are CANDIDATE dependencies or acknowledged limitations. | VERIFIED: I re-traced each attack through the exact rule text in W-A Sec 2.2. R5 recency window is explicitly "v1 candidate." R3 does not address single-match false-positive. R10 does not provide bulk resolution. R4 "both absent consistently" is genuinely ambiguous. | ADOPT (R0-R4, R7-R11 are sound); FLAG (R5 recency window, R3 hash collision risk, R10 bulk resolution, R4 absent-attribute ambiguity) as WARNING |
| Counterexample check #2 (completeness/destructive-delete) | Do Worker B's classification rules C-1 through C-10 permit destructive reconcile under any adversarial input for scenarios 9-14 (silent partial scan, unreported permission denial, trusted freshness, small truncation, deprecated root, legitimate mass deletion)? | Scenario 9: silent partial scan with positive freshness -> C-9 -> COMPLETE -> destructive allowed (irreducible ambiguity). Scenario 10: AList swallows permission error -> C-9 -> COMPLETE -> destructive allowed (Collector trust gap). Scenario 11: Collector lies about cache_bypassed -> C-9 -> COMPLETE -> destructive allowed. Scenario 12: small truncation below threshold -> C-9 -> COMPLETE -> destructive allowed. Scenario 13: deprecated root empty -> C-8 -> SUSPICIOUS (safe but may block cleanup). Scenario 14: legitimate mass deletion -> SUSPICIOUS -> delayed but safe. No rule violates INV-004 directly; all gaps are Collector trust or threshold dependencies. | VERIFIED: I re-traced each attack through C-1 through C-10 in order. C-9 requires ALL dimensions positive, which is the gate, but the dimensions trust Collector-reported evidence. C-7/C-8 thresholds are CANDIDATE. | ADOPT (C-1 through C-10 cascade is sound); FLAG (Collector trust dependency, threshold CANDIDATE, deprecated root gap) as WARNING |
| Counterexample check #3 (concurrency/replay) | Do Worker C's failure model scenarios 15-21 preserve previous canonical truth under adversarial concurrency (infinite retry, timing-dependent ordering, root delete/recreate, accidental overlap)? | Scenario 15: snapshot dedup CANDIDATE -> replay may not be idempotent. Scenario 16: infinite retry possible (no bound). Scenario 17: CAS provides serializability but not deterministic order (timing-dependent winner). Scenario 20: root delete/recreate UNDEFINED -> VIOLATION (no worker defines root lifecycle). Scenario 21: no overlap detection -> silent duplication. Scenarios 18, 19: rollback/atomicity preserve truth (PASS). | VERIFIED: I re-traced each attack through W-C Sec 2.1-2.8. Scenario 20 is a true gap -- W-A Sec 1.1 has no lifecycle field, W-B Sec 10 item 5 defers to Worker A, W-C does not address root deletion. | ADOPT (Scenarios 18, 19 PASS); FLAG (15, 16, 17, 21 as WARNING); ESCALATE (20 as VIOLATION -- root lifecycle undefined) |
| Scenario matrix check #1 (state machine scenarios) | Do Worker C's 10 transition types and precedence rules produce exactly one transition per (canonical resource, snapshot entry) pair for all combinations of IdentityResolution (RESOLVED/UNRESOLVED) x CompletenessClass (COMPLETE/PARTIAL/REJECTED) x canonical presence (present/absent)? | RESOLVED x COMPLETE x present -> ADD/UPDATE/RENAME/MOVE/UNCHANGED (rule 3/4). RESOLVED x COMPLETE x absent -> ADD (rule 3). RESOLVED x PARTIAL x present -> ADD/UPDATE/RENAME/MOVE/UNCHANGED (rule 3/4, Sec 2.2). RESOLVED x PARTIAL x absent -> ADD (rule 3). UNRESOLVED x any -> CONFLICT (rule 2). any x REJECTED -> REJECTED (rule 1). canonical-not-observed x COMPLETE -> MISSING -> lifecycle (rule 5). canonical-not-observed x PARTIAL -> MISSING (not promoted) (rule 5). Each pair resolves to exactly one transition. Partition verified. | VERIFIED: I enumerated all combinations and traced through W-C Sec 1.2 rules 1-6. Precedence is total and pairwise exclusive. | ADOPT (transition partition and precedence are sound) |
| Scenario matrix check #2 (root overlap/recreate) | Does the composed A/B/C design handle root delete/recreate (Scenario 20) and overlapping roots (Scenario 21) correctly? | Scenario 20: VIOLATION. No worker defines root lifecycle (new/active/deprecated/deleted). W-A Sec 1.1 ResourceRoot has no lifecycle field. W-B Sec 3.1 references "root lifecycle state" as input but defers definition to Worker A (Sec 10 item 5). W-C does not address root deletion. If a root is deleted, its canonical resources are orphaned (root_id immutable, no snapshot covers them). If recreated with same root_id, old resources reconcile against new content -> eventual removal. If recreated with new root_id, old resources leak. Scenario 21: WARNING. Overlapping roots rejected (W-A Sec 3 Option A). No detection for accidental overlap. Silent duplication. | VERIFIED: I searched all three documents for "lifecycle," "deprecated," "root delete," "root recreate." W-A has no lifecycle definition. W-B references it but defers. W-C does not mention it. | ESCALATE (Scenario 20 VIOLATION -- root lifecycle must be defined); FLAG (Scenario 21 WARNING -- overlap detection absent) |
| Contract consistency check (Journal vs Canonical Inventory) | Are Worker C's journal rules J1-J7 internally consistent and consistent with principles 1 and 8? Specifically, does J5 (append-only) contradict Sec 3.4 repair option (a) (rebuild)? Does J6 (canonical wins) conflict with J5? Are the 5 event types mapped correctly from transitions? | J5 vs Sec 3.4: "rebuild affected event range" contradicts J5 "events are never edited or deleted." "Append corrective event" preserves J5. Both options offered, neither specified. WARNING. J6 vs J5: J6 (canonical wins, repair journal) is compatible with J5 if repair = append corrective event. Compatible if mechanism (b) chosen. 5 event types map correctly: ADD->resource-added, UPDATE->resource-updated, RENAME->resource-renamed, MOVE->resource-moved, CONFIRMED_REMOVED->resource-removed. MISSING/REMOVAL_CANDIDATE/UNCHANGED/CONFLICT/REJECTED -> no event. Mapping verified. J7 (no provider delta -> journal) consistent with principle 8. J1 (only committed) consistent with Gate 1A C2.2. | VERIFIED: I cross-checked each J rule against Sec 3.1 event table and Sec 3.4 repair text. The J5 vs rebuild contradiction is real. | ADOPT (J1-J4, J6-J7); FLAG (J5 vs Sec 3.4 rebuild option as WARNING); FLAG (repair mechanism DEFERRED_TO_GATE1C) |

---

## 7. Summary

### 7.1 Verdict counts

| Verdict | Count | Scenarios |
|---------|-------|-----------|
| PASS | 7 | 2, 7, 14, 18, 19, 23, 24 |
| WARNING | 16 | 1, 3, 4, 5, 6, 8, 9, 10, 11, 12, 13, 15, 16, 17, 21, 22 |
| VIOLATION | 1 | 20 |

### 7.2 Cross-worker attack verdicts

| Attack | Verdict |
|-------|---------|
| CWA-1 (R5 recency vs grace period) | WARNING |
| CWA-2 (RENAME/MOVE + UPDATE split) | WARNING |
| CWA-3 (freshness evidence required for COMPLETE) | WARNING |
| CWA-4 (R6 directory move unaddressed in Worker C) | WARNING |
| CWA-5 (STALE/SUSPICIOUS not in Worker C input contract) | WARNING |
| CWA-6 (FAILED vs REJECTED naming mismatch) | WARNING |

### 7.3 VIOLATION detail

**Scenario 20 (Root delete/recreate):** No worker defines root lifecycle
states or transitions. Worker A Sec 1.1 ResourceRoot has no lifecycle field.
Worker B Sec 3.1 references "root lifecycle state (active / deprecated / new)"
as a classification input but Sec 10 item 5 defers the state machine to Worker
A. Worker A does not define it. Worker C does not address root deletion or
recreation. The composed A/B/C design has no defined behavior for:
- What happens to canonical resources when a root is deleted.
- Whether a deleted root's root_id can be reused.
- Root lifecycle transitions (new -> active -> deprecated -> deleted).
- Journal events for root lifecycle changes.

This is a gap in the composed design, not a defect in any single worker.
The scenario is mandatory (listed in the task). Recommended fix: Worker A
should define ResourceRoot lifecycle states and transitions in a follow-up,
or the Foreman should explicitly defer root lifecycle to a later gate with
a documented rationale.

### 7.4 Key WARNING themes

1. **CANDIDATE dependencies:** R5 recency window, R6 directory move, R2
   fallback thresholds, removal grace period, retry policy, snapshot dedup,
   entry count threshold, cache age threshold. All are safe in principle
   but unspecified in detail. Implementation could resolve into violations.

2. **Collector trust:** The completeness gate (Worker B C-9) trusts
   Collector-reported evidence (traversal_status, error_summary,
   freshness_evidence). A buggy or adversarial Collector can defeat the
   gate. This is consistent with the architecture (Kernel never reaches
   back to provider) but means the Kernel's safety depends on Collector
   correctness.

3. **Irreducible ambiguities:** Silent truncation (Scenario 9/12), hash
   collision (Scenario 4), provider ID scheme change (Scenario 6). These
   are fundamental limitations that no design can fully resolve. The
   design is conservative (blocks destructive action when ambiguous),
   which is correct per principle 4.

4. **Cross-worker contract mismatches:** STALE/SUSPICIOUS not in Worker C
   input contract (CWA-5), FAILED vs REJECTED naming (CWA-6), R6
   unaddressed in Worker C (CWA-4). These are composition gaps that
   self-verification by each worker missed.

### 7.5 Principles compliance

No scenario violates an accepted principle directly. The VIOLATION in
Scenario 20 is a gap (undefined behavior), not a principle contradiction.
All WARNINGs are CANDIDATE dependencies, Collector trust issues, or
irreducible ambiguities -- none contradict the 10 accepted principles.

| Principle | Status |
|-----------|--------|
| 1 (Canonical Inventory is unique truth) | Preserved -- all transitions go through Kernel |
| 2 (Collector does not own canonical state) | Preserved -- Collector provides evidence, Kernel decides |
| 3 (missing != deleted) | Preserved -- MISSING is state, CONFIRMED_REMOVED requires criteria+validation |
| 4 (incomplete input blocks destructive) | Preserved -- only COMPLETE permits destructive, CANDIDATE thresholds safe |
| 5 (path != identity) | Preserved -- R0-R11 use evidence, not path, for identity |
| 6 (provider capability optional) | Preserved -- provider_object_id OPTIONAL, R1 conditional |
| 7 (hash optional) | Preserved -- content_hash OPTIONAL, R3 conditional, R4 handles absence |
| 8 (native delta != Change Journal) | Preserved -- J2/J3/J4 separate the three concepts |
| 9 (Kernel Domain does not bind schema/ORM) | Preserved -- no schema/SQL in any worker output |
| 10 (Scanner checkpoint not Kernel) | Preserved -- not in any worker output |

---

## 8. DO NOT compliance

| Constraint | Status |
|------------|--------|
| Did not design the primary solution | Honored -- adversarial review only |
| Did not write product code | Honored -- design document only |
| Did not select Collector | Honored -- not referenced |
| Did not design algorithm | Honored -- attacked algorithms, did not design them |
| Did not design PostgreSQL schema | Honored -- not referenced |
| Did not design Scanner checkpoint/resume | Honored -- not referenced |

---

## 9. Evidence Index

| Citation | Source |
|----------|--------|
| W-A Sec 1.1-1.6 | Worker A Domain Model (ResourceRoot, Snapshot, SnapshotEntry, CanonicalResource, Generation, IdentityEvidence) |
| W-A Sec 2.1-2.3 | Worker A Identity Result States, Matching Rules R0-R11, Rule Precedence |
| W-A Sec 3 | Worker A Root Ownership Resolution (Option A, disjoint) |
| W-A Sec 4 | Worker A Scenario Coverage Table |
| W-B Sec 1.1-1.6 | Worker B Snapshot Lifecycle |
| W-B Sec 2.1-2.5 | Worker B Completeness Acceptance States |
| W-B Sec 3.1-3.5 | Worker B Evidence Evaluation Function (C-1 through C-10) |
| W-B Sec 4.1-4.4 | Worker B Destructive Reconcile Eligibility |
| W-B Sec 5.1-5.8 | Worker B Scenario Handling |
| W-B Sec 10 | Worker B Open Questions |
| W-C Sec 0 | Worker C Context and Inputs |
| W-C Sec 1.1-1.4 | Worker C Safe Reconcile State Machine |
| W-C Sec 2.1-2.8 | Worker C Failure Model |
| W-C Sec 3.1-3.4 | Worker C Change Journal Semantics |
| Principle 1-10 | Accepted principles, Gate 1A |
| INV-003, INV-004, INV-013 | Architecture Invariants |
| Gate 1A C1.2, C1.3, C2.2 | Gate 1A Store Contract boundaries |
| D02 Q7, Q12 | D02 research findings (AList/OpenList) |
| D03 Q3, Q5 | D03 research findings (rclone/fsspec) |
---

## 10. Rework Adversarial Attack (Gate 1B REWORK — Foreman cross-check)

> This section records the independent adversarial attack on the reworked
> A/B/C documents. Worker D was dispatched via Bridge but failed due to
> task file size; Foreman performed the cross-check directly. No subagent
> used.

### 10.1 Rework scenario verdicts

| Scenario | Attack | Verdict | Evidence |
|---|---|---|---|
| D1 | Two files with identical content_hash → does system wrongly inherit resource_id? | PASS | R3 requires continuity context (MISSING candidate in horizon). Copy at new path with no MISSING candidate → NEW_RESOURCE. (GATE1B-DOMAIN-MODEL.md Sec 2.2 R3, line 398-414) |
| D2 | Same path, old deleted, new same size → auto MATCH? | PASS | R4 step 3: same path + same size + mtime differs → UNRESOLVED (was MATCHED). Same path + same size alone MUST NOT confirm identity. (GATE1B-DOMAIN-MODEL.md Sec 2.2 R4, line 443) |
| D3 | PARTIAL Snapshot, old resource unobserved → Canonical MISSING? | PASS | PARTIAL/STALE/SUSPICIOUS: prior canonical resources keep previous presence/lifecycle. Only UNOBSERVED marker recorded. (GATE1B-SNAPSHOT-COMPLETENESS.md Sec 4.5, line 215-472) |
| D4 | WEAK_FAILURE_VISIBILITY Collector, success → destructive-safe COMPLETE? | PASS | C-9a: weak/unknown assurance → SUSPICIOUS. Destructive BLOCKED. Only STRONG_FAILURE_VISIBILITY can reach COMPLETE. (GATE1B-SNAPSHOT-COMPLETENESS.md C-9a, line 370) |
| D5 | Removal Validation: Kernel re-list/refresh/Provider API? | PASS | V2b: Kernel MUST NOT touch Provider. Validation evidence from Collector-submitted snapshots only. (GATE1B-SAFE-RECONCILE.md Sec 1.3.2 V2, line 282) |
| D6 | Journal append-only vs repair rewrite contradiction? | PASS | J6: canonical Journal history NEVER rewritten. Repair via corrective append or derived-projection rebuild only. (GATE1B-SAFE-RECONCILE.md J6, line 559) |
| D7 | Concurrent Snapshot A/B, timing changes → different Canonical State? | PASS | IO1-IO7: Kernel-owned admission sequence. Older input cannot overwrite newer. STALE_INPUT for out-of-order. CAS is enforcement, not ordering. (GATE1B-SAFE-RECONCILE.md Sec 2.6, line 441-477) |
| D8 | Move + Update: Consumer knows both occurred? | PASS | Ordered pair: path-change (RENAME/MOVE) then UPDATE, same generation. Both auditable as distinct events. (GATE1B-SAFE-RECONCILE.md Sec 1.2 rule 4, line 642) |

### 10.2 Cross-document consistency checks

| Check | Result | Evidence |
|---|---|---|
| Identity=UNRESOLVED → Reconcile≠MATCHED | PASS | Worker C Sec 1.2 rule 2: UNRESOLVED → CONFLICT |
| PARTIAL → no Canonical MISSING | PASS | Worker B Sec 4.5 + Worker C Sec 1.2 rule 5 |
| WEAK Collector → no CONFIRMED_REMOVED | PASS | Worker B C-9a → SUSPICIOUS → additive-only |
| Canonical state = Journal scope | PASS | Worker C Blocker F: Journal records only ResourcePresence transitions |
| Input ordering = Kernel-owned, CAS = enforcement | PASS | Worker C IO1-IO7, IO6 explicit |

### 10.3 Subagent wrapper check

| Document | Subagent Ledger present? | self-run subagent references? | Result |
|---|---|---|---|
| GATE1B-DOMAIN-MODEL.md | No (renamed to Worker Self-Check) | No | PASS |
| GATE1B-SNAPSHOT-COMPLETENESS.md | No (renamed to Worker Self-Check) | No | PASS |
| GATE1B-SAFE-RECONCILE.md | No (renamed to Worker Scenario Matrix) | No | PASS |
| GATE1B-ADVERSARIAL-CASES.md | No (renamed to Worker Self-Check) | No | PASS |

### 10.4 Gate 1D reference check

| Document | Gate 1D references? | POST_MVP/DEFERRED_UNSCHEDULED used? | Result |
|---|---|---|---|
| GATE1B-DOMAIN-MODEL.md | No | Yes (Identity v2, cross-root dedup, batch propagation) | PASS |
| GATE1B-SNAPSHOT-COMPLETENESS.md | No | Yes (corroboration mechanism) | PASS |
| GATE1B-SAFE-RECONCILE.md | No | Yes (explicit route statement) | PASS |
| GATE1B-ADVERSARIAL-CASES.md | No | N/A (this section) | PASS |

### 10.5 Rework summary

| Verdict | Count |
|---|---|
| PASS | 8 (D1-D8) + 5 (cross-doc) + 4 (subagent) + 4 (Gate 1D) = 21 |
| WARNING | 0 |
| VIOLATION | 0 |

All rework blockers (A1-A5, B, C, D, E, F, G, H, I) resolved.
All 12 Golden Cases satisfied.
No subagent references remain.
No Gate 1D references remain.
