# Index Core — Next Actions

## Current phase

**Gate 2 — PoC**

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex**

Active task: **#44 — [CODEX][GATE-2] Index Core PoC** (replaces Issue #40).

Branch: **`poc/gate2-indexcore`**. Stack (Architect-locked, PR #45 / #44):
**Go 1.27.x + PostgreSQL 18 + pgx/v5** — SQL-first migrations, no ORM,
real-PostgreSQL integration tests.

## Gate 1C — CLOSED

PR #43 received **ARCHITECT FINAL ACCEPTANCE — Gate 1C CLOSED** at final
verification head `7a3b32f`, and is authorized to merge to `main`.

- A `GATE1C-POSTGRESQL-STORE.md` — FROZEN
- B `GATE1C-TRANSACTION-BOUNDARY.md` — FROZEN
- C `GATE1C-QUERY-CONTRACT.md` — FROZEN
- D `GATE1C-JOURNAL-PERSISTENCE.md` — FROZEN
- E `GATE1C-COLLECTOR-ADAPTER-CONTRACT.md` — FROZEN
- ADR-001 Collector Boundary — ACCEPTED
- ADR-002 PostgreSQL Store — ACCEPTED

## Immediate objective

Begin the Gate 2 PoC on the frozen Gate 1C contracts: realize the PostgreSQL
Store, expose the read-only Query Contract, implement Journal persistence, and
validate the Collector Adapter Contract with **rclone as the first real Collector
for additive-safe validation only**.

## Codex work package (Gate 2 PoC)

Preconditions: Gate 1C CLOSED (met); execution issue #44 (received).

Planned scope (to be confirmed by the Gate 2 issue):

1. PostgreSQL Store realization of the frozen A/B/C contracts.
2. Journal persistence + projection catch-up per frozen D.
3. Read-only Query Contract surface per frozen C.
4. rclone Collector adapter for the additive-only role (E / ADR-001). It MUST NOT
   claim destructive-safe COMPLETE while `skipped_scopes` is UNKNOWN.
5. COMPLETE/removal Kernel logic validated with **controlled Snapshot V1/V2
   fixtures**.
6. Destructive COMPLETE deferred until a provider proves a positive completeness
   signal (per-provider exhaust/truncation evidence).

## Constraints carried forward

- Domain != PostgreSQL schema/ORM.
- failed commit preserves prior canonical truth.
- canonical + journal + generation + input-order state commit atomically.
- incomplete inputs cannot advance removal state.
- append-only journal; root/resource_id immutability; consumer write prohibition.
- Collector replaceability.

## Forbidden

- CloudSite integration
- UI
- Scanner Resume
- native delta / true incremental
- new gate names
- silent change to Gate 1B/1C semantics
- license-incompatible donor code copying

## Gate 2 exit direction

Gate 2 PoC succeeds when:

1. Snapshot -> PostgreSQL Inventory works against the frozen A/B/C/D contracts.
2. Reconcile is validated with controlled V1/V2 fixtures, including
   COMPLETE/removal.
3. rclone (additive-only) is validated as the first real Collector.
4. No Kernel semantics were changed without an Architect decision.
