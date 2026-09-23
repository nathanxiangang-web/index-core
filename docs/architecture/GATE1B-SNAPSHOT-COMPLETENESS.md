# Gate 1B Worker B -- Snapshot + Completeness Acceptance

> Phase: Gate 1B -- Core Semantics
> Worker: B (w02)
> Task-ID: gate1b-wb
> Baseline: `7aa798a`
> Branch: `architecture/gate1b-core-semantics`
> Date: 2026-09-23
> Status: REWORKED_GATE1B_BLOCKER_BCD (Architect Review Blocker B/C/D + subagent wrapper removal)
> Output target: `.work/gate1b/W-B-SNAPSHOT-COMPLETENESS.md`
>
> Scope rule: this document defines the Snapshot lifecycle and Completeness
> Acceptance semantics only. It does NOT design stable identity (Worker A),
> Safe Reconcile state machine (Worker C), Change Journal (Worker C),
> PostgreSQL schema/SQL/migration, product code, final Collector selection,
> or Scanner checkpoint/resume. It does NOT let the Collector set
> complete=true. It does NOT trigger provider re-scan for completeness
> verification.
>
> Rework note (2026-09-23): this revision applies Architect Review Blocker
> B (canonical state preservation under incomplete observation), Blocker C
> (provider-neutral freshness evidence), Blocker D (collector completeness
> assurance), and removes the subagent organization-layer wrapper (sections
> 6 and 7) per the Gate 1B rework brief. All reworked passages are tagged
> ACCEPTED_SEMANTIC. No Gate 1D references remain; the official route is
> Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC. Items not scheduled on that
> route are marked POST_MVP or DEFERRED_UNSCHEDULED.
>
> Evidence base: ARCHITECTURE-INVARIANTS (INV-001..INV-020), PROJECT-CONTEXT,
> PROJECT-STATE Gate 1A frozen boundaries, NEXT-ACTIONS Worker B section,
> Gate 1A accepted documents (GATE1A-RESPONSIBILITY-BOUNDARY.md,
> GATE1A-COLLECTOR-CONTRACT-SKELETON.md, GATE1A-BOUNDARY-ATTACK-REPORT.md),
> D02 report (AList/OpenList), D03 report (rclone/fsspec).

---

## 0. Layering context

```text
Provider (external)
      |
Collector / Scanner
      |  Snapshot + evidence (B3.1 fields)
Index Kernel          <-- this document defines the acceptance gate
      |  Canonical Inventory + Change Journal
Store (PostgreSQL)
      |
Consumers
```

The Collector produces a Snapshot plus Snapshot-level evidence and submits
it to the Kernel. The Kernel evaluates completeness acceptance using only
the submitted evidence and prior committed canonical state. The Kernel
never reaches back into the provider to verify completeness (Gate 1A frozen
boundary, GATE1A-RESPONSIBILITY-BOUNDARY.md section 8 item 1).

---

## 1. Snapshot lifecycle

A Snapshot is a first-class object (blueprint section 9,
GATE1A-COLLECTOR-CONTRACT-SKELETON.md B3.1). Its lifecycle is owned jointly:
the Collector owns creation and submission; the Kernel owns evaluation,
acceptance, reconcile, and retirement.

### 1.1 Lifecycle states

| State | Owner | Meaning | Tag |
|-------|-------|---------|-----|
| `DRAFT` | Collector | The Collector is building the Snapshot. The Kernel has not seen it. | ACCEPTED_SEMANTIC |
| `SUBMITTED` | Kernel | The Collector has submitted the Snapshot to the Kernel. Evaluation has not started. | ACCEPTED_SEMANTIC |
| `EVALUATED` | Kernel | The Kernel has classified the Snapshot into a Completeness Acceptance State (section 2). | ACCEPTED_SEMANTIC |
| `RECONCILED` | Kernel | The Kernel has applied reconcile using this Snapshot. Canonical generation has advanced. | ACCEPTED_SEMANTIC |
| `REJECTED` | Kernel | The Kernel rejected the Snapshot (FAILED state, or superseded by a newer Snapshot, or syntactic validation failure). No reconcile performed. | ACCEPTED_SEMANTIC |
| `RETIRED` | Kernel | The Snapshot is no longer the active input. Retained as audit/history; not eligible for new reconcile. | ACCEPTED_SEMANTIC |

Evidence: blueprint section 9 (Snapshot is first-class);
GATE1A-COLLECTOR-CONTRACT-SKELETON.md B3 (Collector provides evidence,
Kernel decides); INV-007 (Collector does not own canonical state);
INV-013 (previous canonical truth survives failed commit -> REJECTED must
not corrupt canonical state).

### 1.2 Lifecycle transitions

```text
DRAFT --submit--> SUBMITTED
SUBMITTED --evaluate--> EVALUATED
EVALUATED --reconcile--> RECONCILED   (for COMPLETE / PARTIAL / STALE / SUSPICIOUS)
EVALUATED --reject--> REJECTED        (for FAILED, or superseded, or invalid)
RECONCILED --retire--> RETIRED        (after generation advances)
REJECTED --retire--> RETIRED          (after audit window)
```

Rules:

- R-LC-1: `DRAFT -> SUBMITTED` is the only transition the Collector may
  perform. After submission, the Snapshot is Kernel-owned. -- ACCEPTED_SEMANTIC
  (INV-007: Collector does not own canonical state; GATE1A B4: Collector
  cannot mutate canonical inventory).
- R-LC-2: `SUBMITTED -> EVALUATED` is deterministic given the same evidence
  and prior canonical state. The classification function is defined in
  section 3. -- ACCEPTED_SEMANTIC (NEXT-ACTIONS: deterministic semantics).
- R-LC-3: `EVALUATED -> RECONCILED` requires a completeness acceptance state
  that permits reconcile. FAILED never reconciles. -- ACCEPTED_SEMANTIC
  (INV-004: incomplete input cannot authorize destructive reconcile;
  INV-013: failed reconcile leaves previous canonical truth intact).
- R-LC-4: `EVALUATED -> REJECTED` occurs when the state is FAILED, when the
  Snapshot is superseded by a newer Snapshot for the same root, or when
  syntactic validation fails. -- ACCEPTED_SEMANTIC (INV-013).
- R-LC-5: `RECONCILED -> RETIRED` and `REJECTED -> RETIRED` are terminal.
  A RETIRED Snapshot is read-only history. It must not be re-submitted or
  re-reconciled. -- ACCEPTED_SEMANTIC (audit integrity; INV-018: Git is
  durable project memory).
- R-LC-6: A Snapshot may be submitted at most once. Re-submission of the
  same Snapshot object is rejected as a duplicate. -- CANDIDATE (dedup
  mechanism is an implementation detail; the semantic rule is
  ACCEPTED_SEMANTIC, the dedup key is CANDIDATE pending Worker A identity
  work).
- R-LC-7: Concurrent Snapshot submission for the same root is not resolved
  here. That is Worker C concurrent-batch conflict resolution scope
  (NEXT-ACTIONS Worker C). This document only states that each Snapshot is
  independently evaluated; ordering and conflict resolution are deferred.
  -- DEFERRED (to Worker C).

### 1.3 How a Snapshot is created

The Collector assembles a Snapshot from a `ScanRequest`
(GATE1A-COLLECTOR-CONTRACT-SKELETON.md B1). The Snapshot contains:

- A list of `SnapshotEntry` records (B2, candidate schema).
- Snapshot-level evidence (B3.1): `source_ref`, `root_ref`, `started_at`,
  `finished_at`, `traversal_status`, `error_summary` (required);
  `skipped_scopes`, `freshness_evidence` (provider-neutral enum, section
  3.1.1), `collector_completeness_assurance` (section 3.1.2),
  `entry_count`, `byte_count` (candidate); `adapter_generation` (deferred
  post-MVP).

The Collector does NOT set a `complete=true` verdict
(GATE1A-COLLECTOR-CONTRACT-SKELETON.md B4: "Final completeness acceptance
| Kernel | REJECTED (from Collector)"). -- ACCEPTED_SEMANTIC.

### 1.4 How a Snapshot is submitted

Submission crosses the Collector -> Kernel contract boundary (INV-009).
The Kernel receives the Snapshot as an immutable input. The Kernel performs
syntactic validation (K7, GATE1A-RESPONSIBILITY-BOUNDARY.md section 3.1):
required fields present, types well-formed, `root_ref` identifies a known
root. Syntactic validation failure -> REJECTED. -- ACCEPTED_SEMANTIC.

### 1.5 How a Snapshot is accepted or rejected

Acceptance is a two-stage decision:

1. Syntactic validation (section 1.4). Failure -> REJECTED.
2. Completeness acceptance classification (section 3). The classification
   determines the acceptance state. FAILED -> REJECTED. All other states
   -> RECONCILED (with destructive reconcile eligibility per section 4).

A Snapshot is "accepted" when it reaches RECONCILED. A Snapshot is
"rejected" when it reaches REJECTED. -- ACCEPTED_SEMANTIC.

### 1.6 How a Snapshot is retired

A Snapshot is retired after:

- RECONCILED: the reconcile transaction has committed and canonical
  generation has advanced (GATE1A-STORE-QUERY-CONTRACT-SKELETON.md:
  `ReconcileTx.commit() -> CommitResult{new_generation}`). The Snapshot
  evidence is retained as audit history linked to the new generation.
- REJECTED: after an audit window. The rejected Snapshot and its rejection
  reason are retained for diagnosis.

Retirement is terminal. A retired Snapshot is never re-reconciled.
-- ACCEPTED_SEMANTIC (INV-018: durable project memory; audit integrity).

---

## 2. Completeness acceptance states

The Kernel classifies a submitted Snapshot into exactly one completeness
acceptance state. The states are mutually exclusive and collectively
exhaustive over the evidence space.

| State | Safe for destructive reconcile? | Reconcile mode | Tag |
|-------|----------------------------------|----------------|-----|
| `COMPLETE` | YES | destructive (add + update + remove) | ACCEPTED_SEMANTIC |
| `PARTIAL` | NO | additive-only (add + update, NO remove) | ACCEPTED_SEMANTIC |
| `FAILED` | NO | no reconcile | ACCEPTED_SEMANTIC |
| `STALE` | NO | additive-only (treat as PARTIAL unless freshness proves otherwise) | ACCEPTED_SEMANTIC |
| `SUSPICIOUS` | NO | additive-only (treat as PARTIAL) | ACCEPTED_SEMANTIC |

Evidence: INV-003 (missing != deleted), INV-004 (incomplete input cannot
authorize destructive reconcile), Gate 1A B3.2/B3.4 (traversal_status is
evidence not proof; entry_count/byte_count are weak sanity check).

### 2.1 COMPLETE

The Kernel has positive, convergent evidence that the Snapshot covers the
full root scope with no errors, no skips, fresh data, and no suspicious
truncation signals. -- ACCEPTED_SEMANTIC.

COMPLETE is the ONLY state that permits destructive reconcile (removal of
entries missing from the Snapshot relative to prior canonical state).
-- ACCEPTED_SEMANTIC (INV-004; principle 4).

### 2.2 PARTIAL

The Snapshot does not cover the full root scope. The uncovered portion is
unknown territory: entries missing from the Snapshot may exist in the
provider but were not observed. Removing them would violate INV-003
(missing != deleted). -- ACCEPTED_SEMANTIC.

PARTIAL permits additive-only reconcile: add new entries, update changed
entries, but do NOT remove entries absent from the Snapshot. Unobserved
prior canonical resources keep their previous canonical presence/lifecycle
state (section 4.5, BLOCKER B). -- ACCEPTED_SEMANTIC.

### 2.3 FAILED

The Snapshot is not usable. The traversal did not produce a coherent entry
list. No reconcile is performed. Prior canonical truth is preserved
(INV-013). -- ACCEPTED_SEMANTIC.

### 2.4 STALE

The Snapshot data was served from a cache and may not reflect current
provider state. Cached data may contain ghost entries (deleted at provider
but still in cache) or miss new entries. -- ACCEPTED_SEMANTIC.

STALE is treated as PARTIAL unless freshness evidence proves the data is
current (i.e., `freshness_evidence` in {`FRESH_DIRECT`, `FRESH_REFRESHED`,
`CACHED_FRESH`}). When freshness evidence is `STALE` or `UNKNOWN`, STALE
defaults to additive-only and unobserved prior canonical resources keep
their previous canonical presence/lifecycle state (section 4.5, BLOCKER B).
-- ACCEPTED_SEMANTIC (BLOCKER C: provider-neutral freshness; D02 section 4:
cache TTL default 30 min; cached list may contain ghost entries or miss new
files; D02 W-D Q3.3).

### 2.5 SUSPICIOUS

Weak evidence suggests possible truncation, but the Snapshot reports
`traversal_status = success`. The Kernel cannot prove truncation occurred,
but cannot prove it did not. Conservative gating blocks destructive
removal. -- ACCEPTED_SEMANTIC (D02 section 5: `len(content)==total` cannot
prove complete; D03 Q3: rclone cannot prove against silent backend
truncation; INV-004; BLOCKER D: weak/unknown failure-visibility Collectors
also land here via C-9a).

SUSPICIOUS is treated as PARTIAL: additive-only reconcile. Unobserved prior
canonical resources keep their previous canonical presence/lifecycle state
(section 4.5, BLOCKER B). -- ACCEPTED_SEMANTIC.

---

## 3. Evidence evaluation function

The Kernel evaluates completeness using a classification function:

```text
classify(snapshot_evidence, prior_canonical_state) -> AcceptanceState
```

### 3.1 Inputs (ACCEPTED_SEMANTIC)

The function reads ONLY:

1. Submitted Snapshot evidence:
   - `traversal_status` (success / partial / failed / interrupted)
   - `error_summary` (count, typed categories, first error)
   - `skipped_scopes` (list of skipped path prefixes with reasons)
   - `freshness_evidence` (provider-neutral normalized freshness, see 3.1.1)
   - `collector_completeness_assurance` (collector failure-visibility class, see 3.1.2)
   - `entry_count` (weak sanity check material)
   - `byte_count` (weak sanity check material)

2. Prior committed canonical state (Kernel-internal):
   - current generation number
   - prior canonical entry count (for the same root)
   - root lifecycle state (active / deprecated / new)

Evidence: GATE1A-COLLECTOR-CONTRACT-SKELETON.md B3.3 (items 1-4 from
Collector, item 5 from Kernel); GATE1A-RESPONSIBILITY-BOUNDARY.md section 8
item 1 ("derived from prior committed canonical state only; no independent
statistical model; never triggers re-traversal").

#### 3.1.1 Provider-neutral freshness evidence (ACCEPTED_SEMANTIC)

`freshness_evidence` is a normalized enum the Collector emits and the Kernel
consumes. The Kernel does NOT understand provider-specific switches (no
AList `cache_bypassed`, no rclone internal flags). The normalized values:

| Value | Meaning | Permits COMPLETE? |
|-------|---------|-------------------|
| `FRESH_DIRECT` | Data obtained directly from the provider with no cache in the path | YES |
| `FRESH_REFRESHED` | Data obtained after an explicit refresh/invalidation that established currency | YES |
| `CACHED_FRESH` | Data served from a cache but verified current against the provider within this scan | YES |
| `STALE` | Data served from a cache and not verified current; may contain ghosts or miss new entries | NO |
| `UNKNOWN` | Freshness not established by the Collector | NO |

The Collector adapter is responsible for translating its provider-specific
signals into exactly one of these values (Gate 1C adapter scope). A missing
`freshness_evidence` field is treated as `UNKNOWN`. -- ACCEPTED_SEMANTIC
(BLOCKER C: freshness must be provider-neutral; the prior
`cache_bypassed = true` binding over-coupled the Kernel to the AList/cache
model and excluded rclone/direct providers that have no cache concept).

#### 3.1.2 Collector completeness assurance (ACCEPTED_SEMANTIC)

`collector_completeness_assurance` classifies the Collector's failure
visibility so the Kernel can weigh a `traversal_status = success` correctly.
D02 proved that some AList paths swallow the underlying `storage.List`
error while the upper layer still returns success; not every Collector's
success is equally trustworthy.

| Value | Meaning | Permits COMPLETE on success alone? |
|-------|---------|-------------------------------------|
| `STRONG_FAILURE_VISIBILITY` | Errors are explicitly propagated; a reported success means no error was observed | YES (with all other dimensions positive) |
| `WEAK_FAILURE_VISIBILITY` | Errors may be swallowed by the adapter; a reported success does not guarantee no underlying error occurred | NO without independent corroboration |
| `UNKNOWN_FAILURE_VISIBILITY` | The adapter's error propagation behavior is not characterized | NO without independent corroboration |

For `WEAK_FAILURE_VISIBILITY` / `UNKNOWN_FAILURE_VISIBILITY`, a single scan
cannot reach destructive-safe COMPLETE. Options: (a) require additional
independent corroboration; (b) accept only NON_DESTRUCTIVE reconcile; or
(c) defer to a Gate 1C adapter that provides strengthened verification. A
missing field is treated as `UNKNOWN_FAILURE_VISIBILITY`. -- ACCEPTED_SEMANTIC
(BLOCKER D: known-silent-error Collectors must not easily obtain COMPLETE;
D02 section 4: `storage.List` error swallowed when `virtualFiles` non-empty).

### 3.2 MUST NOT (ACCEPTED_SEMANTIC)

The classification function MUST NOT:

- R-EV-1: Re-scan the provider to verify completeness.
  (Gate 1A frozen boundary: completeness heuristics never trigger
  re-traversal; GATE1A-RESPONSIBILITY-BOUNDARY.md section 8 item 1.)
- R-EV-2: Trust `traversal_status = success` as proof of provider-complete
  snapshot.
  (D02 section 5: HTTP 200 != complete; D03 Q3: rclone cannot prove
  against silent backend truncation; GATE1A B3.2.)
- R-EV-3: Use `entry_count` decline alone to reject the Snapshot.
  (`entry_count` / `byte_count` are weak sanity check material, never
  sufficient alone to authorize destructive reconcile; GATE1A B3.4;
  INV-004. Decline is a signal that blocks COMPLETE promotion, not a
  rejection of the Snapshot -- the Snapshot is still accepted for
  additive-only reconcile.)
- R-EV-4: Use an independent statistical model of expected entry counts.
  (GATE1A-RESPONSIBILITY-BOUNDARY.md section 8 item 1: "derived from prior
  committed canonical state only; no independent statistical model".)
- R-EV-5: Let the Collector's `traversal_status` be the final verdict.
  (GATE1A B4: "Final completeness acceptance | Kernel | REJECTED (from
  Collector)".)

### 3.3 Classification rules

The classification is evaluated as a conservative cascade. The first
matching rule determines the state. When multiple signals are present, the
most conservative (most restrictive) state wins.

| Rule | Condition | State | Tag | Evidence |
|------|-----------|-------|-----|----------|
| C-1 | `traversal_status = failed` | `FAILED` | ACCEPTED_SEMANTIC | GATE1A B3.2: failed = no usable entry list |
| C-2 | `traversal_status = interrupted` | `PARTIAL` | ACCEPTED_SEMANTIC | GATE1A B3.2: scan stopped before completion |
| C-3 | `traversal_status = partial` | `PARTIAL` | ACCEPTED_SEMANTIC | GATE1A B3.2: errors encountered, partial list |
| C-4 | `error_summary` is non-empty AND `traversal_status != failed` | `PARTIAL` | ACCEPTED_SEMANTIC | GATE1A B3.1: errors captured; non-empty errors mean incomplete coverage |
| C-5 | `skipped_scopes` is non-empty | `PARTIAL` | ACCEPTED_SEMANTIC | GATE1A B3.1: skipped subtrees are unknown territory (INV-003) |
| C-6 | `freshness_evidence` in {`STALE`} OR (`freshness_evidence` = `UNKNOWN` AND the Collector reported a cache was used) | `STALE` | ACCEPTED_SEMANTIC | BLOCKER C: provider-neutral freshness; D02 section 4: cache may contain ghosts or miss new entries; D02 W-D Q3.3 |
| C-7 | `traversal_status = success` AND prior canonical was non-empty AND `entry_count` decline exceeds Kernel-configured threshold | `SUSPICIOUS` | ACCEPTED_SEMANTIC | D02 section 5: weak sanity check; possible silent truncation; INV-004 conservative gating |
| C-8 | `traversal_status = success` AND prior canonical was non-empty AND `entry_count = 0` | `SUSPICIOUS` | ACCEPTED_SEMANTIC | Scenario 7: empty result for non-empty root is anomalous until proven otherwise |
| C-9 | `traversal_status = success` AND all of: `error_summary` empty, `skipped_scopes` empty, `freshness_evidence` in {`FRESH_DIRECT`, `FRESH_REFRESHED`, `CACHED_FRESH`}, `collector_completeness_assurance` = `STRONG_FAILURE_VISIBILITY`, no entry count decline, prior canonical consistent or root is new | `COMPLETE` | ACCEPTED_SEMANTIC | BLOCKER C + BLOCKER D: convergent positive evidence across traversal, errors, scope, freshness, and failure-visibility; the only path to COMPLETE |
| C-9a | `traversal_status = success` AND all of C-9 dimensions positive EXCEPT `collector_completeness_assurance` in {`WEAK_FAILURE_VISIBILITY`, `UNKNOWN_FAILURE_VISIBILITY`} | `SUSPICIOUS` | ACCEPTED_SEMANTIC | BLOCKER D: weak/unknown failure-visibility Collectors cannot reach destructive-safe COMPLETE on a single scan; downgraded to SUSPICIOUS pending corroboration or Gate 1C adapter strengthening |
| C-10 | None of the above matched (ambiguous / unclassified evidence) | `PARTIAL` | ACCEPTED_SEMANTIC | Conservative default: when in doubt, block destructive reconcile (INV-004) |

### 3.4 COMPLETE promotion is convergent (ACCEPTED_SEMANTIC)

COMPLETE requires convergent positive evidence across ALL dimensions:

1. `traversal_status = success` (necessary but not sufficient)
2. `error_summary` empty (no typed errors)
3. `skipped_scopes` empty (no skipped subtrees)
4. `freshness_evidence` in {`FRESH_DIRECT`, `FRESH_REFRESHED`, `CACHED_FRESH`} (provider-neutral freshness proof; BLOCKER C)
5. `collector_completeness_assurance` = `STRONG_FAILURE_VISIBILITY` (success is trustworthy; BLOCKER D)
6. No significant entry count decline relative to prior canonical
7. No silent truncation suspicion signal

Failure of any single dimension blocks COMPLETE promotion. The Snapshot
falls to the appropriate restrictive state (PARTIAL / STALE / SUSPICIOUS).
For dimension 5, a `WEAK_FAILURE_VISIBILITY` or `UNKNOWN_FAILURE_VISIBILITY`
Collector falls to SUSPICIOUS via C-9a and must obtain independent
corroboration, accept NON_DESTRUCTIVE reconcile, or wait for a Gate 1C
adapter that strengthens failure visibility. -- ACCEPTED_SEMANTIC.

Evidence: R-EV-2 (traversal_status not proof), C-5 (skips block), C-6
(stale blocks), C-7/C-8 (suspicion blocks), C-9a (weak failure visibility
blocks), INV-004 (conservative gating); BLOCKER C + BLOCKER D.

### 3.5 Conservative default (ACCEPTED_SEMANTIC)

Rule C-10 is the safety net. Any evidence pattern not explicitly matched
by C-1 through C-9 / C-9a defaults to PARTIAL (additive-only). This
implements the hard rule: "when in doubt, block destructive reconcile"
(INV-004, task MUST DEFINE: destructive reconcile gating must be
conservative).

---

## 4. Destructive reconcile eligibility

### 4.1 Eligibility table (ACCEPTED_SEMANTIC)

| Acceptance State | Destructive reconcile | Additive-only reconcile | No reconcile |
|------------------|-----------------------|-------------------------|--------------|
| `COMPLETE` | ALLOWED | allowed | -- |
| `PARTIAL` | BLOCKED | ALLOWED | -- |
| `FAILED` | BLOCKED | BLOCKED | ALLOWED (no reconcile) |
| `STALE` | BLOCKED | ALLOWED | -- |
| `SUSPICIOUS` | BLOCKED | ALLOWED | -- |

### 4.2 Hard rule (ACCEPTED_SEMANTIC)

```
PARTIAL  -> destructive removal BLOCKED
FAILED   -> destructive removal BLOCKED
STALE    -> destructive removal BLOCKED
SUSPICIOUS -> destructive removal BLOCKED
```

Only `COMPLETE` permits destructive reconcile (removal of entries missing
from the Snapshot relative to prior canonical state).

Evidence: INV-004 (incomplete input cannot authorize destructive reconcile);
INV-003 (missing != deleted); principle 4; task MUST DEFINE hard rule.

### 4.3 "Destructive reconcile" definition (ACCEPTED_SEMANTIC)

Destructive reconcile = reconcile that may remove entries from Canonical
Inventory. Specifically: entries present in prior canonical state but
absent from the Snapshot are marked as removal candidates and, subject to
the Safe Reconcile state machine (Worker C scope), confirmed removed.

Additive-only reconcile = add new entries + update changed entries, but
do NOT produce removal candidates for absent entries. Absent entries
remain in canonical state with their prior identity.

Evidence: blueprint section 8 (Missing -> Removal Candidate -> Validation
-> Confirmed Removed); INV-003 (missing != deleted). The full Safe
Reconcile state machine is Worker C scope; this document only defines
which acceptance states gate entry into the destructive path.

### 4.4 Conservative gating principle (ACCEPTED_SEMANTIC)

When the Kernel cannot positively establish COMPLETE, it must not permit
destructive reconcile. The burden of proof is on completeness, not on
incompleteness. Absence of negative signals is not sufficient; positive
convergent evidence is required (section 3.4).

Evidence: INV-004; task MUST DEFINE ("when in doubt, block").

### 4.5 Canonical state preservation under incomplete observation (ACCEPTED_SEMANTIC)

BLOCKER B fix. When the acceptance state is PARTIAL, STALE, or SUSPICIOUS,
the Snapshot did not authoritatively cover the full root scope. An
unobserved prior canonical resource is NOT evidence of deletion; it is
evidence of non-observation. The Kernel MUST preserve the prior canonical
truth for every resource not present in the Snapshot.

Concretely, for every prior canonical resource `R` absent from a PARTIAL /
STALE / SUSPICIOUS Snapshot, the reconcile MUST:

- Keep `R.canonical_presence` and `R.lifecycle_state` at their prior
  generation values (do NOT transition PRESENT -> MISSING).
- Record an observation marker such as `UNOBSERVED`,
  `UNKNOWN_COVERAGE`, or `NOT_OBSERVED_DUE_TO_INCOMPLETE_INPUT` in the
  reconcile result / snapshot observation log, so the audit trail reflects
  that the resource was not seen, without mutating canonical truth.
- NOT start or extend any removal timer on `R`.
- NOT increment any `consecutive_complete_missing` counter on `R`.
- NOT transition `R` into `REMOVAL_CANDIDATE`.

Only a destructive-eligible COMPLETE observation (section 3.4, all
dimensions positive, including `STRONG_FAILURE_VISIBILITY`) may produce
canonical MISSING evidence for a previously PRESENT resource, and only
then through the Safe Reconcile state machine (Worker C scope).

Rationale: incomplete input must not rewrite canonical absence (INV-003
missing != deleted; INV-004 incomplete input cannot authorize destructive
reconcile). The Canonical Inventory is the single source of truth; a
non-observation under incomplete coverage is an observation-log entry, not
a canonical-state mutation. -- ACCEPTED_SEMANTIC (BLOCKER B; INV-003;
INV-004; principle 1: Canonical Inventory is the single source of truth).

---

## 5. Scenario handling

All 8 mandatory scenarios. Each scenario cites the input evidence, the
classification rule(s) that fire, the resulting acceptance state, and the
reconcile eligibility.

### 5.1 Scenario 1: HTTP 200 but possible silent truncation

Input: `traversal_status = success`, but the provider may have silently
truncated the result (D02 section 4: `storage.List` error swallowed when
`virtualFiles` non-empty; D03 Q3: rclone cannot prove against silent
backend truncation).

Classification: `traversal_status = success` alone is necessary but not
sufficient for COMPLETE (R-EV-2, section 3.4). The Kernel requires
convergent positive evidence. If all other dimensions are positive
(fresh, no skips, no entry count decline, STRONG_FAILURE_VISIBILITY),
C-9 fires -> COMPLETE. If any dimension is negative, the appropriate
restrictive rule fires. If the Collector has WEAK_/UNKNOWN_FAILURE_VISIBILITY,
C-9a fires -> SUSPICIOUS even on a clean success (BLOCKER D). If the
Kernel has no way to distinguish "complete" from "no error surfaced"
(D03 W-B INFERENCE-X.1), and prior canonical was non-empty with no
decline signal, C-9 may fire only when ALL other dimensions are positive
AND assurance is STRONG -- this is the fundamental irreducible ambiguity,
and the Kernel accepts the risk only under the full convergent profile.

Reconcile eligibility: per the resulting state. The irreducible ambiguity
is acknowledged: the Kernel cannot be more certain than the evidence
allows. -- ACCEPTED_SEMANTIC.

### 5.2 Scenario 2: Explicit traversal error

Input: `traversal_status = partial` or `traversal_status = failed`,
`error_summary` non-empty.

Classification: C-1 (failed -> FAILED) or C-3 (partial -> PARTIAL).

Reconcile eligibility: FAILED -> no reconcile. PARTIAL -> additive-only.
Destructive reconcile BLOCKED. -- ACCEPTED_SEMANTIC.

### 5.3 Scenario 3: Permission denied subtree

Input: `error_summary` contains `permission_denied` category. The
subtree is inaccessible; its contents are unknown.

Classification: C-4 fires (non-empty error_summary) -> PARTIAL. Even if
`traversal_status = success` (adapter may report success for the
accessible portion), the permission-denied error is captured in
`error_summary` and forces PARTIAL.

Reconcile eligibility: additive-only. Entries in the permission-denied
subtree are unknown territory; they must not be removed from canonical
state (INV-003). -- ACCEPTED_SEMANTIC.

Note: the exact behavior of AList vs rclone on permission-denied subtrees
differs (D03 Q5: rclone propagates `ErrorPermissionDenied` as non-nil
error; AList may silently swallow in `virtualFiles` scenarios). The
classification is robust to both: any `permission_denied` in
`error_summary` forces PARTIAL regardless of `traversal_status`.
-- ACCEPTED_SEMANTIC.

### 5.4 Scenario 4: Partial scan

Input: `traversal_status = partial`.

Classification: C-3 fires -> PARTIAL.

Reconcile eligibility: additive-only. Destructive reconcile BLOCKED.
-- ACCEPTED_SEMANTIC.

### 5.5 Scenario 5: Stale cache

Input: `freshness_evidence` = `STALE` (or `UNKNOWN` with the Collector
reporting a cache was used).

Classification: C-6 fires -> STALE.

Reconcile eligibility: additive-only. Destructive reconcile BLOCKED.
Cached data may contain ghost entries (deleted at provider but still in
cache) or miss new entries (D02 section 4, D02 W-D Q3.3). Removing based
on stale data would violate INV-003. Unobserved prior canonical resources
keep their prior state (section 4.5, BLOCKER B). -- ACCEPTED_SEMANTIC.

Promotion: if `freshness_evidence` in {`FRESH_DIRECT`, `FRESH_REFRESHED`,
`CACHED_FRESH`} AND `collector_completeness_assurance` =
`STRONG_FAILURE_VISIBILITY` AND no other negative signals, C-9 may fire ->
COMPLETE. The normalized freshness value is the provider-neutral freshness
proof (BLOCKER C). -- ACCEPTED_SEMANTIC.

### 5.6 Scenario 6: Entry count significant decline

Input: `traversal_status = success`, `entry_count` much smaller than prior
canonical entry count. This is weak sanity check material (GATE1A B3.4;
D02 section 5).

Classification: C-7 fires -> SUSPICIOUS.

Reconcile eligibility: additive-only. Destructive reconcile BLOCKED.

Key distinction (R-EV-3): entry count decline alone does NOT reject the
Snapshot. The Snapshot is accepted for additive-only reconcile. The
decline blocks COMPLETE promotion (conservative gating), but does not
cause REJECTED. The decline could be legitimate mass deletion or silent
truncation; the Kernel cannot distinguish, so it blocks the destructive
path. -- ACCEPTED_SEMANTIC (INV-004; GATE1A B3.4: "never sufficient alone
to authorize destructive reconcile").

### 5.7 Scenario 7: Provider returns empty directory

Input: `traversal_status = success`, `entry_count = 0`.

Two sub-cases:

- 7a: Prior canonical state was empty OR root is new (no prior canonical),
  AND `collector_completeness_assurance` = `STRONG_FAILURE_VISIBILITY`,
  AND `freshness_evidence` permits COMPLETE.
  Classification: C-9 fires (all dimensions positive, no decline) ->
  COMPLETE. An empty root is legitimate. -- ACCEPTED_SEMANTIC.
  (If assurance is weak/unknown, C-9a fires -> SUSPICIOUS, BLOCKER D.)
- 7b: Prior canonical state was non-empty.
  Classification: C-8 fires -> SUSPICIOUS. An empty result for a
  previously non-empty root is anomalous: it could be legitimate mass
  deletion or silent truncation / scan failure. Conservative gating
  blocks destructive reconcile. -- ACCEPTED_SEMANTIC.

Evidence: D02 W-B sub-report (W-B-ALIST-OPENLIST-PROVIDER-API.md: "Empty
directories are not cached, so an empty result might be a cache miss or a
genuinely empty dir -- indistinguishable"). The Kernel treats
empty-for-non-empty as SUSPICIOUS pending further evidence.
-- ACCEPTED_SEMANTIC.

### 5.8 Scenario 8: Silent truncation suspicion

Input: `traversal_status = success`, `entry_count` much smaller than prior
canonical, but no explicit error.

This is the same evidence pattern as Scenario 6. Classification: C-7 fires
-> SUSPICIOUS. Reconcile eligibility: additive-only. Destructive reconcile
BLOCKED.

The Kernel cannot prove truncation occurred (D03 Q3: "not proof against
silent backend truncation"). The Kernel cannot prove it did not. Conservative
gating blocks destructive removal until the suspicion is resolved (e.g.,
by a subsequent fresh, complete snapshot). -- ACCEPTED_SEMANTIC (INV-004;
D02 section 5; D03 Q3).

---

## 6. Worker Counterexample Review

I reviewed my own COMPLETE promotion rules (section 3.3, C-9 / C-9a) to
verify no scenario wrongly permits destructive reconcile. This is a Worker
self-check, not a delegated subagent analysis.

### 6.1 Attack: cached snapshot with traversal_status=success

Can a cached snapshot (`freshness_evidence` = `STALE`) with
`traversal_status = success` and no entry count decline be promoted to
COMPLETE?

Result: C-6 fires before C-9 (cascade order). State = STALE. Destructive
BLOCKED. SAFE. -- PASS.

### 6.2 Attack: empty result for previously non-empty root

Can `entry_count = 0` with `traversal_status = success` for a non-empty
prior canonical be promoted to COMPLETE?

Result: C-8 fires before C-9. State = SUSPICIOUS. Destructive BLOCKED.
SAFE. -- PASS.

### 6.3 Attack: skipped scopes with traversal_status=success

Can `skipped_scopes` non-empty with `traversal_status = success` be
promoted to COMPLETE?

Result: C-5 fires before C-9. State = PARTIAL. Destructive BLOCKED. SAFE.
-- PASS.

### 6.4 Attack: entry_count decline alone causing REJECTED

Does entry_count decline alone cause the Snapshot to be REJECTED (not
reconciled at all)?

Result: No. C-7 produces SUSPICIOUS, which permits additive-only
reconcile. R-EV-3 explicitly forbids using entry_count decline alone to
reject. The decline blocks COMPLETE promotion, not acceptance. Consistent
with INV-004 and GATE1A B3.4. SAFE. -- PASS.

### 6.5 Attack: FAILED with matching entry_count

Can a `traversal_status = failed` snapshot with `entry_count` matching
prior canonical be promoted?

Result: C-1 fires first (failed -> FAILED). Entry_count is not consulted.
State = FAILED. No reconcile. SAFE. -- PASS.

### 6.6 Attack: error_summary with permission_denied but traversal_status=success

Can `traversal_status = success` with `error_summary` containing
`permission_denied` be promoted to COMPLETE?

Result: C-4 fires (non-empty error_summary). State = PARTIAL. Destructive
BLOCKED. SAFE. -- PASS.

### 6.7 Attack: unclassified evidence pattern

An evidence pattern not explicitly matched by C-1 through C-9 (e.g.,
`traversal_status = success`, `error_summary` empty, `skipped_scopes`
empty, but `freshness_evidence` entirely absent).

Result: C-10 fires (conservative default). State = PARTIAL. Destructive
BLOCKED. SAFE. -- PASS.

### 6.8 Attack: two concurrent COMPLETE snapshots

Two snapshots for the same root both classify as COMPLETE. Which one
authorizes destructive reconcile?

Result: Out of scope. Concurrent snapshot ordering and conflict resolution
is Worker C scope (NEXT-ACTIONS Worker C; R-LC-7 deferred). This document
guarantees each snapshot is independently classified; it does not define
which wins. NOT ATTACKED here (DO NOT: design concurrent conflict
resolution). -- DEFERRED (to Worker C).

### 6.9 Attack: weak failure-visibility Collector on clean success

Can a `WEAK_FAILURE_VISIBILITY` Collector with `traversal_status = success`,
fresh data, no skips, no entry count decline reach COMPLETE?

Result: C-9 requires `collector_completeness_assurance` =
`STRONG_FAILURE_VISIBILITY`. C-9a fires -> SUSPICIOUS. Destructive BLOCKED.
The Collector must obtain independent corroboration, accept NON_DESTRUCTIVE
reconcile, or wait for a Gate 1C adapter that strengthens failure visibility.
SAFE. -- PASS (BLOCKER D).

### 6.10 Attack: PARTIAL snapshot rewriting a prior canonical resource to MISSING

Under a PARTIAL Snapshot, can a previously PRESENT canonical resource absent
from the Snapshot be marked MISSING, start a removal timer, or enter
REMOVAL_CANDIDATE?

Result: Section 4.5 forbids all of these. The resource keeps its prior
canonical presence/lifecycle state; only an UNOBSERVED /
NOT_OBSERVED_DUE_TO_INCOMPLETE_INPUT marker is recorded in the observation
log. No canonical mutation, no timer, no counter increment, no
REMOVAL_CANDIDATE. SAFE. -- PASS (BLOCKER B).

### 6.11 Counterexample summary

| Attack | Target rule | Result |
|--------|-------------|--------|
| 6.1 | C-9 promotion with cached data | BLOCKED by C-6 (STALE) |
| 6.2 | C-9 promotion with empty result | BLOCKED by C-8 (SUSPICIOUS) |
| 6.3 | C-9 promotion with skipped scopes | BLOCKED by C-5 (PARTIAL) |
| 6.4 | REJECTED from entry_count decline | NOT REJECTED (SUSPICIOUS, additive-only) |
| 6.5 | COMPLETE from failed + matching count | BLOCKED by C-1 (FAILED) |
| 6.6 | COMPLETE with permission_denied | BLOCKED by C-4 (PARTIAL) |
| 6.7 | COMPLETE from unclassified evidence | BLOCKED by C-10 (PARTIAL default) |
| 6.8 | Concurrent COMPLETE conflict | DEFERRED to Worker C |
| 6.9 | COMPLETE from weak failure-visibility success | BLOCKED by C-9a (SUSPICIOUS) |
| 6.10 | PARTIAL rewriting canonical PRESENT to MISSING | BLOCKED by section 4.5 (BLOCKER B) |

No attack produced a wrongful COMPLETE promotion or a wrongful canonical
absence mutation. The conservative cascade (C-1 through C-10, with C-9a
gating weak failure visibility, first match wins, C-10 default PARTIAL) is
sound under all tested scenarios. -- ACCEPTED_SEMANTIC.

---

## 7. Worker Self-Check

Per the Gate 1B rework brief, the subagent organization-layer wrapper is
removed. The evidence verification below is a Worker self-check: I read the
cited sources directly and recorded the findings. No subagent was dispatched;
no Subagent Ledger is maintained.

### 7.1 Evidence verification (Worker self-check)

| Claim | Source I read | Finding |
|-------|---------------|---------|
| 1-4: completeness-evidence base | D02 / D03 original reports | SUPPORTED with exact file:section quotes |
| 5: `entry_count` / `byte_count` weak sanity check | GATE1A-COLLECTOR-CONTRACT-SKELETON.md B3.4 (Gate 1A accepted, authoritative); D02 section 5 / D02 W-D Q4.4 for the underlying `len(content)==total` fact | SUPPORTED via Gate 1A B3.4 authority; D02/D03 originals use `len(content)`/`total` terminology, Gate 1A consolidated to `entry_count`/`byte_count`. No fabrication. |
| 6: cache TTL / refresh behavior | D02 section 4, D02 W-D Q3.3 | SUPPORTED |
| 7: permission_denied typed error behavior | D03 main report Q5 (cited in GATE1A-COLLECTOR-CONTRACT-SKELETON.md B5.2) | SUPPORTED: rclone propagates `ErrorPermissionDenied`; AList may silently swallow in `virtualFiles` scenarios. Classification rule C-4 is robust to both (any `permission_denied` in `error_summary` forces PARTIAL). |
| 8: empty directory indistinguishable from cache miss | D02 W-B sub-report (W-B-ALIST-OPENLIST-PROVIDER-API.md) | SUPPORTED: "Empty directories are not cached, so an empty result might be a cache miss or a genuinely empty dir -- indistinguishable". Cited in Scenario 7. |

### 7.2 Worker adoption notes

- Claim 5: I cite Gate 1A B3.4 as the authoritative source for
  `entry_count`/`byte_count`, and D02 section 5 / D02 W-D Q4.4 for the
  underlying `len(content)==total` fact.
- Claim 7: classification rule C-4 is robust to both AList and rclone
  behavior.
- Claim 8: cited in Scenario 7.
- Counterexample review (section 6) is a Worker self-check, because it
  requires knowledge of my own design rules.

---

## 8. Consistency check against invariants

| Invariant | How this design respects it |
|-----------|------------------------------|
| INV-003 (missing != deleted) | PARTIAL/STALE/SUSPICIOUS permit additive-only reconcile; absent entries are not removed and prior canonical presence/lifecycle is preserved (section 4.5, BLOCKER B). Only COMPLETE permits removal, and only with convergent positive evidence. |
| INV-004 (incomplete input cannot authorize destructive reconcile) | Sections 3.2 R-EV-2/R-EV-3, section 4.2 hard rule, section 4.4 conservative gating, section 4.5 canonical freeze. COMPLETE requires convergent positive evidence (section 3.4). |
| INV-007 (Collector does not own canonical state) | R-LC-1: after submission, Snapshot is Kernel-owned. Collector cannot set complete=true (section 1.3, GATE1A B4). |
| INV-009 (snapshot/change input is explicit boundary) | Submission crosses a defined contract boundary (section 1.4). Kernel performs syntactic validation. |
| INV-010 (identity != path) | Not in scope (Worker A). This document does not use path as identity. SnapshotEntry path is observed path, not identity (GATE1A B2). |
| INV-011 (driver capability != universal) | Classification uses only evidence fields defined as optional/driver-dependent in GATE1A B2/B3. Does not require hash or provider_object_id. |
| INV-012 (native delta != change journal) | Not in scope (Worker C). This document treats all input as snapshot evidence (GATE1A B1: CollectorMode full_snapshot). |
| INV-013 (previous canonical truth survives failed commit) | R-LC-3/R-LC-4: FAILED -> REJECTED, no reconcile. Prior canonical truth preserved. |
| INV-020 (keep kernel small) | Completeness evaluation is read-only inspection of submitted evidence + prior canonical state. No re-scan, no statistical model, no provider traversal (R-EV-1, R-EV-4). |

---

## 9. DO NOT compliance

| Constraint | Status |
|------------|--------|
| Did not design identity matching (Worker A) | Honored -- not referenced |
| Did not design Safe Reconcile state machine (Worker C) | Honored -- section 4.3 references it as Worker C scope; only defines acceptance gate |
| Did not design Change Journal (Worker C) | Honored -- not referenced |
| Did not design PostgreSQL schema/SQL/migration | Honored -- not referenced |
| Did not write product code | Honored -- design document only |
| Did not select final Collector | Honored -- not referenced |
| Did not design Scanner checkpoint/resume | Honored -- not referenced |
| Did not let Collector set complete=true | Honored -- section 1.3, R-EV-5, GATE1A B4 cited |
| Did not trigger provider re-scan for completeness verification | Honored -- R-EV-1, Gate 1A frozen boundary cited |
| Did not use subagent organization-layer wrapper | Honored -- sections 6 and 7 reworked to Worker Self-Check; no Subagent Ledger |
| Did not reference Gate 1D | Honored -- official route Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC; off-route items marked POST_MVP / DEFERRED_UNSCHEDULED |
| Did not bind freshness to a provider-specific switch | Honored -- section 3.1.1 provider-neutral freshness enum (BLOCKER C) |
| Did not treat all Collectors' success as equally trustworthy | Honored -- section 3.1.2 collector completeness assurance (BLOCKER D) |
| Did not let incomplete observation rewrite canonical absence | Honored -- section 4.5 canonical state preservation (BLOCKER B) |

---

## 10. Open questions for Foreman cross-check

0. **Official route**: Gate 1A -> Gate 1B -> Gate 1C -> Gate 2 PoC. No
   Gate 1D is on the route. Items not scheduled on that route are marked
   POST_MVP or DEFERRED_UNSCHEDULED. -- ACCEPTED_SEMANTIC.
1. **Entry count decline threshold**: C-7 references a "Kernel-configured
   threshold" for significant entry count decline. The threshold value is
   an implementation/configuration concern (Gate 1C or runtime config),
   not a Gate 1B semantic. The semantic rule is: decline exists -> blocks
   COMPLETE. The threshold is CANDIDATE.
2. **Freshness enum translation**: the Collector adapter must translate
   provider-specific signals into the normalized `freshness_evidence` enum
   (section 3.1.1). The exact translation rules are Gate 1C adapter scope.
   The Gate 1B semantic is: only {`FRESH_DIRECT`, `FRESH_REFRESHED`,
   `CACHED_FRESH`} permit COMPLETE. -- ACCEPTED_SEMANTIC (BLOCKER C).
3. **Collector assurance characterization**: classifying a Collector as
   `STRONG_/WEAK_/UNKNOWN_FAILURE_VISIBILITY` is Gate 1C adapter scope
   (requires characterizing the adapter's error propagation). The Gate 1B
   semantic is: only `STRONG_FAILURE_VISIBILITY` permits COMPLETE on a
   single scan. -- ACCEPTED_SEMANTIC (BLOCKER D).
4. **STALE promotion**: section 2.4 states STALE defaults to additive-only
   "unless freshness evidence proves the data is current". The exact
   promotion condition is CANDIDATE. The conservative default
   (STALE -> additive-only) is ACCEPTED_SEMANTIC.
5. **Concurrent snapshot conflict**: R-LC-7 deferred to Worker C. This
   document does not define which of two concurrent COMPLETE snapshots
   wins. Foreman should confirm Worker C covers this.
6. **Root lifecycle interaction**: C-9 references "root is new" as a
   condition for COMPLETE on empty result. The root lifecycle state
   machine (new / active / deprecated) is Worker A scope (ResourceRoot,
   NEXT-ACTIONS Worker A). This document assumes the Kernel can query
   prior canonical state for the root; the root state model itself is
   Worker A.

---

## 11. Deferred items

| ID | Item | Deferred to | Reason |
|----|------|-------------|--------|
| D-DEFER-7 | Entry count decline threshold value | Gate 1C / runtime config | Semantic rule is ACCEPTED; threshold is implementation |
| D-DEFER-8 | Freshness enum translation rules (provider-specific -> normalized) | Gate 1C adapter | Semantic enum is ACCEPTED (BLOCKER C); translation is adapter implementation |
| D-DEFER-9 | Concurrent snapshot conflict resolution | Worker C (Gate 1B) | NEXT-ACTIONS Worker C owns concurrent-batch conflict |
| D-DEFER-10 | Root lifecycle state machine | Worker A (Gate 1B) | NEXT-ACTIONS Worker A owns ResourceRoot |
| D-DEFER-11 | Snapshot dedup key | Worker A (Gate 1B) | R-LC-6 semantic accepted; dedup key depends on identity work |
| D-DEFER-12 | Stale promotion exact condition | Gate 1C / runtime config | Conservative default accepted; promotion condition is CANDIDATE |
| D-DEFER-13 | Collector assurance characterization per adapter | Gate 1C adapter | Semantic assurance levels are ACCEPTED (BLOCKER D); classifying an adapter's failure visibility is adapter implementation |
| D-DEFER-14 | Weak/unknown assurance Collector corroboration mechanism | POST_MVP | Gate 1B defines the gate; the corroboration mechanism is post-MVP |

---

## 12. Summary

This document defines:

1. **Snapshot lifecycle** (section 1): DRAFT -> SUBMITTED -> EVALUATED ->
   RECONCILED/REJECTED -> RETIRED. Collector owns creation+submission;
   Kernel owns evaluation+acceptance+reconcile+retirement.
2. **Completeness acceptance states** (section 2): COMPLETE, PARTIAL,
   FAILED, STALE, SUSPICIOUS. Mutually exclusive, collectively exhaustive.
3. **Evidence evaluation function** (section 3): classify(submitted
   evidence, prior canonical state) -> state. Conservative cascade C-1
   through C-10 with C-9a gating weak failure visibility. MUST NOT re-scan,
   MUST NOT trust traversal_status=success as proof, MUST NOT use
   entry_count decline alone to reject. Provider-neutral freshness
   evidence (section 3.1.1, BLOCKER C) and collector completeness
   assurance (section 3.1.2, BLOCKER D) are required inputs.
4. **Destructive reconcile eligibility** (section 4): only COMPLETE
   permits destructive reconcile. PARTIAL/FAILED/STALE/SUSPICIOUS ->
   BLOCKED. Canonical state preservation under incomplete observation
   (section 4.5, BLOCKER B) forbids rewriting canonical absence from
   incomplete input. When in doubt, block.
5. **All 8 scenarios handled** (section 5).
6. **Worker Counterexample Review** (section 6): 9 attacks on COMPLETE
   promotion / canonical mutation, all blocked. 1 concurrent conflict
   deferred to Worker C.
7. **Worker Self-Check** (section 7): evidence claims verified by the
   Worker against cited sources; subagent wrapper removed.
8. **No DO NOT violations** (section 9).
9. **Official route** (section 10 item 0): Gate 1A -> Gate 1B -> Gate 1C
   -> Gate 2 PoC. No Gate 1D; off-route items are POST_MVP or
   DEFERRED_UNSCHEDULED.
