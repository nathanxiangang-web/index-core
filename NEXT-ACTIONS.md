# Index Core — Next Actions

## Current phase

**Gate 1C — Persistence / Query / Collector Boundary**

Architecture / acceptance owner:

**ChatGPT Architect**

Execution owner:

**Codex**

Active task:

**Issue #40 — [CODEX][GATE-1C] Persistence, Query & Collector Contracts**

## Immediate objective

Turn the accepted Gate 1A/1B semantics into implementation-facing contracts that are precise enough for Gate 2 PoC without coupling the Domain to PostgreSQL or to a specific Collector.

## Codex work package

Start from current remote `main` and read Issue #40.

Create:

`architecture/gate1c-implementation-contracts`

### PostgreSQL Store

Define the persistence representation for:

- ResourceRoot + lifecycle
- CanonicalResource + logical REMOVED tombstone
- RemovalEvidenceState
- Snapshot metadata/evidence required by accepted semantics
- per-root Generation
- serialized per-root admission/applied-input ordering state
- applied snapshot identity / idempotency tracking
- append-only Canonical Change Journal
- correctness indexes/constraints

### Transaction boundary

Freeze exact transaction behavior so one reconcile atomically:

1. checks generation/order preconditions
2. applies canonical changes
3. appends ordered journal events
4. advances generation
5. records applied snapshot/admission state
6. commits all-or-nothing

CAS/locking enforce ordering; database timing must not define it.

### Query Contract

Define the minimum provider-neutral read surface for future consumers:

- roots
- active resources
- resource lookup
- hierarchy/path resolution
- explicit tombstone/history access
- generation/status
- Change Journal cursor/sequence consumption

Consumers receive no canonical write path.

### Change Journal persistence

Freeze:

- sequence scope
- relation to generation
- intra-generation event ordering
- append-only guarantees
- corrective events
- projection catch-up semantics

### Collector Adapter Contract

Map AList/OpenList and rclone to the accepted normalized contracts:

- traversal
- failure visibility assurance
- freshness evidence
- identity assurance
- optional hashes
- skipped scopes/errors
- root/scope mapping
- operational complexity
- license boundary

Codex provides the evidence matrix and ADR recommendation.

ChatGPT Architect makes the final architecture acceptance/selection.

## Required consistency checks

Before PR, prove:

- every Gate 1B state has an unambiguous persistence representation
- PARTIAL/STALE/SUSPICIOUS cannot advance removal evidence in transaction flow
- confirmed removal tombstone + journal event commit atomically
- duplicate replay is idempotent
- stale/out-of-order input cannot overwrite newer truth
- MOVE/RENAME + UPDATE journal ordering is preserved
- DELETED root cannot reconcile
- IDs cannot be reused
- Consumer cannot mutate canonical truth
- Collector-specific fields do not leak into Kernel Domain

## Forbidden in Gate 1C

- CloudSite integration
- UI
- product MVP implementation
- Scanner Resume
- native delta / true incremental
- new gate names
- silent change to Gate 1B semantics
- license-incompatible donor code copying

## Gate 1C exit condition

ChatGPT Architect must be able to answer:

1. How is every accepted Domain state represented in PostgreSQL?
2. What exact transaction boundary preserves previous truth?
3. How are generation and admission ordering enforced?
4. How is the journal sequenced and replayed?
5. What can Consumers read?
6. What can Consumers never write?
7. How does a Collector prove normalized freshness/failure/identity assurance?
8. Which initial Collector adapter should Gate 2 validate, and why?
9. Can the Collector be replaced without rewriting Kernel semantics?

Only then may Gate 2 PoC begin.
