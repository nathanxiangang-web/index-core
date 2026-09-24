# Incremental P2 — Schema Sketch (conceptual, **NO SQL**)

> Status: **PROPOSED_FOR_ARCH_REVIEW — P2 DESIGN ONLY**
>
> Parent: #57 · Executing issue: #69 · Plan: `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`
>
> **STORE MIGRATION / DIRTY TABLES: NOT AUTHORIZED** — this document ships **no SQL** and no
> production Store code. It is a migration-ready conceptual sketch only.
>
> `FROZEN_CONTRACT_CHANGES: NONE`

Covers Issue #69 §G.

## 1. Conventions

- Names are **conceptual**; physical names/types are an implementation choice for a later authorized
  phase.
- All tables are root-scoped and live **behind the Store Interface** (Gate 1C A/ADR-002: PostgreSQL
  stays behind the interface; no leakage into Domain/ORM).
- Operational scheduler state is **not** Canonical state and is **not** exposed through Q1–Q9.
- Every mutable row carries a `version` column for CAS (contract §2.8 / §3.7).

## 2. Conceptual table `scope_watch_state`

| Column (conceptual) | Notes |
| --- | --- |
| `root_id` | FK to root; part of the logical key. |
| `scope_key` | Normalized root-relative path; part of the logical key. |
| `watch_state` | `HOT`/`WARM`/`COLD`/`DISABLED` (constrained text suggested). |
| `cadence_class` | Policy class. |
| `effective_interval` | Nullable; resolved interval. |
| `source_set` | Set of watch-policy provenance (§4). |
| `priority_class` | `URGENT`/`HIGH`/`NORMAL`/`LOW`. |
| `last_due_at` | Nullable instant. |
| `last_attempt_started_at` / `last_attempt_finished_at` | Nullable instants. |
| `last_success_at` | Nullable instant. |
| `next_due_at` | Nullable instant; authoritative due source. |
| `consecutive_failures` | Int; provider failures only. |
| `last_error_class` | Nullable constrained class. |
| `deferred_until` | Nullable instant (scheduling pressure). |
| `created_at` / `updated_at` | Instants. |
| `version` | Int; CAS token. |

**Unique key:** `(root_id, scope_key)`.

**Due-watch selection index (conceptual):** on
`(watch_state, next_due_at)` and/or a partial index restricted to
`watch_state IN (HOT,WARM) AND next_due_at IS NOT NULL`, optionally including
`priority_class`, `scope_key` for deterministic ordering. `deferred_until` is checked in the query
guard.

**Retention:** one row per `(root_id, scope_key)` retained while the policy exists; `DISABLED` rows
are retained (audit) and simply not selected.

## 3. Conceptual table `dirty_scope_work`

| Column (conceptual) | Notes |
| --- | --- |
| `root_id` / `scope_key` | Logical key. |
| `work_state` | `PENDING`/`IN_FLIGHT`/`VERIFIED`/`RETRY_WAIT`/`BLOCKED`/`SUSPENDED`. |
| `signal_seq` | Monotonic durable counter. |
| `claimed_signal_seq` | Nullable; set only while `IN_FLIGHT`. |
| `claimed_source_set` | Nullable; snapshot of the sources the attempt claimed (Watch attribution uses this). |
| `claimed_reason_set` | Nullable; snapshot of the claimed reasons. |
| `claimed_priority` | Nullable; claimed priority. |
| `pending_source_set` | Sources **not yet claimed**. Empty ⇒ no outstanding un-claimed signal. |
| `pending_reason_set` | Reasons not yet claimed. |
| `pending_priority` | Nullable; highest un-claimed priority. |
| `pending_first_seen_at` | Nullable instant; first un-claimed signal. |
| `pending_not_before` | Nullable instant; eligibility of the un-claimed signals. |
| `last_seen_at` | Instant (bookkeeping). |
| `attempt_count` | Int (diagnostic). |
| `consecutive_failures` | Int. |
| `last_attempt_started_at` / `last_attempt_finished_at` | Nullable instants. |
| `last_error_class` | Nullable class. |
| `last_verified_at` / `last_verified_signal_seq` | Nullable instant / bigint. |
| `created_at` / `updated_at` | Instants. |
| `version` | Int; CAS token. |

**Unique key (v1 decision):** `(root_id, scope_key)` — a **plain unique key**.

**v1 current-row decision (no history table):**

- There is **exactly one current row per `(root_id, scope_key)`, permanently** — the table is **not**
  append-only history and there is **no** partial-unique-index variant.
- `VERIFIED` is **retained** as the current state (never deleted/compacted in v1), keeping the
  "already verified at signal N" fast path and simple provenance/audit.
- `signal_seq` therefore remains a **single, strictly monotonic ever-increasing counter per key**
  (contract §3.3) — never reset, not across epochs and not after `VERIFIED`.
- History/compaction may be introduced only by a later Architect decision; **v1 does not** include it.

Rationale: a plain unique key makes the "one active logical item per key" invariant (contract §4 /
Issue #69 invariant 4) trivially enforceable in the database, with no partial-index ambiguity.

**Eligible-work selection index (conceptual):** on
`(work_state, not_before, priority_class, first_seen_at, scope_key)` restricted to the eligible states
(`PENDING`, `RETRY_WAIT`), enabling deterministic selection order.

## 4. Set representation (conceptual)

the `pending_*` / `claimed_*` set columns are small, closed, low-cardinality sets. Options:

- **Portable (recommended for MVP):** canonical text encoding of a sorted set (e.g. sorted,
  delimiter-joined tokens) with application-level union on merge; no provider-specific columns.
- **Native:** a Postgres `text[]`/enum array with set-union on merge.

Either is acceptable. Constraints: sets **union-merge within the current outstanding epoch** (§3.4 of
the contract) and are **reset at each epoch boundary**; order-independent equality; equality must not
depend on insertion order.

## 5. Attempt history: MVP necessity

- For MVP, per-attempt history is **not** required for correctness. `attempt_count`,
  `consecutive_failures`, `last_error_class`, and the last attempt timestamps on the current row are
  sufficient to implement the contract and the state machines.
- Operational visibility (per-attempt latency, `budget_defer_count`, `canonical_mutated`) may be
  emitted as **logs/metrics** rather than a dedicated history table.
- A dedicated attempt-history table may be added later if audit/metrics requirements demand it; P2
  does not authorize it and it is not needed to satisfy the invariants.

## 6. Migration-readiness checklist (no SQL shipped)

The sketch is migration-ready when:

1. both logical keys and every index above are named and justified;
2. `version` CAS columns exist on both tables;
3. the "one active logical item" invariant is enforceable by the plain unique key `(root_id, scope_key)`;
4. set columns have a chosen portable or native representation with **same-epoch union + epoch reset**;
5. `VERIFIED` rows are **retained** (v1) and `signal_seq` remains strictly monotonic — no compaction,
   no history table;
6. no operational-table column leaks into Q1–Q9 or the Canonical Journal.

## 7. Explicitly **not** defined here

- No SQL DDL, no migration numbers, no Store methods/queries.
- No provider-specific columns (no direct 115/AList/rclone fields).
- No queue/lease/fencing tables (single-writer only; no HA).
- No Canonical Inventory or Journal changes.