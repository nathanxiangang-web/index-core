# ADR-002 — PostgreSQL as the Store realization behind the Store Interface

> Status: **ACCEPTED** — Architect-approved in PR #43 (ARCHITECT FINAL ACCEPTANCE
> — Gate 1C CLOSED, final verification head `7a3b32f`).
> Date: 2026-09-24. Gate: 1C.
> Related (FROZEN): `docs/architecture/GATE1C-POSTGRESQL-STORE.md` (A),
> `docs/architecture/GATE1C-TRANSACTION-BOUNDARY.md` (B),
> `docs/architecture/GATE1C-QUERY-CONTRACT.md` (C),
> `docs/architecture/GATE1C-JOURNAL-PERSISTENCE.md` (D).
> Upstream: Gate 1A Store Interface; Gate 1B Domain/Reconcile/Completeness.

---

## Context

Gate 1A accepted a Store Interface boundary: persistence sits **behind** the Store
Interface and MUST NOT leak into the Domain or an ORM-shaped Domain model. Gate 1B
froze the semantics that persistence must realize:

- per-root monotonic generations; a new generation only on real canonical mutation;
- atomic canonical + journal + generation + admission/applied commit; failed
  commit preserves previous truth (INV-013);
- per-root admission ordering with absolute FIFO;
- append-only Canonical Change Journal with a per-root authoritative `event_seq`
  and **no** cross-root canonical order;
- append-only IdentityEvidence observations; structured `snapshot_identity` with
  application history;
- R8 path overlap represented explicitly (no database fake-uniqueness);
- root lifecycle and root-level-only DELETED tombstone semantics.

Gate 1C A/B/C (FROZEN, PR #43 Final Freeze Decision) fixed the concrete schema,
transaction boundary, and read-only Query Contract. D (the Journal persistence
contract) is **FROZEN** in the PR #43 ARCHITECT FINAL ACCEPTANCE (Gate 1C CLOSED,
final verification head `7a3b32f`).
This ADR records **why PostgreSQL is the PoC Store** and what that commits us to.

---

## Decision

Use **PostgreSQL** as the Store realization for the Gate 2 PoC, implementing the
FROZEN A/B/C contracts and the FROZEN D Journal-persistence contract, with these
concrete commitments:

1. **Isolation:** `READ COMMITTED` with explicit row locks / CAS; per-root
   serialization (`SELECT ... FOR UPDATE` or advisory lock) as the recommended
   option; `SERIALIZABLE` is an allowed alternative (B Sec 2.2, Sec 7).
2. **Generation advance:** conditional (B R11) — CAS + advance on mutation;
   compare-and-check (no advance) on a zero-mutation reconcile.
3. **Journal:** append-only enforced by revoking `UPDATE`/`DELETE` from the Store
   writer role (optional trigger as defense-in-depth); per-root `event_seq` is
   the only cursor; `event_id` is opaque (D Sec 3, Sec 7).
4. **Identity evidence:** append-only observation history is authoritative; the
   `_current` aggregate is a rebuildable projection.
5. **Path overlap:** no `UNIQUE(root_id, canonical_path)`; `resolve_path` returns
   all matches + `ambiguous` (A `C-C3` REJECTED; C `PathResolution`).
6. **Domain isolation:** the Store maps Domain <-> schema; no ORM type crosses the
   Store Interface.

---

## Alternatives considered

| Alternative | Disposition | Reason |
|-------------|-------------|--------|
| SQLite as PoC Store | **REJECTED (for PoC)** | Weaker concurrent-writer/locking profile; the whole point of Gate 1C is to freeze concurrency-safe multi-root semantics (per-root FIFO, CAS, MVCC reads). |
| Embedded KV store (e.g. RocksDB-class) | **REJECTED** | Would require hand-building transactional multi-table atomicity, MVCC snapshot reads, and constraint enforcement that PostgreSQL provides; raises PoC risk. |
| Document store (e.g. a JSON/document DB) | **REJECTED** | Weak relational integrity for the multi-table atomic unit (canonical + journal + generation + admission/applied); harder to enforce append-only and unique ordering keys. |
| In-memory store | **REJECTED** | No durability; INV-013 (previous truth survives failed commit) and crash-recovery semantics cannot be validated. |
| Specialized event-store database | **DEFERRED** | The Journal is append-only but is not the sole source of truth (Canonical Inventory is authoritative; J6). A general relational store realizes both atomically. |
| ORM-first domain model | **REJECTED** | Violates the Gate 1A boundary (Domain != ORM/schema). |

---

## Consequences

**Positive**

- Native support for the exact primitives A/B/C/D require: MVCC snapshot reads,
  row locks/advisory locks, multi-statement transactions, CHECK constraints,
  partial/unique indexes, and role-based privilege revocation (append-only).
- Concurrent roots are independent (no cross-root lock), matching the frozen
  no-cross-root-order rule.
- Constraint-driven correctness (PK `C-J1`, UNIQUE `C-J2`, identity uniqueness)
  makes violations hard failures rather than silent reorders.

**Negative / risks**

- Operational cost of running PostgreSQL in the PoC.
- The `index_root` row is a hot spot for same-root serialization; advisory locks
  are an option (B Sec 2.2 option B).
- Append-only must be enforced by privilege/trigger discipline, not by schema
  alone.
- `SERIALIZABLE` (if chosen) may require retry handling.

**Follow-ups**

- Final isolation-level and serialization-mechanism choice (B open items).
- Enum representation, page-size limits, physical retention/partitioning
  (A/C `CANDIDATE` items) — implementation choices that do not reopen the
  contract.

---

## Compliance with frozen contracts

| Frozen requirement | PostgreSQL realization |
|--------------------|------------------------|
| Domain != ORM/schema | Store maps Domain <-> schema; no ORM type at the boundary. |
| Failed commit preserves previous truth (INV-013) | Transactional `ROLLBACK`; no partial durable state. |
| Canonical + journal + generation + admission atomic | Single Stage-2 transaction (B `T-AT1`). |
| Stale/out-of-order cannot overwrite newer truth | Per-root FIFO + generation CAS (A `C-A5`, B R2/R11). |
| `root_id`/`resource_id` immutable | Kernel-assigned ids; no reuse (A `C-*`). |
| Journal append-only | Role revokes `UPDATE`/`DELETE`; optional trigger (D Sec 7.2). |
| Per-root `event_seq` only cursor; `event_id` opaque | `C-J1` PK; `I-J2` non-cursor index (A Sec 3.9). |
| No cross-root canonical order | Independent per-root transactions; vector cursors (A Sec 3.9, C `JC8`). |
| R8 path overlap | No path UNIQUE; `PathResolution(ambiguous)` (A `C-C3` REJECTED). |
| IdentityEvidence versioned | Append-only observations; `_current` projection (A Sec 3.4). |
| `snapshot_identity` structured | (`kind`,`namespace`,`version`,`value`) all in uniqueness; application history (A `C-AS*`). |
| Root-level-only DELETED | Root lifecycle only; no cascade `resource-removed` (A `C-R4`). |

### Issue #40 required consistency checks

| # | Check | Realization |
|---|-------|-------------|
| 1 | Every Gate 1B field/state has a persistence representation | A Sec 3 logical tables |
| 2 | PARTIAL/STALE/SUSPICIOUS absence cannot advance removal | B Sec 6 Scen. 2, Sec 1.2 R8/R9 |
| 3 | Confirmed removal = tombstone + journal atomically | B Sec 5, D Sec 7.1 |
| 4 | Duplicate replay no duplicate change/event | B Sec 3, D Sec 10 |
| 5 | Older admission cannot commit over newer | B Sec 4 |
| 5a | Absolute per-root FIFO | A `C-A5`, B R2/RC1–RC7 |
| 6 | MOVE/RENAME + UPDATE same-generation ordering | A `C-J2`, D Sec 6 |
| 7 | DELETED root cannot reconcile | B Sec 1.2 R4 |
| 8 | `root_id`/`resource_id` cannot be reused | A `C-*` |
| 9 | Consumers have no write path | C Sec 6 |
| 10 | Collector fields do not leak into Kernel | E Sec 2.3/AR3–AR9 |

---

## Evidence

- FROZEN `GATE1C-POSTGRESQL-STORE.md`, `GATE1C-TRANSACTION-BOUNDARY.md`,
  `GATE1C-QUERY-CONTRACT.md`, `GATE1C-JOURNAL-PERSISTENCE.md` (D).
- Gate 1B `GATE1B-DOMAIN-MODEL.md`, `GATE1B-SAFE-RECONCILE.md`,
  `GATE1B-SNAPSHOT-COMPLETENESS.md`, `GATE1B-ADVERSARIAL-CASES.md`.
- Gate 1A `GATE1A-STORE-QUERY-CONTRACT-SKELETON.md`,
  `GATE1A-RESPONSIBILITY-BOUNDARY.md`; `ARCHITECTURE-INVARIANTS.md` (INV-001..024).
- Issue #40 (Gate 1C execution template).