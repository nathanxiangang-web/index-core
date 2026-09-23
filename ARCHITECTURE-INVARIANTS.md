# Index Core — Architecture Invariants

> These are guardrails, not a complete architecture.
> Codex may not silently change them.
> Any intentional change requires explicit ChatGPT Architect review and should normally become an ADR.

## INV-001 — One canonical resource truth

Index Core must have one canonical resource inventory.

Search indexes, catalogs, caches and application databases are projections/consumers, not competing resource truths.

## INV-002 — Search is not canonical inventory

A search index may be rebuilt, replaced or deleted without redefining the resource truth model.

## INV-003 — Missing is not deleted

Failure to observe an item during an incomplete, failed, stale or ambiguous collection run must not automatically become destructive removal.

## INV-004 — Incomplete input cannot authorize destructive reconcile

Any destructive reconcile requires an explicit completeness/safety gate.

## INV-005 — Kernel is independent from CloudSite

CloudSite may consume Index Core but the kernel must not import CloudSite application concerns.

## INV-006 — Kernel is provider-neutral

Kernel domain logic must not require AList, OpenList, a specific cloud drive, or any specific storage vendor.

## INV-007 — Collector does not own canonical state

Collectors acquire external facts.

Collectors do not directly mutate canonical inventory according to provider-specific business logic.

## INV-008 — Consumer cannot redefine kernel truth

Search, Catalog, UI, AI features, download/playback logic and other consumers cannot become source-of-truth for resource existence.

## INV-009 — Snapshot/change input is an explicit boundary

External resource facts must cross a defined contract boundary before reconciliation.

Implicit direct writes from provider code into canonical tables are prohibited.

## INV-010 — Identity and path are different concepts

A path, file name or URL must not automatically be treated as stable resource identity.

Stable identity design must explicitly account for rename, move and path reuse.

## INV-011 — Driver capability is not universal capability

A field supported by one provider/driver does not become a guaranteed Index Core input field.

Contracts must represent optional/driver-dependent capability honestly.

## INV-012 — Full scan, diff, polling and native delta are distinct

Do not call directory refresh, local set diff or repeated full listing a provider-native change feed.

Terminology must remain precise.

## INV-013 — Previous canonical truth survives failed commit

A failed validation/reconcile/commit must not silently leave a partially authoritative new inventory.

## INV-014 — Architecture before implementation

Discovery evidence -> architecture/contracts -> PoC -> MVP.

Do not skip directly from an attractive donor implementation to product code.

## INV-015 — One gate at a time

A later stage may not begin until the prior gate is ChatGPT Architect-accepted.

## INV-016 — Evidence over executor confidence

Codex statements such as "done", "pass" or "works" have no architectural authority.

Claims require evidence appropriate to the phase:

- architecture/research: contracts, source/API/data evidence, adversarial consistency
- implementation: tests, fault injection, reproducibility

## INV-017 — License uncertainty blocks copying, not research

Unknown/restrictive license status does not block reading and architectural learning, but it blocks casual code copying into product implementation.

## INV-018 — Git is durable project memory

Accepted project truth must be recoverable from repository files, issues, ADRs, contracts, tests and commits.

Critical architectural state must not exist only in chat history.

## INV-019 — Codex executes a bounded Architect task

Codex receives an explicit task packet / Issue and works within it.

Codex must report architectural ambiguity instead of silently redefining the roadmap, invariants or phase boundaries.

## INV-020 — Keep the kernel small

Before adding a responsibility to Index Core, prove that it cannot remain in:

- Collector
- Storage adapter
- Consumer
- external mature tool

The default is to keep responsibilities outside the kernel unless canonical truth/safety requires kernel ownership.

## INV-021 — Hash is evidence, not canonical identity

A content hash may be a strong fingerprint when available, but identical content does not prove two observations are the same canonical resource.

## INV-022 — Incomplete coverage cannot create canonical absence

PARTIAL, STALE, SUSPICIOUS, failed or otherwise incomplete coverage may report unknown/unobserved scope, but must not by itself mutate a previously present canonical resource into a missing/removal state or advance removal evidence.

## INV-023 — Kernel does not re-traverse providers

Completeness and removal decisions consume normalized Collector evidence, accepted Snapshots and canonical history.

The Kernel does not independently call provider listing/refresh APIs to validate its own decisions.

## INV-024 — Accepted input ordering is Kernel-owned and deterministic

Store CAS protects commits, but commit timing must not define canonical ordering.

Duplicate, concurrent and out-of-order inputs require explicit deterministic per-root semantics.
