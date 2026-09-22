# Index Core — Architecture Invariants

> These are guardrails, not a complete architecture.
> A Worker or Foreman may not silently change them.
> Any intentional change requires explicit Architect review and should normally become an ADR.

## INV-001 — One canonical resource truth

Index Core must have one canonical resource inventory.

Search indexes, catalogs, caches and application databases are projections/consumers, not competing resource truths.

## INV-002 — Search is not canonical inventory

A search index may be rebuilt, replaced or deleted without redefining the resource truth model.

## INV-003 — Missing is not deleted

Failure to observe an item during an incomplete, failed, stale or ambiguous collection run must not automatically become destructive removal.

## INV-004 — Incomplete input cannot authorize destructive reconcile

Any future destructive reconcile requires an explicit completeness/safety gate.

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

The exact final contract is not frozen yet, but implicit direct writes from provider code into canonical tables are prohibited.

## INV-010 — Identity and path are different concepts

A path, file name or URL must not automatically be treated as stable resource identity.

Stable identity design must explicitly account for rename and move.

## INV-011 — Driver capability is not universal capability

A field supported by one provider/driver does not become a guaranteed Index Core input field.

Contracts must represent optional/driver-dependent capability honestly.

## INV-012 — Full scan, diff, polling and native delta are distinct

Do not call directory refresh, local set diff or repeated full listing a provider-native change feed.

Terminology must remain precise.

## INV-013 — Previous canonical truth survives failed commit

A failed validation/reconcile/commit must not silently leave a partially authoritative new inventory.

Exact transaction/staging mechanism is not yet frozen, but the safety property is mandatory.

## INV-014 — Architecture before implementation

Discovery evidence → architecture/contracts → PoC → MVP.

Do not skip directly from an attractive donor implementation to product code.

## INV-015 — One gate at a time

A later stage may not begin until the prior gate is Architect-accepted.

Parallel research is allowed only inside the current authorized stage.

## INV-016 — Evidence over worker confidence

Worker statements such as “done”, “pass” or “works” have no architectural authority.

Claims require evidence appropriate to the phase:

- research: source/API/data evidence
- implementation: tests, fault injection, reproducibility

## INV-017 — License uncertainty blocks copying, not research

Unknown/restrictive license status does not block reading and architectural learning, but it blocks casual code copying into product implementation.

## INV-018 — Git is durable project memory

Accepted project truth must be recoverable from repository files, issues, ADRs, contracts, tests and commits.

Critical architectural state must not exist only in chat history.

## INV-019 — Worker context is intentionally narrow

Workers receive the minimum context needed for their task.

Workers do not independently reinterpret the full project roadmap.

## INV-020 — Keep the kernel small

Before adding a responsibility to Index Core, prove that it cannot remain in:

- Collector
- Storage adapter
- Consumer
- external mature tool

The default is to keep responsibilities outside the kernel unless canonical truth/safety requires kernel ownership.
