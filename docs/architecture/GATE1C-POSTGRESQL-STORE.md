# Gate 1C — PostgreSQL Store Realization

> Implementation-facing contract for the PostgreSQL realization of the already
> accepted Gate 1A **Store Interface**.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **FROZEN** — Architect-approved in the PR #43 Final Freeze Decision (final verification against head `09faf41`). Part of the frozen Gate 1C A/B/C contract.
> Baseline: remote `main` = `6a131f17657807d9aee2921be1f286ceaff784e4`.
> Accepted inputs: `GATE1B-DOMAIN-MODEL.md`, `GATE1B-SNAPSHOT-COMPLETENESS.md`,
> `GATE1B-SAFE-RECONCILE.md`, `GATE1B-ADVERSARIAL-CASES.md`,
> `GATE1A-STORE-QUERY-CONTRACT-SKELETON.md`, `GATE1A-RESPONSIBILITY-BOUNDARY.md`,
> `ARCHITECTURE-INVARIANTS.md` (INV-001..024).

---

## 0. Scope, non-goals, and tag system

### 0.1 Scope

This document freezes the **persistence representation** of every Gate 1B Domain
state behind the Gate 1A Store Interface, plus the correctness indexes and
constraints the Store implementation depends on. It is the "A" deliverable of
Issue #40.

This document does **not**:

- bind Kernel Domain to PostgreSQL (INV / principle 9). The Kernel depends only
  on the Store Interface expressed in Domain vocabulary; the tables below are
  Store-internal and invisible to Kernel and Consumer.
- define the transaction boundary in full (deliverable **B**,
  `GATE1C-TRANSACTION-BOUNDARY.md`). Section 7 states only the schema-level
  requirements the transaction boundary relies on.
- define the read API (deliverable **C**, `GATE1C-QUERY-CONTRACT.md`).
- define the Collector adapter mapping (deliverable **E**).
- write product code, migrations, or ORM models (DO NOT).

### 0.2 Tag system

| Tag | Meaning |
|-----|---------|
| `DERIVED` | Forced by an accepted Gate 1A/1B semantic; the Store realization has no design freedom. Violating it violates an upstream contract. |
| `PROPOSED` | A concrete persistence mechanism proposed by the Worker; awaits ChatGPT Architect acceptance. |
| `CANDIDATE` | Multiple reasonable options exist; the Worker records a recommendation but does not freeze it. |
| `DEFERRED` | Explicitly out of Gate 1C scope. |
| `REJECTED` | Explicitly refused, with reason. |

Evidence column vocabulary (per project evidence standard):

| Evidence tag | Meaning |
|--------------|---------|
| `FACT` | Directly cited from an accepted contract, invariant, or D02/D03 source evidence. |
| `INFERENCE` | Worker derivation from accepted semantics. |
| `UNKNOWN` | Not determinable at this gate; recorded as a gap. |

### 0.3 Layer contract (restated, non-negotiable)

```text
Collector Adapter
      |
      v
normalized Snapshot + Evidence
      |
      v
IndexCore Kernel        -- depends on Store Interface (Domain vocabulary only)
      |
      v
Store Interface
      |
      v
PostgreSQL              -- this document
```

`FACT` — Kernel Domain does not bind schema/ORM (GATE1A-STORE-QUERY-CONTRACT-SKELETON
C1.1, C2.3; principle 9). The schema below is Store-internal.

---

## 1. Mapping principles

| # | Principle | Tag | Evidence |
|---|-----------|-----|----------|
| M1 | Every Gate 1B Domain field/state maps to exactly one persistence representation; no Gate 1B state is unrepresentable. | `DERIVED` | Issue #40 consistency check #1; `FACT` (Gate 1B Domain Model Sec 1) |
| M2 | No Collector-specific field enters the Kernel Domain or its persistence. `extra_evidence` / `metadata` are opaque audit payloads, never domain inputs. | `DERIVED` | INV-011, INV-006; Issue #40 consistency check #10; `FACT` |
| M3 | `root_id` and `resource_id` are immutable for their lifetime; `root_id` is never reused. | `DERIVED` | GATE1B-DOMAIN-MODEL 1.1, 1.4, Sec 10.2; Issue #40 consistency checks #7, #8; `FACT` |
| M4 | The Canonical Change Journal is append-only; events are never updated or deleted. | `DERIVED` | GATE1B-SAFE-RECONCILE J5/J6, Sec 3.4; `FACT` |
| M5 | Canonical state, journal events, generation, and admission/ordering state commit in one atomic unit. | `DERIVED` | GATE1A C2.2; GATE1B-SAFE-RECONCILE Sec 2.8; `FACT` |
| M6 | RemovalEvidenceState is Kernel-internal and MUST NOT be exposed as consumer-visible resource status. | `DERIVED` | GATE1B-SAFE-RECONCILE Sec 1.3 (Blocker F); `FACT` |
| M7 | Logical tombstones (`REMOVED`, `DELETED` root) are retained, not physically erased. | `DERIVED` | GATE1B-DOMAIN-MODEL Sec 10.3; GATE1B-SAFE-RECONCILE Sec 1.3.4; `FACT` |
| M8 | Provider-native delta is never persisted as canonical state or as a journal event. | `DERIVED` | principle 8, J2/J3/J7; `FACT` |
| M9 | `canonical_path` is an observed/derived coordinate, NOT canonical identity. Multiple live resources MAY share a path (path reuse / imposter, R8); persistence MUST represent the overlap rather than force false uniqueness. | `DERIVED` | GATE1B R8; PR #43 review #5; `FACT` |

---

## 2. Logical table inventory

`PROPOSED` — the table set below is the Worker's proposed Store-internal model.
Table names are `index_*` to keep the kernel namespace distinct from any consumer
schema (the Kernel does not depend on these names).

| # | Table | Persists (Gate 1B concept) | Tag |
|---|-------|----------------------------|-----|
| T1 | `index_root` | ResourceRoot + lifecycle + per-root generation cursor + admission high-water mark | `DERIVED` (fields) / `PROPOSED` (shape) |
| T2 | `index_generation` | Generation record per root | `DERIVED` |
| T3 | `index_canonical_resource` | CanonicalResource + tombstone + RemovalEvidenceState + current attributes | `DERIVED` |
| T4 | `index_identity_evidence_observation` (+ `_current`) | IdentityEvidence append-only history (authoritative) + current aggregate (projection/cache) | `DERIVED` |
| T5 | `index_snapshot` | Snapshot metadata/evidence + Kernel acceptance classification | `DERIVED` |
| T6 | `index_snapshot_entry` | SnapshotEntry records (audit/replay) | `CANDIDATE` |
| T7 | `index_admission` | Per-root serialized admission sequence (IO1) | `DERIVED` (semantic) / `PROPOSED` (shape) |
| T8 | `index_applied_snapshot` | Applied snapshot identity / idempotency (IO3) | `DERIVED` (semantic) / `PROPOSED` (shape) |
| T9 | `index_journal_event` | Canonical Change Journal (append-only) | `DERIVED` |
| T10 | `index_root_config` | Per-root policy (grace period, horizons, thresholds) | `CANDIDATE` |
| T11 | `index_reconcile_result` | Reconcile result + conflict records (only where required) | `DERIVED` (conflicts must be recorded) / `PROPOSED` (shape) |

---

## 3. Table definitions

> Column types are indicative PostgreSQL types. `snake_case` column names follow
> the project naming convention. `SCREAMING_SNAKE_CASE` enum values are stored as
> constrained text (or native enums); the choice is `CANDIDATE`.

### 3.1 T1 `index_root` — ResourceRoot + lifecycle

| Column | Type | Null | Constraint / notes |
|--------|------|------|--------------------|
| `root_id` | `uuid` | NO | PRIMARY KEY. Kernel-assigned. Immutable, never reused (M3). NOT a path. |
| `scope_descriptor` | `jsonb` | NO | Opaque to Kernel domain logic; interpreted by Collector selection (Gate 1C E). |
| `owning_collector_ref` | `text` | YES | Optional until Collector selection. |
| `lifecycle_state` | `text` | NO | CHECK IN (`NEW`,`ACTIVE`,`DEPRECATED`,`DELETED`). |
| `current_generation` | `bigint` | NO | DEFAULT 0. Per-root monotonic. CAS token (see doc B). |
| `latest_admission_seq` | `bigint` | NO | DEFAULT 0. Per-root admission high-water mark (IO1). |
| `created_at` | `timestamptz` | NO | |
| `updated_at` | `timestamptz` | NO | |

Mapping:

| Gate 1B field/state | Column | Tag | Evidence |
|---------------------|--------|-----|----------|
| `root_id` | `root_id` | `DERIVED` | GATE1B-DOMAIN-MODEL 1.1; `FACT` |
| `scope_descriptor` | `scope_descriptor` | `DERIVED` | 1.1; `FACT` |
| `owning_collector_ref` | `owning_collector_ref` | `DERIVED` | 1.1; `FACT` |
| `generation_cursor` | `current_generation` | `DERIVED` | 1.1, 1.5; `FACT` |
| lifecycle `NEW/ACTIVE/DEPRECATED/DELETED` | `lifecycle_state` | `DERIVED` | Sec 10.1; `FACT` |

Constraints:

- `C-R1` (PROPOSED): `root_id` PK; no DELETE permitted for roots (tombstone via
  `lifecycle_state='DELETED'`), enforcing no reuse (M3).
- `C-R2` (PROPOSED): a state-transition guard rejects `DELETED -> ACTIVE` and
  `DELETED -> NEW` (GATE1B Sec 10.2). Enforcement is application-layer and/or a
  DB trigger; `CANDIDATE`.
- `C-R3` (PROPOSED): `current_generation >= 0`, `latest_admission_seq >= 0`.
- `C-R4` (`DERIVED`, Architect, PR #43 review round 3): root lifecycle
  `DELETED` is a **ROOT-level tombstone only**. It MUST NOT cascade
  `resource_presence='REMOVED'` onto the root's resources and MUST NOT emit
  `resource-removed` for them. Each resource keeps its last committed presence
  (it may still be `PRESENT`). Root deletion emits at most the root-level
  `root-deleted` journal event. `FACT` (Architect; Gate 1B Sec 10.3).

### 3.2 T2 `index_generation` — Generation

| Column | Type | Null | Constraint / notes |
|--------|------|------|--------------------|
| `root_id` | `uuid` | NO | FK -> `index_root(root_id)`. |
| `generation_number` | `bigint` | NO | Monotonic per root, > 0. |
| `produced_by_snapshot_id` | `uuid` | YES | Null only for non-reconcile generation effects if any; normally set. |
| `produced_by_admission_seq` | `bigint` | YES | Links generation to the admitted input that produced it (IO1). |
| `produced_at` | `timestamptz` | NO | |
| `summary` | `jsonb` | NO | Reconcile summary: added / marked_missing / unchanged / rejected counts. |

Constraints:

- `C-G1` PRIMARY KEY (`root_id`, `generation_number`).
- `C-G2` (`DERIVED`): `generation_number` strictly increases per root; the
  current value equals `index_root.current_generation`. No global generation
  (GATE1B 1.5; `FACT`).

### 3.3 T3 `index_canonical_resource` — CanonicalResource + tombstone + removal evidence

| Column | Type | Null | Constraint / notes |
|--------|------|------|--------------------|
| `resource_id` | `uuid` | NO | PRIMARY KEY. Kernel-assigned, immutable (M3). |
| `root_id` | `uuid` | NO | FK -> `index_root`. Immutable (disjoint partitions). |
| `introduced_at_generation` | `bigint` | NO | |
| `last_confirmed_generation` | `bigint` | NO | |
| `resource_presence` | `text` | NO | CHECK IN (`PRESENT`,`REMOVED`). |
| `removal_evidence_state` | `text` | NO | CHECK IN (`NONE`,`MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT`,`REMOVAL_CANDIDATE`). Kernel-internal (M6). |
| `missing_since` | `timestamptz` | YES | Set at first `MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT`. |
| `consecutive_complete_missing` | `integer` | NO | DEFAULT 0. Reset on reappearance (C3). |
| `canonical_path` | `text` | YES | Derived, NOT identity. |
| `parent_resource_id` | `uuid` | YES | Derived hierarchy (nullable for root children). NOT identity. |
| `name` | `text` | YES | Current canonical leaf name. |
| `is_dir` | `boolean` | YES | |
| `size` | `bigint` | YES | Weak evidence. |
| `mtime` | `timestamptz` | YES | Weak evidence; absent is not positive match evidence. |
| `content_hash` | `text` | YES | OPTIONAL fingerprint, NOT identity (INV-021). |
| `hash_algorithm` | `text` | YES | Required iff `content_hash` present (CHECK). |
| `content_type` | `text` | YES | |
| `current_attributes` | `jsonb` | NO | Last-reconciled canonical attributes aggregate. |
| `created_at` | `timestamptz` | NO | |
| `updated_at` | `timestamptz` | NO | |

Mapping:

| Gate 1B field | Column | Tag | Evidence |
|---------------|--------|-----|----------|
| `resource_id` | `resource_id` | `DERIVED` | 1.4; `FACT` |
| `root_id` | `root_id` | `DERIVED` | 1.4, Sec 3; `FACT` |
| `introduced_at_generation` | `introduced_at_generation` | `DERIVED` | 1.4; `FACT` |
| `last_confirmed_generation` | `last_confirmed_generation` | `DERIVED` | 1.4; `FACT` |
| `resource_presence` | `resource_presence` | `DERIVED` | 1.4; Safe Reconcile 1.3; `FACT` |
| `removal_evidence_state` | `removal_evidence_state` | `DERIVED` | 1.4; Safe Reconcile 1.3; `FACT` |
| `current_attributes` | `current_attributes` (+ denormalized `name/size/mtime/content_hash/...`) | `DERIVED` | 1.4; `FACT` |
| `canonical_path` | `canonical_path` | `DERIVED` | 1.4; `FACT` |
| (Kernel-internal) `missing_since` | `missing_since` | `DERIVED` | Safe Reconcile 1.3; `FACT` |
| (Kernel-internal) consecutive-complete-missing counter | `consecutive_complete_missing` | `DERIVED` | Safe Reconcile 1.3.1 C3; `FACT` |

Constraints (correctness):

- `C-C1` PRIMARY KEY (`resource_id`).
- `C-C2` (`DERIVED`): `resource_id` and `root_id` are write-once (immutability,
  M3). Enforced by no-UPDATE policy on those columns.
- `C-C3` (`REJECTED` — Architect, PR #43 review #5): a partial UNIQUE index on
  (`root_id`, `canonical_path`) `WHERE resource_presence = 'PRESENT'` is INVALID.
  Gate 1B R8 explicitly permits the old resource to remain `PRESENT` with MISSING
  evidence while a new resource receives a new `resource_id` at the SAME path. A
  false uniqueness constraint MUST NOT paper over this. `FACT` (Architect).
- `C-C3a` (`DERIVED`): NO uniqueness constraint on (`root_id`, `canonical_path`).
  Multiple `PRESENT` rows MAY share a path; they are distinguished by
  `resource_id` and `removal_evidence_state`. Persistence MUST represent the
  overlap. `FACT` (Architect; Gate 1B R8).
- `C-C3b` (`PROPOSED`): a NON-unique index on (`root_id`, `canonical_path`,
  `removal_evidence_state`) supports deterministic path resolution (doc C Q5).
- `C-C4` (`PROPOSED`): CHECK `(content_hash IS NULL) = (hash_algorithm IS NULL)`.
- `C-C5` (`DERIVED`): CHECK `resource_presence = 'REMOVED'` implies
  `removal_evidence_state` is a terminal value and the row is retained (M7).
- `C-C6` (`PROPOSED`): CHECK `resource_presence = 'PRESENT'` may carry any
  `removal_evidence_state` (PRESENT + MISSING evidence is valid — Blocker F).

Indexes:

- `I-C1` (`PROPOSED`): (`root_id`, `resource_presence`) — active-resource scans.
- `I-C2` (`PROPOSED`): (`root_id`, `parent_resource_id`) — hierarchy listing.
- `I-C3` (`PROPOSED`): (`root_id`, `canonical_path`) — path resolution (NON-unique; see `C-C3a`).
- `I-C4` (`PROPOSED`): (`root_id`, `removal_evidence_state`) `WHERE
  resource_presence = 'PRESENT'` — removal-candidate sweep.
- `I-C5` (`PROPOSED`): (`root_id`, `removal_evidence_state`, `missing_since`) —
  grace-period evaluation.

> `DERIVED` `removal_evidence_state` is Kernel-internal and MUST NOT be surfaced
> as consumer-visible resource status (M6). The Query Contract (doc C) exposes
> only `resource_presence`. `FACT` (Safe Reconcile Sec 1.3 Blocker F).

### 3.4 T4 `index_identity_evidence_observation` — IdentityEvidence (append-only, authoritative)

> `DERIVED` (Architect decision, PR #43 review #4) — Gate 1B states evidence is
> multi-sourced and versioned. A mutable aggregate row cannot preserve the
> historical evidence values that produced earlier decisions, especially when
> full SnapshotEntry persistence is optional. Therefore the **normalized
> append-only observation history is authoritative**, and a current aggregate is
> a rebuildable projection/cache only. `FACT` (Architect).

Authoritative table `index_identity_evidence_observation` (append-only):

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `observation_id` | `bigserial` | NO | PK. Surrogate; NEVER an ordering cursor. |
| `resource_id` | `uuid` | NO | FK -> `index_canonical_resource`. |
| `snapshot_id` | `uuid` | NO | FK -> `index_snapshot`; the observation source. |
| `source_ref` | `text` | YES | Collector/adapter provenance of this observation. |
| `observed_at` | `timestamptz` | NO | When THIS observation was made. |
| `provider_object_id` | `text` | YES | OPTIONAL, NOT identity by itself. |
| `provider_object_id_scope` | `text` | YES | Required iff `provider_object_id` present. |
| `provider_identity_assurance` | `text` | NO | CHECK IN (`STABLE_WITHIN_SCOPE`,`UNVERIFIED`,`UNSTABLE`,`UNAVAILABLE`). |
| `content_hash` | `text` | YES | Fingerprint, NOT identity. |
| `hash_algorithm` | `text` | YES | |
| `observed_path` | `text` | YES | Weak continuity hint. |
| `observed_parent_ref` | `text` | YES | Collector-local. NEVER canonical `resource_id`. |
| `size` | `bigint` | YES | Weak. |
| `mtime` | `timestamptz` | YES | Weak; absent is not positive evidence. |
| `is_dir` | `boolean` | NO | Affects move matching (R6). |
| `extra_evidence` | `jsonb` | YES | Opaque; not a domain input (M2). |

Constraints:

- `C-E1` PK (`observation_id`).
- `C-E2` (`DERIVED`): append-only. No UPDATE/DELETE; a later observation is a new
  row, never a mutation of an earlier one. `FACT` (Architect; Gate 1B versioned
  evidence).
- `C-E3` (`PROPOSED`): indexes (`resource_id`, `observed_at`) and (`resource_id`,
  `snapshot_id`).
- `C-E4` (`PROPOSED`): CHECK `(provider_object_id IS NULL) =
  (provider_object_id_scope IS NULL)`.
- `C-E5` (`DERIVED`): only `provider_identity_assurance = 'STABLE_WITHIN_SCOPE'`
  makes `provider_object_id` STRONG evidence (GATE1B 1.6; `FACT`). The Store does
  not enforce this; it is Kernel matching logic. Stored as data for audit.
- `C-E6` (`DERIVED`): canonical-decision evidence MUST remain durably
  reconstructable from here even when `index_snapshot_entry` persistence is
  disabled (PR #43 accepted direction). `FACT` (Architect).

Projection/cache table `index_identity_evidence_current` (NOT authoritative):

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `resource_id` | `uuid` | NO | PK, FK -> `index_canonical_resource`. |
| (current aggregate fields) | | | Latest folded values from the observation history. |
| `derived_from_observation_id` | `bigint` | YES | Last observation folded in. |
| `rebuilt_at` | `timestamptz` | NO | |

> `DERIVED` `index_identity_evidence_current` is a **projection/cache**: it MAY be
> dropped and rebuilt from `index_identity_evidence_observation` at any time, and
> is never the source of truth for audit or reconcile (Architect decision, PR #43
> review #4). `FACT`.
>
> `DERIVED` `observed_parent_ref` remains Collector-local and is never the
> canonical `resource_id` (GATE1A frozen boundary; `FACT`).

### 3.5 T5 `index_snapshot` — Snapshot metadata/evidence

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `snapshot_id` | `uuid` | NO | PK. |
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `provenance` | `jsonb` | NO | Collector, driver info. |
| `observed_at` | `timestamptz` | NO | |
| `started_at` | `timestamptz` | YES | |
| `finished_at` | `timestamptz` | YES | Absent if scan did not finish. |
| `traversal_status` | `text` | NO | CHECK IN (`SUCCESS`,`PARTIAL`,`FAILED`,`INTERRUPTED`). |
| `error_summary` | `jsonb` | YES | count, typed categories, first error. |
| `skipped_scopes` | `jsonb` | YES | list of skipped prefixes + reasons. |
| `freshness_evidence` | `text` | YES | CHECK IN (`FRESH_DIRECT`,`FRESH_REFRESHED`,`CACHED_FRESH`,`STALE`,`UNKNOWN`). Missing -> `UNKNOWN`. |
| `collector_completeness_assurance` | `text` | YES | CHECK IN (`STRONG_FAILURE_VISIBILITY`,`WEAK_FAILURE_VISIBILITY`,`UNKNOWN_FAILURE_VISIBILITY`). Missing -> `UNKNOWN_FAILURE_VISIBILITY`. |
| `scope_shrink_corroboration` | `text` | YES | CHECK IN (`NONE`,`CORROBORATED`,`CONTRADICTED`). |
| `completeness_flag` | `text` | NO | CHECK IN (`COMPLETE`,`PARTIAL`). Collector-declared flag. |
| `acceptance_state` | `text` | YES | CHECK IN (`COMPLETE`,`PARTIAL`,`FAILED`,`STALE`,`SUSPICIOUS`). Kernel classification (set on EVALUATED). |
| `lifecycle_state` | `text` | NO | CHECK IN (`DRAFT`,`SUBMITTED`,`EVALUATED`,`RECONCILED`,`REJECTED`,`RETIRED`). |
| `entry_count` | `bigint` | YES | Weak sanity material. |
| `byte_count` | `bigint` | YES | Weak sanity material. |
| `generation_hint` | `text` | YES | OPTIONAL, Collector-tracked; HINT only. |
| `created_at` | `timestamptz` | NO | |

Mapping: Snapshot 1.2 (all fields) and Completeness Sec 2/3 evidence inputs
(`FACT`). `acceptance_state` is the Kernel-owned classification
(GATE1B-SNAPSHOT-COMPLETENESS Sec 1.1 EVALUATED; `FACT`).

Indexes:

- `I-S1` (`PROPOSED`): (`root_id`, `observed_at`).
- `I-S2` (`PROPOSED`): (`root_id`, `lifecycle_state`).

> `DERIVED` A Snapshot is immutable once submitted (GATE1B 1.2). `lifecycle_state`
> and `acceptance_state` are the only mutable columns; they advance through the
> lifecycle but never rewrite the evidence columns. `FACT`.

### 3.6 T6 `index_snapshot_entry` — SnapshotEntry (audit/replay)

`CANDIDATE` — persisting full entries is optional (PR #43 accepted direction),
**provided** that evidence used for canonical decisions remains durably
reconstructable elsewhere (see `index_identity_evidence_observation` `C-E6` and
the `index_snapshot` evidence columns). It supports audit, replay, and
re-derivation, at storage cost. PoC target is 100k+ resources (blueprint Sec 15
PoC-1).

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `snapshot_id` | `uuid` | NO | FK -> `index_snapshot`. |
| `entry_local_id` | `text` | NO | Per-snapshot unique id. NOT canonical identity. |
| `name` | `text` | NO | |
| `parent_ref` | `text` | NO | Collector-local. NEVER canonical `resource_id`. |
| `is_dir` | `boolean` | NO | |
| `size` | `bigint` | YES | |
| `mtime` | `timestamptz` | YES | |
| `content_hash` | `text` | YES | |
| `hash_algorithm` | `text` | YES | |
| `provider_object_id` | `text` | YES | |
| `provider_object_id_scope` | `text` | YES | |
| `content_type` | `text` | YES | |
| `extra_evidence` | `jsonb` | YES | Opaque; not a domain input (M2). |

Constraints: `C-SE1` PK (`snapshot_id`, `entry_local_id`).

### 3.7 T7 `index_admission` — per-root serialized admission (IO1)

`DERIVED` semantic (IO1-IO7) / `PROPOSED` shape.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `admission_seq` | `bigint` | NO | Per-root monotonic. Kernel-owned order token. |
| `snapshot_id` | `uuid` | NO | FK -> `index_snapshot`. |
| `admitted_at` | `timestamptz` | NO | |
| `status` | `text` | NO | CHECK IN (`PENDING`,`APPLIED`,`NOOP`,`REJECTED`,`STALE_INPUT`,`FAILED`). |
| `applied_generation` | `bigint` | YES | Set when `APPLIED`; the canonical generation AFTER this application completes (== the unchanged `current_generation` when the application caused no mutation; == the new generation when it did). |
| `claimed_by` | `text` | YES | Worker/claim id currently processing this input (`PROPOSED`). |
| `claimed_at` | `timestamptz` | YES | Claim time (`PROPOSED`). |
| `lease_expires_at` | `timestamptz` | YES | Reclaimable after this time (`PROPOSED`). |

Constraints:

- `C-A1` PK (`root_id`, `admission_seq`).
- `C-A2` (`DERIVED`): `admission_seq` is assigned at a single serialized per-root
  ingress point BEFORE reconcile work (IO1). It is NOT derived from provider
  wall-clock (IO7). `FACT`.
- `C-A3` (`PROPOSED`): the admission high-water mark is
  `index_root.latest_admission_seq`, advanced in the same short admission
  transaction that allocates the sequence.
- `C-A4` (`DERIVED`, Architect decision, PR #43 review #2): crash recovery MUST
  be defined:
  - the head-of-line input (see `C-A5`) remains **reclaimable** after process
    death (via lease expiry or explicit claim release);
  - process death MUST NOT head-of-line block a root forever;
  - a reclaim/retry keeps the SAME `admission_seq` (it is never re-numbered);
  - if the head-of-line input is found already superseded by a committed higher
    input, it becomes `STALE_INPUT` per IO4.
  `FACT` (Architect).
- `C-A5` (`DERIVED`, Architect decision, PR #43 review round 2): **absolute
  per-root FIFO**. Define `head_seq` = the MINIMUM `admission_seq` for the root
  whose `status = 'PENDING'` (non-terminal; `APPLIED`/`NOOP`/`REJECTED`/
  `STALE_INPUT`/`FAILED` are terminal). Then:
  - ONLY the `head_seq` input MAY be claimed, processed, or committed;
  - an input with `admission_seq > head_seq` MUST NOT be claimed, processed, or
    committed while `head_seq` exists — **even if `head_seq` is currently being
    processed (claimed, lease not expired)**;
  - a reclaim is permitted only for `head_seq`, and only when its claim is free
    or its lease has expired;
  - higher sequences advance only after `head_seq` becomes terminal.
  A higher sequence MUST NOT "leapfrog" a lower non-terminal one under any
  circumstance (IO5/IO6). `FACT` (Architect).

> `DERIVED` `FAILED` is a valid terminal status (a reconcile that errored and was
> rolled back); the schema and doc B state machine MUST agree (PR #43 review #2).
> `FACT`.

> `DERIVED` An older admission sequence MUST NOT overwrite canonical state
> committed by a newer admitted input (IO2/IO4). Doc B defines the enforcement
> transaction flow. `FACT`.

### 3.8 T8 `index_applied_snapshot` — idempotency / application history (IO3)

`DERIVED` semantic (IO3) / `PROPOSED` shape.

This table is an **append-only application history**, NOT a single latest row.
The same `snapshot_identity` MAY be applied again after the canonical generation
advanced (a re-reconcile); each successful application appends one row.

> `DERIVED` (Gate 1B frozen; Architect, PR #43 review round 3) — a reconcile
> produces a NEW generation ONLY if it actually mutates canonical state. A
> zero-mutation re-reconcile keeps the `current_generation`, yet is still recorded
> as an application for that generation. `FACT`.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `snapshot_identity_kind` | `text` | NO | CHECK IN (`REVISION_TOKEN`,`DETERMINISTIC_DIGEST`). Part of the identity. |
| `snapshot_identity_namespace` | `text` | NO | Namespace that makes the value collision-free: for a token, the adapter/provider scope that issued it; for a digest, the canonicalization rule-set id. Adapter-declared. Part of the identity. |
| `snapshot_identity_version` | `text` | NO | Version of the identity algorithm/contract (required for the digest form; token-scheme version for the token form). Part of the identity. |
| `snapshot_identity_value` | `text` | NO | The revision token string or the digest hex. |
| `snapshot_id` | `uuid` | NO | The snapshot applied in this application. |
| `applied_generation` | `bigint` | NO | **Canonical generation AFTER this application completes** (post-application generation). If the application mutated canonical state, it is the NEW generation; if it caused no mutation, it is the UNCHANGED `current_generation`. |
| `applied_admission_seq` | `bigint` | NO | Admission input that produced it. |
| `applied_at` | `timestamptz` | NO | |

Constraints:

- `C-AS1` UNIQUE (`root_id`, `snapshot_identity_kind`,
  `snapshot_identity_namespace`, `snapshot_identity_version`,
  `snapshot_identity_value`, `applied_generation`) — the same logical identity is
  recorded at most ONCE per post-application generation; each later application
  (at a higher generation) appends a new row.
- `C-AS2` (`DERIVED`, PR #43 review round 2): the table is **append-only
  application history**. A row is NEVER UPDATEd to a newer generation (no
  upsert-latest); history is retained for audit/replay.
- `C-AS3` (`DERIVED`, IO3): the NO-OP test at reconcile time is exact — if a row
  exists with the identity tuple (`root_id`, kind, namespace, version, value) AND
  `applied_generation = current_generation`, it is a NO-OP. Otherwise (no row, or
  all matching rows have `applied_generation < current_generation`) the input is
  reconciled normally against the current generation. A zero-mutation reconcile
  MUST still append a row whose `applied_generation` equals the (unchanged)
  `current_generation`, so the NEXT identical collection at that generation is a
  NO-OP.

> `DERIVED` (Architect decision, PR #43 review #3) — the `snapshot_identity`
> contract is provider-neutral and MUST NOT include wall-clock, admission timing,
> or DB timing. It is exactly one of:
> 1. a stable adapter/native **revision token** whose stability the adapter
>    declares explicitly, or
> 2. a **versioned deterministic digest** over the normalized immutable Snapshot
>    semantics/content (the SnapshotEntry set plus relevant evidence), tagged
>    with `snapshot_identity_version`.
> Identical observed content collected at a different `observed_at` MUST yield
> the SAME identity. `kind` + `namespace` + `version` + `value` together form the
> identity, and ALL of them enter its uniqueness; two values that differ only in
> namespace/version MUST NOT be collapsed. Adapter E maps concrete sources to
> this contract later; A/B fix the key semantics now. `FACT` (Architect, PR #43
> review round 2).

> `DERIVED` (Architect, PR #43 review round 2 / round 3) — identity match alone
> does NOT imply NO-OP. Identical content (same identity) collected after the
> canonical generation advanced MUST be reconciled again; only the
> identity + unchanged-generation case is a NO-OP. Crucially, a re-reconcile
> produces a NEW generation only if it actually mutates canonical state (Gate 1B
> frozen); a zero-mutation re-reconcile keeps the generation and still records the
> application for that generation. `FACT`.

### 3.9 T9 `index_journal_event` — Canonical Change Journal (append-only)

`DERIVED`.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `event_seq` | `bigint` | NO | Per-root logical sequence, gap-free per committed transaction. |
| `event_id` | `bigserial` | YES | Opaque surrogate identity ONLY. **MUST NOT** be used as an ordering or consumption cursor (allocation order != commit order). |
| `generation_number` | `bigint` | NO | Generation the event belongs to. |
| `intra_generation_seq` | `integer` | NO | Orders the RENAME/MOVE + UPDATE pair within one generation (Blocker H). |
| `event_type` | `text` | NO | CHECK IN (`resource-added`,`resource-updated`,`resource-renamed`,`resource-moved`,`resource-removed`,`root-deprecated`,`root-deleted`). |
| `resource_id` | `uuid` | YES | Null for root-lifecycle events. |
| `payload` | `jsonb` | NO | Semantic payload (identity, old/new path, attributes, generation). |
| `committed_at` | `timestamptz` | NO | |

Constraints (correctness):

- `C-J1` PRIMARY KEY (`root_id`, `event_seq`).
- `C-J2` UNIQUE (`root_id`, `generation_number`, `intra_generation_seq`) — the
  intra-generation ordering key (Blocker H).
- `C-J3` (`DERIVED`): append-only. No UPDATE/DELETE granted to the Store writer
  role; optionally a trigger rejects UPDATE/DELETE. Events are never rewritten
  (J5/J6, Sec 3.4). `FACT`.
- `C-J4` (`DERIVED`): only the seven event types above exist. `MISSING`,
  `REMOVAL_CANDIDATE`, `UNCHANGED`, `CONFLICT`, `REJECTED` produce NO event
  (GATE1B-SAFE-RECONCILE Sec 3.1; `FACT`).

Indexes:

- `I-J1` (`PROPOSED`): (`root_id`, `event_seq`) — per-root replay/cursor.
- `I-J2` (`PROPOSED`): (`event_id`) — opaque identity lookup only; NOT an ordering/cursor index.
- `I-J3` (`PROPOSED`): (`root_id`, `generation_number`).

> `DERIVED` (Architect decision, PR #43 review #1) — the authoritative Journal
> ordering/cursor is the **per-root `event_seq`**. A global `bigserial`
> allocation order is NOT commit visibility order: under concurrent root
> transactions a consumer could observe id=11, advance, then transaction id=10
> commits later and is permanently skipped. `event_id` is therefore retained
> only as an opaque surrogate identity, never as a cross-root cursor. Cross-root
> consumers use a **vector of per-root cursors** (`{root_id: event_seq, ...}`)
> unless a future explicit commit-order mechanism is designed. `FACT` (Architect).
>
> `DERIVED` (Architect, PR #43 review round 2) — even consumed as a per-root
> cursor vector, an **all-roots journal read has NO canonical global order**. Only
> ordering WITHIN a root (`event_seq`) is guaranteed. Any physical merge order of
> multiple roots' events is a non-canonical implementation detail; consumers MUST
> NOT derive a cross-root total order from it. `FACT` (Architect).

### 3.10 T10 `index_root_config` — per-root policy

`CANDIDATE`.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | PK, FK -> `index_root`. |
| `removal_grace_period` | `interval` | NO | C2 time criterion. |
| `move_recognition_horizon` | `interval` | NO | R5/R3 recognition window. |
| `min_consecutive_complete_missing` | `integer` | NO | C3 default >= 1. |
| `min_independent_confirmations` | `integer` | NO | V2c default >= 1. |

Constraints:

- `C-RC1` (`DERIVED`): CHECK `removal_grace_period >= move_recognition_horizon`
  (GATE1B-DOMAIN-MODEL Sec 2.4 frozen relationship; `FACT`).
- `C-RC2`: all thresholds > 0 where required; `CANDIDATE`.

### 3.11 T11 `index_reconcile_result` — result + conflicts

`DERIVED` (conflicts must be recorded; Safe Reconcile 1.3.3, Sec 2.1) /
`PROPOSED` shape. Issue #40 allows persistence "only where required".

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `reconcile_id` | `uuid` | NO | PK. |
| `root_id` | `uuid` | NO | |
| `admission_seq` | `bigint` | NO | |
| `generation_number` | `bigint` | YES | Null if no generation advanced. |
| `outcome` | `text` | NO | CHECK IN (`RECONCILED`,`NOOP`,`REJECTED`,`STALE_INPUT`,`FAILED`). |
| `counts` | `jsonb` | NO | added/updated/renamed/moved/removed/unchanged. |
| `conflicts` | `jsonb` | YES | UNRESOLVED/CONFLICT records (NOT journal events). |
| `rejection_reason` | `text` | YES | |
| `created_at` | `timestamptz` | NO | |

> `DERIVED` CONFLICT and REJECTED are reconcile-result signals, not journal
> events (GATE1B-SAFE-RECONCILE Sec 1.1 rows 8/10, Sec 2.1). `FACT`.

---

## 4. Gate 1B state → persistence coverage matrix (consistency check #1)

> Every Gate 1B Domain field/state has an unambiguous persistence
> representation.

| Gate 1B concept | State/field | Persistence | Tag |
|-----------------|-------------|-------------|-----|
| ResourceRoot | `root_id` | `index_root.root_id` | `DERIVED` |
| ResourceRoot | `scope_descriptor` | `index_root.scope_descriptor` | `DERIVED` |
| ResourceRoot | `owning_collector_ref` | `index_root.owning_collector_ref` | `DERIVED` |
| ResourceRoot | `generation_cursor` | `index_root.current_generation` | `DERIVED` |
| Root lifecycle | `NEW/ACTIVE/DEPRECATED/DELETED` | `index_root.lifecycle_state` | `DERIVED` |
| Snapshot | `snapshot_id, root_id, provenance, observed_at, generation_hint` | `index_snapshot` | `DERIVED` |
| Snapshot | `completeness_flag` | `index_snapshot.completeness_flag` | `DERIVED` |
| Snapshot | Kernel `acceptance_state` | `index_snapshot.acceptance_state` | `DERIVED` |
| Snapshot | lifecycle states | `index_snapshot.lifecycle_state` | `DERIVED` |
| SnapshotEntry | all entry fields | `index_snapshot_entry` | `CANDIDATE` |
| CanonicalResource | `resource_id, root_id` | `index_canonical_resource` | `DERIVED` |
| CanonicalResource | `introduced_at_generation, last_confirmed_generation` | same | `DERIVED` |
| CanonicalResource | `resource_presence` | same | `DERIVED` |
| CanonicalResource | `removal_evidence_state` | same (Kernel-internal) | `DERIVED` |
| CanonicalResource | `current_attributes` | `current_attributes` + denormalized cols | `DERIVED` |
| CanonicalResource | `canonical_path` | `canonical_path` | `DERIVED` |
| Generation | `generation_number, produced_by, produced_at, summary` | `index_generation` + `index_root.current_generation` | `DERIVED` |
| IdentityEvidence | versioned observation history (authoritative) | `index_identity_evidence_observation` | `DERIVED` |
| IdentityEvidence | current folded aggregate (rebuildable projection/cache) | `index_identity_evidence_current` | `DERIVED` |
| Journal | 7 event types | `index_journal_event.event_type` | `DERIVED` |
| Journal | intra-generation ordering | `intra_generation_seq` | `DERIVED` |
| Admission ordering | IO1 sequence + absolute per-root FIFO head-of-line | `index_admission` (`admission_seq`, `status`); `head_seq` = min `PENDING` | `DERIVED` |
| Idempotency | snapshot identity (kind+namespace+version+value); append-only application history per post-application generation | `index_applied_snapshot` | `DERIVED` |
| Removal policy | grace/horizon/thresholds | `index_root_config` | `CANDIDATE` |
| Conflict result | UNRESOLVED/CONFLICT records | `index_reconcile_result.conflicts` | `DERIVED` |

No Gate 1B state is unrepresented. `INFERENCE` (coverage derived from Gate 1B
Sec 1-3 and Sec 10).

---

## 5. Schema-level expression of invariants

| Invariant | Schema expression | Tag |
|-----------|-------------------|-----|
| M3 root_id/resource_id immutable | write-once columns; no DELETE for roots; PK uniqueness prevents reuse | `DERIVED` |
| M4 journal append-only | `C-J3` (no UPDATE/DELETE role/trigger) | `DERIVED` |
| M5 atomic multi-write | all writes for one reconcile in a single transaction (doc B) | `DERIVED` |
| M7 tombstones retained | no physical delete; `resource_presence='REMOVED'` / root `DELETED` | `DERIVED` |
| Generation monotonic per root | `C-G2`; CAS in doc B | `DERIVED` |
| Removal evidence not consumer-visible | Query Contract excludes it (doc C) | `DERIVED` |
| No Collector-specific domain field | M2; `extra_evidence`/`metadata` opaque only | `DERIVED` |
| Path is a NON-unique coordinate; live rows MAY overlap on a path | NO unique constraint; `C-C3` REJECTED, `C-C3a` (no uniqueness), `I-C3` non-unique index | `DERIVED` |
| Admission is absolute per-root FIFO | only `head_seq` (min `PENDING` `admission_seq`) may be claimed/processed/committed; higher sequences blocked (`C-A5`) | `DERIVED` |
| Idempotency identity is complete and generation-scoped | identity = (kind, namespace, version, value); append-only history per `applied_generation`; NO-OP only at same generation (`C-AS1`..`C-AS3`) | `DERIVED` |
| Journal has NO cross-root total order | per-root `event_seq` ordering only; `event_id` opaque; all-roots reads define no canonical global order | `DERIVED` |
| Generation advances only on real canonical mutation | zero-mutation re-reconcile keeps `current_generation`; a row is still recorded for IO3 (`C-AS3`) | `DERIVED` |
| Root `DELETED` does not cascade resource tombstones | root-level only; children keep last committed presence; no bulk `resource-removed` (`C-R4`) | `DERIVED` |

---

## 6. What the schema must NOT do (Domain-leak guard)

| Prohibition | Reason | Tag |
|-------------|--------|-----|
| No table/column may reference a Collector, AList, rclone, or provider vendor as a domain field | INV-006, INV-011, M2 | `DERIVED` |
| No provider cache flag (e.g. `cache_bypassed`) as a domain column | freshness is normalized (`freshness_evidence` enum) | `DERIVED` |
| No `parent_ref` stored as canonical identity | `parent_ref` is Collector-local; `parent_resource_id` is Kernel-derived | `DERIVED` |
| No provider-native delta token as canonical state | principle 8, J3 | `DERIVED` |
| Kernel must not depend on these table/column names | principle 9; Store Interface only | `DERIVED` |

---

## 7. Schema requirements consumed by the transaction boundary (doc B)

This section only states what the schema must provide so doc B can enforce
atomicity and ordering. It does not define the transaction itself.

| Requirement | Schema support | Tag |
|-------------|----------------|-----|
| CAS on generation | `index_root.current_generation` single hot row | `DERIVED` |
| Serialized admission | `index_root.latest_admission_seq` single hot row | `DERIVED` |
| Atomic canonical + journal + generation + ordering commit | all tables in one transactional write set | `DERIVED` |
| Duplicate detection | `index_applied_snapshot` PK | `DERIVED` |
| Stale/out-of-order detection | `index_admission.admission_seq` vs high-water mark | `DERIVED` |
| Gap-free per-root journal | `C-J1` PK + intra-generation unique | `DERIVED` |

---

## 8. Consistency-check mapping (Issue #40)

| # | Check | Where addressed | Status |
|---|-------|-----------------|--------|
| 1 | Every Gate 1B field/state has a persistence representation | Sec 4 | `COVERED` |
| 2 | PARTIAL/STALE/SUSPICIOUS absence cannot advance removal state in SQL flow | Sec 3.3 (`removal_evidence_state` only mutated by COMPLETE path) + doc B | `COVERED` (doc B) |
| 3 | Confirmed removal = tombstone + journal event atomically | Sec 3.3/3.9 + doc B | `COVERED` (doc B) |
| 4 | Duplicate replay cannot produce duplicate changes/events | Sec 3.8 + doc B | `COVERED` (doc B) |
| 5 | Older admission sequence cannot commit over newer applied input | Sec 3.7 + doc B | `COVERED` (doc B) |
| 6 | MOVE/RENAME + UPDATE same-generation ordering | Sec 3.9 `intra_generation_seq` | `COVERED` |
| 7 | DELETED root cannot reconcile | Sec 3.1 `lifecycle_state` + doc B precondition | `COVERED` (doc B) |
| 8 | root_id/resource_id cannot be reused | Sec 3.1/3.3 constraints | `COVERED` |
| 9 | Consumers have no write path | doc C | `COVERED` (doc C) |
| 10 | Collector-specific fields do not leak into Domain | Sec 6 | `COVERED` |
| 5a | Absolute per-root admission FIFO (head-of-line; no leapfrog) | Sec 3.7 `C-A5` + doc B | `COVERED` |
| 4a | Idempotency identity includes kind/namespace/version; re-reconcile at a higher generation keeps history | Sec 3.8 `C-AS1`..`C-AS3` + doc B | `COVERED` |
| — | All-roots journal defines no canonical global order | Sec 3.9 + doc C Sec 5 | `COVERED` |

---

## 9. Open items (CANDIDATE / DEFERRED / UNKNOWN)

| Item | Tag | Note |
|------|-----|------|
| ~~Global vs per-root journal sequence~~ | `DERIVED` (CLOSED — Architect, PR #43 review #1, round 2) | Per-root `event_seq` is authoritative; `event_id` is opaque identity only; cross-root consumption uses a per-root cursor vector, and an all-roots read defines NO canonical global order. Sec 3.9. |
| ~~`snapshot_identity` dedup key~~ | `DERIVED` (CLOSED — Architect, PR #43 review #3, round 2) | Identity = (kind, namespace, version, value); all four enter uniqueness. Application is append-only history per generation; NO-OP only when identity matches AND `applied_generation = current_generation`. Sec 3.8. |
| Global commit-order cursor (should a future consumer require one) | `DEFERRED` | No commit-order mechanism is designed in Gate 1C; consumers use the per-root cursor vector (Sec 3.9). |
| Admission lease duration / reclaim policy | `CANDIDATE` | `C-A4` fixes the correctness rule; exact lease timing is operational config (doc B). |
| Persisting full SnapshotEntry | `CANDIDATE` | Storage cost vs audit/replay. Sec 3.6. |
| Root lifecycle transition enforcement (trigger vs application) | `CANDIDATE` | Sec 3.1 `C-R2`. |
| Enum storage (text+CHECK vs native enum) | `CANDIDATE` | Sec 3 preamble. |
| Partitioning / large-table strategy for 100k+ and beyond | `DEFERRED` | Post-MVP operational concern; PoC is single-node. |
| Physical retention policy for tombstones/old generations | `DEFERRED` | GATE1B Sec 10.3 notes retention is explicit policy. |
| `snapshot_identity` kind/namespace selection for Collectors with no stable token | `UNKNOWN` | The versioned deterministic-digest form covers them; the concrete adapter mapping is deliverable E. Sec 3.8. |

---

## 10. Self-check against Gate 1C constraints

| Constraint | Status |
|------------|--------|
| Did not start CloudSite integration | Met |
| Did not build UI | Met |
| Did not start Scanner Resume | Met |
| Did not start true incremental / native delta | Met |
| Did not invent a new gate | Met (uses Gate 1A -> 1B -> 1C -> 2) |
| Did not silently change Gate 1B semantics | Met (every mapping cites Gate 1B) |
| Did not copy license-incompatible donor code | Met (no code copied) |
| Did not merge own PR | Met (Worker stops after branch + PR) |
| Domain does not bind schema/ORM | Met (Sec 0.1, Sec 6) |

---

## 11. Golden cases (schema-level)

| # | Case | Expected schema behavior |
|---|------|--------------------------|
| G1 | New resource added | INSERT `index_canonical_resource` (PRESENT, NONE); INSERT `index_journal_event` (`resource-added`); bump `current_generation`; INSERT `index_generation`; UPDATE `index_admission.status='APPLIED'`. |
| G2 | Resource missing in a COMPLETE snapshot | UPDATE `removal_evidence_state='MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT'`, set `missing_since`; NO journal event. |
| G3 | Resource missing in a PARTIAL snapshot | NO change to `removal_evidence_state`, `missing_since`, or `consecutive_complete_missing`; NO journal event. |
| G4 | Confirmed removal | UPDATE `resource_presence='REMOVED'`; INSERT `resource-removed` event; same transaction. |
| G5 | Replay same snapshot | `index_applied_snapshot` hit -> no writes, no generation bump. |
| G6 | Stale input (older admission_seq) | Reject; record `index_reconcile_result.outcome='STALE_INPUT'`; no canonical mutation. |
| G7 | Path reused by imposter while the old resource is only MISSING | Old row stays `PRESENT` with `MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT` evidence; new row INSERTs with a new `resource_id` at the SAME `canonical_path`; BOTH rows coexist. No uniqueness constraint is violated (`C-C3` REJECTED; `C-C3a`). `resolve_path` (doc C Q5) MUST surface the overlap explicitly, never an arbitrary single winner. |
| G8 | RENAME + UPDATE in one pass | Two journal events same generation, `intra_generation_seq` 1 then 2. |
| G9 | Concurrent reconciles on two different roots | Both commit independently. Journal consumption MUST NOT assume a global `event_id` allocation order equals commit-visibility order: a consumer that advanced past a higher `event_id` MUST still be able to read a later-committed lower `event_id` via the per-root `event_seq` cursor vector. |
| G10 | Worker crashes after Stage 1 commit, leaving the head-of-line (`head_seq`) admission `PENDING` | The input stays durably `PENDING` with its `admission_seq`. A later worker reclaims it (lease expiry / claim release), reuses the SAME `admission_seq` (never re-numbered) and processes it. The root is not head-of-line blocked forever (`C-A4`/`C-A5`); no higher sequence leapfrogs it. |
| G11 | Same immutable snapshot content collected twice at different `observed_at` | Both scans map to the SAME `snapshot_identity` (`kind` + `namespace` + `version` + `value`). `observed_at` MUST NOT enter the identity. Whether the second application is a NO-OP depends ONLY on generation: same identity + unchanged `current_generation` -> `NOOP` (`C-AS3`); same identity but `current_generation` advanced -> normal re-reconcile (see G14). |
| G12 | A path is repeatedly impersonated before the old MISSING resource is removed | Each imposter gets a distinct new `resource_id`; at any instant multiple `PRESENT` rows MAY share the path, each addressable by `resource_id` and distinguished by `removal_evidence_state`. Persistence never collapses them into one row, and path resolution reports the ambiguity. |
| G13 | A lower `admission_seq` is being processed (claimed, lease NOT expired); a higher `admission_seq` is available | The higher input MUST NOT be claimed, processed, or committed; `head_seq` (min `PENDING`) blocks it until it reaches a terminal status (`C-A5`). No leapfrog even while the head is actively in flight. |
| G14 | Same `snapshot_identity` re-collected after canonical advanced G -> G+1, and the re-reconcile DOES mutate canonical state | NOT a NO-OP: apply the mutation, bump G+1 -> G+2, and APPEND a new `index_applied_snapshot` row with `applied_generation = G+2` (history retained; the earlier row is never UPDATEd). |
| G15 | Two identities that differ only in `snapshot_identity_namespace` or `snapshot_identity_version` | They are DISTINCT identities (kind + namespace + version + value all enter uniqueness); neither collapses into the other, and each may be applied independently. |
| G16 | Same `snapshot_identity` re-collected after canonical advanced G -> G+1, and the re-reconcile causes NO canonical mutation | The generation MUST NOT advance (stays G+1); a row is still APPENDED with `applied_generation = G+1` (identity + post-application generation), so the NEXT identical collection at G+1 is a NO-OP (`C-AS3`). |
| G17 | Root transitions to `DELETED` while some child resources are `PRESENT` | Only the ROOT is tombstoned: `index_root.lifecycle_state='DELETED'` and at most a `root-deleted` journal event. Child resources keep their last committed presence (some remain `PRESENT`); NO bulk `resource_presence='REMOVED'` and NO `resource-removed` events (`C-R4`). |

> Resolution of G7 (Architect, PR #43 review #5): the earlier note posed this as an
> `UNKNOWN`. The Architect ruled that `UNIQUE(root_id, canonical_path)` is invalid
> and MUST NOT be reintroduced; Gate 1B R8 is not silently changed. The Store
> represents genuine path overlap and exposes it through `resource_id`/history and
> an explicit `resolve_path` ambiguity result (doc C). `FACT` (Architect).