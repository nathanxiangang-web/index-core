# Gate 1C — PostgreSQL Store Realization

> Implementation-facing contract for the PostgreSQL realization of the already
> accepted Gate 1A **Store Interface**.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
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
| T4 | `index_identity_evidence` | IdentityEvidence aggregate + evidence sources | `DERIVED` |
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
- `C-C3` (`PROPOSED`): partial UNIQUE index on (`root_id`, `canonical_path`)
  `WHERE resource_presence = 'PRESENT'` — at most one live resource per path per
  root. Tombstones are excluded so a removed path may be re-added as a fresh
  resource (Safe Reconcile 1.3.4).
- `C-C4` (`PROPOSED`): CHECK `(content_hash IS NULL) = (hash_algorithm IS NULL)`.
- `C-C5` (`DERIVED`): CHECK `resource_presence = 'REMOVED'` implies
  `removal_evidence_state` is a terminal value and the row is retained (M7).
- `C-C6` (`PROPOSED`): CHECK `resource_presence = 'PRESENT'` may carry any
  `removal_evidence_state` (PRESENT + MISSING evidence is valid — Blocker F).

Indexes:

- `I-C1` (`PROPOSED`): (`root_id`, `resource_presence`) — active-resource scans.
- `I-C2` (`PROPOSED`): (`root_id`, `parent_resource_id`) — hierarchy listing.
- `I-C3` (`PROPOSED`): (`root_id`, `canonical_path`) — path resolution.
- `I-C4` (`PROPOSED`): (`root_id`, `removal_evidence_state`) `WHERE
  resource_presence = 'PRESENT'` — removal-candidate sweep.
- `I-C5` (`PROPOSED`): (`root_id`, `removal_evidence_state`, `missing_since`) —
  grace-period evaluation.

> `DERIVED` `removal_evidence_state` is Kernel-internal and MUST NOT be surfaced
> as consumer-visible resource status (M6). The Query Contract (doc C) exposes
> only `resource_presence`. `FACT` (Safe Reconcile Sec 1.3 Blocker F).

### 3.4 T4 `index_identity_evidence` — IdentityEvidence

Two options are recorded; the Worker recommends **Option A**.

- **Option A (`PROPOSED`, recommended):** one row per (`resource_id`) holding the
  current aggregate evidence, plus a `evidence_sources` `jsonb` array of
  `(snapshot_id, collector_ref, observed_at)`.
- **Option B (`CANDIDATE`):** normalized child table
  `index_identity_evidence_source(resource_id, snapshot_id, collector_ref,
  observed_at, ...)` for full history.

Option A columns:

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `resource_id` | `uuid` | NO | PK, FK -> `index_canonical_resource`. |
| `provider_object_id` | `text` | YES | OPTIONAL. NOT identity by itself. |
| `provider_object_id_scope` | `text` | YES | Required iff `provider_object_id` present. |
| `provider_identity_assurance` | `text` | NO | CHECK IN (`STABLE_WITHIN_SCOPE`,`UNVERIFIED`,`UNSTABLE`,`UNAVAILABLE`). |
| `content_hash` | `text` | YES | Fingerprint, NOT identity. |
| `hash_algorithm` | `text` | YES | |
| `observed_path` | `text` | YES | Weak continuity hint. |
| `observed_parent_ref` | `text` | YES | Collector-local. NEVER canonical `resource_id`. |
| `size` | `bigint` | YES | Weak. |
| `mtime` | `timestamptz` | YES | Weak; absent is not positive evidence. |
| `is_dir` | `boolean` | NO | Affects move matching (R6). |
| `evidence_sources` | `jsonb` | NO | List of contributing (snapshot_id, collector_ref, observed_at). |

Constraints:

- `C-E1` (`PROPOSED`): CHECK `(provider_object_id IS NULL) =
  (provider_object_id_scope IS NULL)`.
- `C-E2` (`DERIVED`): only `provider_identity_assurance = 'STABLE_WITHIN_SCOPE'`
  makes `provider_object_id` STRONG evidence (GATE1B 1.6; `FACT`). The Store does
  not enforce this; it is Kernel matching logic. Stored as data for audit.

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

`CANDIDATE` — persisting full entries is optional. It supports audit, replay, and
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
| `status` | `text` | NO | CHECK IN (`PENDING`,`APPLIED`,`NOOP`,`REJECTED`,`STALE_INPUT`). |
| `applied_generation` | `bigint` | YES | Set when `APPLIED`. |

Constraints:

- `C-A1` PK (`root_id`, `admission_seq`).
- `C-A2` (`DERIVED`): `admission_seq` is assigned at a single serialized per-root
  ingress point BEFORE reconcile work (IO1). It is NOT derived from provider
  wall-clock (IO7). `FACT`.
- `C-A3` (`PROPOSED`): the admission high-water mark is
  `index_root.latest_admission_seq`, advanced in the same short admission
  transaction that allocates the sequence.

> `DERIVED` An older admission sequence MUST NOT overwrite canonical state
> committed by a newer admitted input (IO2/IO4). Doc B defines the enforcement
> transaction flow. `FACT`.

### 3.8 T8 `index_applied_snapshot` — idempotency (IO3)

`DERIVED` semantic (IO3) / `PROPOSED` shape.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `snapshot_identity` | `text` | NO | Dedup key for "same snapshot". |
| `snapshot_id` | `uuid` | NO | |
| `applied_generation` | `bigint` | NO | Generation at which it was applied. |
| `applied_admission_seq` | `bigint` | NO | |
| `applied_at` | `timestamptz` | NO | |

Constraints:

- `C-AS1` PK (`root_id`, `snapshot_identity`).
- `C-AS2` (`PROPOSED`): replay of the same `snapshot_identity` at the same
  canonical generation is a NO-OP (Safe Reconcile 2.4, IO3).

> `CANDIDATE` — the exact `snapshot_identity` key is not frozen by Gate 1B
> (R-LC-6 dedup key is CANDIDATE pending identity work). The Worker proposes the
> key be a Collector-declared stable content/cursor token scoped to the root,
> e.g. `sha256(root_id || source_ref || observed_at || entry_count ||
  byte_count)`, with `UNKNOWN` where a Collector cannot supply a stable token.
> Architect decision required.

### 3.9 T9 `index_journal_event` — Canonical Change Journal (append-only)

`DERIVED`.

| Column | Type | Null | Notes |
|--------|------|------|-------|
| `root_id` | `uuid` | NO | FK -> `index_root`. |
| `event_seq` | `bigint` | NO | Per-root logical sequence, gap-free per committed transaction. |
| `event_id` | `bigserial` | NO | Global physical order for cross-root cursor consumption. `PROPOSED`. |
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
- `I-J2` (`PROPOSED`): (`event_id`) — global cursor.
- `I-J3` (`PROPOSED`): (`root_id`, `generation_number`).

> `CANDIDATE` — global vs per-root sequence. The Worker recommends: **per-root
> logical `event_seq`** is authoritative (aligns with per-root generation and
> gap-free per-root ordering), and **global `event_id`** exists only as a
> convenience cursor for cross-root consumers. This must be accepted by the
> Architect (Issue #40 deliverable D).

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
| IdentityEvidence | all fields | `index_identity_evidence` | `DERIVED` |
| IdentityEvidence | `evidence_sources` | `evidence_sources` jsonb | `DERIVED` |
| Journal | 7 event types | `index_journal_event.event_type` | `DERIVED` |
| Journal | intra-generation ordering | `intra_generation_seq` | `DERIVED` |
| Admission ordering | IO1 sequence | `index_admission.admission_seq` | `DERIVED` |
| Idempotency | applied snapshot identity | `index_applied_snapshot` | `DERIVED` |
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
| Path uniqueness among live resources | `C-C3` partial unique index | `PROPOSED` |

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

---

## 9. Open items (CANDIDATE / DEFERRED / UNKNOWN)

| Item | Tag | Note |
|------|-----|------|
| Global vs per-root journal sequence | `CANDIDATE` | Worker recommends per-root logical + global physical (Sec 3.9). Deliverable D. |
| `snapshot_identity` dedup key | `CANDIDATE` | Not frozen by Gate 1B (R-LC-6). Sec 3.8. |
| Persisting full SnapshotEntry | `CANDIDATE` | Storage cost vs audit/replay. Sec 3.6. |
| Root lifecycle transition enforcement (trigger vs application) | `CANDIDATE` | Sec 3.1 `C-R2`. |
| Enum storage (text+CHECK vs native enum) | `CANDIDATE` | Sec 3 preamble. |
| Partitioning / large-table strategy for 100k+ and beyond | `DEFERRED` | Post-MVP operational concern; PoC is single-node. |
| Physical retention policy for tombstones/old generations | `DEFERRED` | GATE1B Sec 10.3 notes retention is explicit policy. |
| `snapshot_identity` for Collectors with no stable token | `UNKNOWN` | Depends on Collector adapter (deliverable E). |

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
| G7 | Path reused by imposter | Old row marked MISSING (not removed); new row INSERT with new `resource_id`; partial unique index `C-C3` satisfied because old is MISSING but still PRESENT — see note below. |
| G8 | RENAME + UPDATE in one pass | Two journal events same generation, `intra_generation_seq` 1 then 2. |

> Note on G7: `C-C3` allows only one `PRESENT` row per path. A path reused by an
> imposter while the old resource is only MISSING (still `PRESENT`) would violate
> `C-C3`. This is a **real design question** raised by the Worker: `FACT`
> (GATE1B R8 marks the old resource MISSING, `resource_presence` unchanged) vs
> `INFERENCE` (a unique live-path constraint). Resolution options:
> (a) drop `C-C3` and rely on Kernel invariant only;
> (b) scope uniqueness to `PRESENT` AND `removal_evidence_state='NONE'`;
> (c) treat imposter as CONFLICT until the old resource resolves.
> **UNKNOWN -> escalated to Architect** (do not silently pick).