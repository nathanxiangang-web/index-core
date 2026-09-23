# Gate 1C — Canonical Change Journal Persistence

> Implementation-facing contract for the persistence, sequencing, consumption,
> and repair of the Canonical Change Journal.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **PARTIAL_FOR_ARCH_REVIEW** (deliverable D).
> Baseline: remote `main` = `6a131f17657807d9aee2921be1f286ceaff784e4`.
> Depends on the FROZEN contracts `GATE1C-POSTGRESQL-STORE.md` (A),
> `GATE1C-TRANSACTION-BOUNDARY.md` (B), `GATE1C-QUERY-CONTRACT.md` (C) and the
> accepted Gate 1B contracts (`GATE1B-SAFE-RECONCILE.md`, `GATE1B-DOMAIN-MODEL.md`).
> A/B/C are FROZEN (PR #43 Final Freeze Decision, final verification head
> `09faf41`). This document DERIVES from them and MUST NOT change their semantics.

---

## 0. Scope and tag system

This document freezes the persistence semantics of the Canonical Change Journal
so that Gate 2 PoC can implement it without inventing ordering, repair, or
projection rules. It does **not** re-open A/B/C.

Tags are the same as A/B/C:

- Design freedom: `DERIVED` (forced by a frozen upstream contract) /
  `PROPOSED` (Worker-proposed mechanism that closes a `DEFERRED_TO_GATE1C` /
  `CANDIDATE` gap; needs Architect acceptance) / `CANDIDATE` (implementation
  choice) / `DEFERRED` / `REJECTED`.
- Evidence: `FACT` (directly cited from a frozen contract or D02/D03 source) /
  `INFERENCE` / `UNKNOWN`.

Any statement tagged `DERIVED` here cites the frozen clause it comes from. Any
statement that closes a gap the upstream contracts left open (`intra_generation_seq`
numbering, append-only enforcement mechanism, corrective-event persistence,
projection catch-up protocol, lifecycle-event generation attribution) is tagged
`PROPOSED` and is explicitly the Worker's design proposal, not a frozen rule.

---

## 1. Immovable upstream rules (restated, not re-decided)

These are copied from frozen contracts and are the load-bearing inputs of D.
D adds no semantics to them.

| # | Rule | Source | Tag |
|---|------|--------|-----|
| U1 | The authoritative Journal ordering/cursor is the **per-root `event_seq`**; the global `bigserial event_id` is an **opaque surrogate identity only** and MUST NOT be used as an ordering or consumption cursor. | A Sec 3.9 `C-J1`/`I-J2`; C `JC2` | `DERIVED` / `FACT` |
| U2 | An all-roots Journal read has **NO canonical global order**; only ordering **within a root** (`event_seq`) is guaranteed. Physical merge order is non-canonical. | A Sec 3.9; C `JC8` | `DERIVED` / `FACT` |
| U3 | Exactly **seven** canonical event types exist: `resource-added`, `resource-updated`, `resource-renamed`, `resource-moved`, `resource-removed`, `root-deprecated`, `root-deleted`. `MISSING`/`REMOVAL_CANDIDATE`/`UNCHANGED`/`CONFLICT`/`REJECTED` produce **no** event. | Gate 1B Sec 3.1; A `C-J4` | `DERIVED` / `FACT` (Gate 1B `ACCEPTED_SEMANTIC`) |
| U4 | A new generation exists **only when a reconcile actually mutates canonical state**; a zero-mutation reconcile appends **no** Journal event and does not advance the generation. | Gate 1B generation rule; A `C-AS3`; B Sec 3 | `DERIVED` / `FACT` |
| U5 | The Journal is **append-only**; events are never edited or deleted. A reappearing resource appends `resource-added`; it never undoes a prior `resource-removed`. | Gate 1B `J5`; A `M4`/`C-J3` | `DERIVED` / `FACT` (Gate 1B `ACCEPTED_SEMANTIC`) |
| U6 | On disagreement, **Canonical Inventory is the truth**; the canonical Journal history is **never rewritten**. Repair is append-only (corrective event) or via a **derived projection rebuild**, never by editing canonical Journal or canonical Inventory. | Gate 1B `J6`, Sec 3.4; C `JC4`/`JC6` | `DERIVED` / `FACT` (Gate 1B `ACCEPTED_SEMANTIC`) |
| U7 | Canonical changes + Journal events + generation + admission/applied state commit **atomically, all-or-nothing**; a failed commit leaves previous truth intact. | A `M5`; B `T-AT1`..`T-AT4`; INV-013 | `DERIVED` / `FACT` |
| U8 | Replaying the same snapshot identity at the same `current_generation` is an **IO3 NO-OP**: no `ResourceEntry` change, **no Journal event**, no generation bump. | Gate 1B IO3 / Sec 2.4; A `C-AS3`; B Sec 3 | `DERIVED` / `FACT` |

> `DERIVED` D exists to formalize (not alter) U1–U8. Where D states a mechanism
> rather than a rule, it is tagged `PROPOSED`. `FACT`.

---

## 2. The Journal record

The physical table is A Sec 3.9 `index_journal_event` (append-only). D does not
change the schema; it fixes the **semantics of each ordering column**.

| Column | Meaning (frozen) | Source |
|--------|------------------|--------|
| `root_id` | The partition the event belongs to. | A Sec 3.9 |
| `event_seq` | Per-root logical sequence; **the authoritative cursor**; gap-free per committed transaction. | A Sec 3.9 / `C-J1` |
| `event_id` | Opaque surrogate identity only; NOT a cursor. | A Sec 3.9 / `I-J2` |
| `generation_number` | The generation the event belongs to. | A Sec 3.9 |
| `intra_generation_seq` | Orders events **within one generation**. | A Sec 3.9 / `C-J2` |
| `event_type` | One of the seven types (U3). | A Sec 3.9 / `C-J4` |

`C-J1` PRIMARY KEY (`root_id`, `event_seq`) and `C-J2` UNIQUE
(`root_id`, `generation_number`, `intra_generation_seq`) are the two ordering
constraints; `I-J3` (`root_id`, `generation_number`) supports generation scans.

> `DERIVED` The pair (`root_id`, `event_seq`) is the **only** consumption cursor.
> The pair (`root_id`, `generation_number`, `intra_generation_seq`) is the
> **within-generation** ordering. `FACT` (A `C-J1`/`C-J2`).

---

## 3. Authoritative per-root sequence (`event_seq`)

`PROPOSED` — allocation mechanism (closing the "assign a per-root logical
sequence, gap-free per committed transaction" wording of A `event_seq`).

- `event_seq` is assigned **per root**, **inside the reconcile transaction**, as
  `next_event_seq(root) = COALESCE(MAX(event_seq), 0) + 1` for the first event of
  the transaction and `+1` for each subsequent event produced by the same
  transaction.
- Because assignment happens inside the same transaction that appends the events,
  a **rolled-back transaction consumes no visible sequence number**: the
  committed sequence is gap-free (A `event_seq`: "gap-free per committed
  transaction"). Any internal "holes" left by rollback are never durable.
- `event_seq` is monotonic per root and never reused, even across root lifecycle
  changes (`NEW -> ACTIVE -> DEPRECATED -> DELETED`).
- Concurrency: because same-root reconciles are serialized (B Sec 2.2), sequence
  assignment is race-free by construction. A Store that instead relies on
  optimistic CAS (B option C) MUST derive `event_seq` from the committed
  per-root high-water mark so that no two committed transactions share a value.

> `INFERENCE` Under absolute per-root FIFO (A `C-A5`, B R2) at most one reconcile
> mutates a root at a time, so `event_seq` is a total order of committed
> transitions for that root. `FACT` once the per-root serialization is in place.

### 3.1 Consumption rule (restated from C `JC3`)

A consumer stores its **last-seen per-root `event_seq`** and resumes from
`last_seen + 1`. Within a root it may observe a duplicate only if it re-reads an
**inclusive boundary**; it MUST never assume gaps. `DERIVED` / `FACT` (C `JC3`).

---

## 4. No canonical cross-root order

`DERIVED` / `FACT` (A Sec 3.9; C `JC8`; U2):

- `event_id` allocation order is **not** commit-visibility order. Under
  concurrent roots, a consumer can observe `event_id = 11`, advance its cursor,
  and permanently skip a transaction with `event_id = 10` that commits later.
- Cross-root consumers MUST track a **per-root cursor vector**
  `{root_id: event_seq}` (C `JC2`/`JC7`).
- An all-roots read returns each root's events in `event_seq` order and defines
  **no** canonical order across roots. The physical merge order is a
  non-canonical implementation detail and MUST NOT be exported as a total order.
- D explicitly forbids any derived feature that would reintroduce a global
  Journal cursor (e.g. a monotonically increasing "global event offset" exposed
  to consumers).

---

## 5. Journal events and canonical generation

`DERIVED` — the generation relation (from A `generation_number`/`C-J2`, Gate 1B
Sec 1.5, U4).

- Each Journal event carries the `generation_number` of the reconcile that
  produced it. All events of one reconcile share the same `generation_number`
  (the **post-application generation**, B R12: `G+1` when the reconcile mutated
  canonical state).
- A zero-mutation reconcile produces **no event** and does not create a
  generation (U4). It may still record an application-history row (B Sec 3), but
  that row is not a Journal event.
- `generation_number` strictly increases per root across mutating reconciles;
  the Journal is therefore a sequence of generation-scoped event groups.
- Consumers reconstruct canonical state by applying a root's events in
  (`generation_number`, `intra_generation_seq`, `event_seq`) order.

### 5.1 Lifecycle-event generation attribution (`PROPOSED`)

- `root-deprecated` and `root-deleted` are produced by a root-lifecycle
  transition. When such a transition commits inside a reconcile transaction,
  the event carries that reconcile's post-application generation, exactly like
  resource events.
- If a lifecycle transition is applied by a dedicated lifecycle operation rather
  than a catalog reconcile, it MUST still commit in one transaction with the
  generation/state change it represents, and it MUST be assigned
  `event_seq`/`intra_generation_seq` by the same rules (Sec 3, Sec 6). It MUST
  NOT be emitted outside a committed transaction.

> `PROPOSED` This closes the generation-attribution gap A left open for
> lifecycle events; it adds no new event type. `INFERENCE`.

---

## 6. Same-generation ordering for MOVE/RENAME + UPDATE

Gate 1B freezes the **semantics**: when path and attributes change in one pass,
the reconcile produces an **ordered pair** within the same generation — first the
path-change transition (`RENAME` or `MOVE`), then the attribute-change transition
(`UPDATE`), committed atomically as two events. The **numbering scheme** was
`DEFERRED_TO_GATE1C`. D closes it.

`PROPOSED` — `intra_generation_seq` allocation:

- It is scoped to (`root_id`, `generation_number`) and starts at **1**.
- It is assigned in **Kernel transition order** within the reconcile: the
  path-change event receives the lower value, the attribute-change event the next.
- It is contiguous `1..N` for the events this reconcile produces in that
  generation (a zero-mutation reconcile produces none). `C-J2` UNIQUE (`root_id`,
  `generation_number`, `intra_generation_seq`) makes any duplicate or collision a
  hard failure, not a silent reorder.

`DERIVED` — the semantic ordering itself (path-change before attribute-change,
same generation, atomic commit) is fixed by Gate 1B Sec 1.2 rule 4 and is **not**
re-decided here. A `G8` already asserts "two journal events same generation,
`intra_generation_seq` 1 then 2". `FACT`.

> `INFERENCE` A consumer that observes the pair at generation `G+1` in
> `intra_generation_seq` order sees: resource at new path, then new attributes —
> matching the end state (new path + new attributes) while keeping both changes
> auditable as distinct events.

---

## 7. Append-only enforcement and atomicity

### 7.1 Atomicity (frozen)

`DERIVED` / `FACT`: canonical changes + Journal events + generation + admission/
applied state commit in **one transaction** (A `M5`; B `T-AT1`..`T-AT4`; U7). On
any error before commit, `ROLLBACK` leaves durable state unchanged; **no partial
Journal append and no partial generation bump is durable** (B `T-AT4`).

### 7.2 Append-only enforcement (`PROPOSED`)

A `C-J3` says append-only is enforced by "No UPDATE/DELETE granted to the Store
writer role; optionally a trigger rejects UPDATE/DELETE". D fixes the mechanism
as follows:

- **Primary (REQUIRED for PoC):** the Store writer role is granted only
  `INSERT`/`SELECT` on `index_journal_event`. `UPDATE` and `DELETE` are revoked.
- **Secondary (`CANDIDATE`):** a `BEFORE UPDATE OR DELETE` trigger raises an
  exception, giving a defense-in-depth failure even if a future role grant
  regresses.
- No code path exists that issues `UPDATE`/`DELETE` against
  `index_journal_event`. The Query Contract exposes no write path (C Sec 6 W1).

> `DERIVED` The append-only **semantic** is frozen (U5); the concrete
> role/trigger mechanism is the `PROPOSED` implementation above. `FACT` for the
> semantic, `CANDIDATE` for the trigger.

---

## 8. Corrective events (repair persistence)

Gate 1B `J6`/Sec 3.4 fixes two repair paths and defers the persistence mechanism
to Gate 1C:

- **Path A — corrective event:** append a corrective `resource-*` Journal event
  inside a committed reconcile transaction.
- **Path B — derived projection rebuild:** rebuild a read model; never edit the
  canonical Journal or canonical Inventory.

`PROPOSED` — persistence rules for Path A:

- A corrective event **reuses one of the seven existing `event_type`s**
  (`C-J4` forbids new types). Which `resource-*` type is chosen is the Kernel's
  semantic decision based on the observed disagreement.
- It is emitted through the **same** reconcile transaction mechanism: it gets a
  normal `event_seq` and `intra_generation_seq`, is committed atomically, and is
  visible to consumers as an ordinary event (C `JC4`).
- It is **never** marked as a rewrite, and it never edits, deletes, or back-dates
  the event it corrects. History is cumulative.
- If the repair does not mutate canonical state, no event is emitted (U4); the
  reconciliation is recorded only as an application-history row (B Sec 3).

> `DERIVED` Path A/B semantics and "never rewrite" are frozen (U6); the
> `event_type`-reuse rule is forced by `C-J4`; the transaction reuse is the
> `PROPOSED` mechanism. `FACT` for the constraint, `PROPOSED` for the mechanism.

---

## 9. Projection catch-up and rebuild

Gate 1B defers the projection rebuild/catch-up **protocol** to Gate 1C; C fixes
the consuming cursor semantics (`JC1`–`JC8`). D closes the protocol.

`DERIVED` / `FACT` (C `JC4`/`JC6`, U6):

- A projection is **non-authoritative**. When it disagrees with canonical
  Inventory, canonical wins and the projection rebuilds from the Journal.
- Canonical Inventory is **never** edited to satisfy a projection.

`PROPOSED` — catch-up protocol:

1. A projection stores a **per-root cursor vector** `{root_id: last_seen_event_seq}`
   (never a single global cursor; U2).
2. **Catch-up (incremental):** for each root, read `read_journal` from
   `last_seen_event_seq + 1` (exclusive lower bound) up to the current tail, apply
   events in (`generation_number`, `intra_generation_seq`, `event_seq`) order, and
   advance that root's cursor to the last applied `event_seq`.
3. **Inclusive-boundary re-read:** if a consumer re-reads from
   `last_seen_event_seq` (inclusive), it MUST deduplicate by (`root_id`,
   `event_seq`); duplicates are permitted, gaps are not (C `JC3`).
4. **Rebuild (from scratch):** choose a per-root floor (0 = beginning, or a
   caller-chosen `event_seq`) and replay forward; the result is a new projection
   instance, not an edit of canonical data.
5. **New root:** a root absent from the cursor vector starts from its beginning
   or a caller-chosen floor (C `JC7`).
6. **Isolation:** because each root is independent (A `C-J*`, U1/U2), projections
   catch up per root and MUST NOT assume any cross-root ordering.

> `DERIVED` The vector-cursor and rebuild semantics are frozen by C `JC1`–`JC8`;
> the step protocol above is `PROPOSED` and closes the Gate 1B
> `DEFERRED_TO_GATE1C` item. `INFERENCE`.

---

## 10. Rollback, failure, and replay guarantees

`DERIVED` / `FACT`:

| Guarantee | Statement | Source |
|-----------|-----------|--------|
| No partial Journal | A failed/rolled-back reconcile leaves **no** durable Journal event. | B `T-AT2`/`T-AT4`, Sec 6 Scenarios 3/5/7 |
| Prior truth preserved | A failed commit does not destroy previous canonical truth. | INV-013; B `T-AT3` |
| Atomic visibility | A read returns a single committed generation; no half-commit is observed. | C `CR1` |
| Replay is a NO-OP | Same snapshot identity at the same generation emits **no** event and no generation bump. | Gate 1B IO3/Sec 2.4; U8 |
| Re-reconcile is event-accurate | Same identity after the generation advanced runs a normal reconcile: mutation -> new events + `G+1`; zero mutation -> no events, generation unchanged, application row only. | B Sec 3, T12; A G14/G16 |

`PROPOSED` — failure-recording transaction shape (closes B `T-AT5`, `PROPOSED`):

- A failed reconcile's Journal writes are rolled back with the reconcile
  transaction. The failure itself is recorded by setting
  `index_admission.status='FAILED'` (terminal) in a **separate, later
  transaction**. That later transaction writes **no** Journal event (a failure is
  not a canonical transition, U3).
- The `FAILED` marker releases the head-of-line so the next sequence can proceed
  (B RC6), without leaving a partial Journal.

> `PROPOSED` The separate-transaction failure record is retained from B `T-AT5`;
> D only fixes that it emits no Journal event. `INFERENCE`.

---

## 11. Consistency mapping (Issue #40)

| Issue #40 check | Where it is satisfied in D |
|-----------------|----------------------------|
| 3 — confirmed removal = tombstone + journal atomically | U7, Sec 7.1 (one transaction; no partial Journal) |
| 4 — duplicate replay produces no duplicate changes/events | U8, Sec 10 (replay NO-OP; re-reconcile split) |
| 6 — MOVE/RENAME + UPDATE preserve same-generation ordering | Sec 6 (`intra_generation_seq` allocation; `C-J2`) |
| Journal append-only / never rewritten | U5/U6, Sec 7.2, Sec 8 |
| Journal sequence scope + relation to generation | Sec 3, Sec 5 |
| Projection catch-up | Sec 9 |
| Rollback / replay | Sec 10 |

---

## 12. Golden cases (journal-level)

| # | Case | Expected |
|---|------|----------|
| JD1 | Two roots reconcile concurrently | Each root's Journal is ordered by its own `event_seq`; NO cross-root total order is asserted; a per-root cursor vector consumes both without loss. |
| JD2 | Rollback mid-reconcile | No Journal event, no generation bump, no partial append is durable; `index_admission` becomes `FAILED` in a later tx. |
| JD3 | Duplicate snapshot replay at the same generation | IO3 NO-OP: no `ResourceEntry` change, **no Journal event**, no generation bump; the application-history row already exists. |
| JD4 | Re-reconcile after generation advanced, mutating | New generation `G+1`; the reconcile's events carry `generation_number = G+1`; `event_seq` continues gap-free. |
| JD5 | Re-reconcile after generation advanced, zero mutation | No Journal event; generation stays `G`; only an application-history row is appended (A G16, B T12). |
| JD6 | RENAME/MOVE + UPDATE in one pass | Two events at the same generation, `intra_generation_seq` 1 (path-change) then 2 (attribute-change); both auditable (A G8). |
| JD7 | Corrective repair (Path A) | A corrective `resource-*` event is appended with normal `event_seq`/`intra_generation_seq`; the corrected event is untouched; canonical wins. |
| JD8 | Projection catch-up with a per-root cursor vector | Each root resumes from `last_seen + 1`; no gaps; a root absent from the vector starts from its floor; no cross-root ordering assumed. |
| JD9 | Projection disagrees with canonical | Projection rebuilds from the Journal; canonical Journal and canonical Inventory are never edited. |
| JD10 | `event_id` observed out of commit order across roots | Consumer MUST NOT use `event_id` as a cursor; per-root `event_seq` vectors lose no event. |

---

## 13. Open items

| Item | Tag | Note |
|------|-----|------|
| `intra_generation_seq` exact allocation algorithm (Kernel transition enumeration) | `PROPOSED` | Sec 6 fixes scoping/contiguity; the Kernel-side enumeration implementation is a Gate 2 choice under the frozen semantics. |
| Append-only trigger vs role-only enforcement | `CANDIDATE` | Sec 7.2 requires role revocation; the trigger is optional defense-in-depth. |
| Corrective-event `resource-*` type selection policy | `PROPOSED` | Sec 8 fixes mechanism/constraint; the semantic choice rule follows Kernel repair logic (Gate 2). |
| Projection rebuild checkpoint storage | `CANDIDATE` | Sec 9 fixes the protocol; whether cursors are persisted in a table or derived is implementation. |
| Lifecycle event emitted by a dedicated lifecycle op vs a catalog reconcile | `PROPOSED` | Sec 5.1 requires one atomic transaction either way. |
| Journal retention / archival | `DEFERRED` | Not required for Gate 2 PoC; canonical Journal remains authoritative. |