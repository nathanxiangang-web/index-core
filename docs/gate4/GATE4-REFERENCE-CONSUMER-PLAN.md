# Gate 4 — Reference Consumer Integration Plan

> Architect decision: 2026-09-24
> Execution issue: #50
> IndexCore baseline: main@f7edc518dfc99a43cfc464e332e6fe3bfcda601c
> Status: ARCHITECT_ACCEPTED / CLOSED

> Final accepted artifacts:
> - index-core PR #53 merge `9bc98fb7759f16d5c6b772cf4442dea133e2012b`
> - reference-web PR #2 merge `8f7062216dc9924f64d9ae0367e504c279704857`
> - index-core PR #52 merge `9d23b24f0ed715fce6128c031da99f6e111257ed`
> - final real E2E `PASS=52 FAIL=0`
> - `INDEXCORE_FROZEN_CONTRACT_CHANGES: NONE`

## 1. Decision

Gate 4 no longer means “integrate IndexCore into CloudSite”.

CloudSite 1.0 is treated as a **Legacy / Frozen Product**. It remains useful as
historical evidence for product requirements and UX, but it is not the new
architecture validation target and is not the foundation for the formal successor
product.

Gate 4 validates the Consumer boundary using a new disposable Web application.

## 2. Separate repository

Create:

`nathanxiangang-web/indexcore-reference-web`

This repository is:

- disposable;
- reference/integration-only;
- not CloudSite 2;
- not the final successor product;
- not a compatibility promise.

No CloudSite application code is to be copied.

## 3. Runtime shape

```text
Browser
   ↓
Reference Web
   ↓ server-side BFF / proxy
IndexCore HTTP /v1
   ↓
IndexCore
   ↓
PostgreSQL
```

Browser-to-IndexCore direct calls are forbidden in Gate 4.

## 4. Technology

- Next.js + TypeScript;
- App Router;
- server-side `INDEXCORE_BASE_URL`;
- server components / route handlers where appropriate;
- no separate FastAPI service;
- no application database;
- no ORM / Redis / auth framework;
- lightweight UI only.

## 5. Consumer client boundary

Inside the Reference Web:

```text
src/lib/indexcore/
  client.ts
  types.ts
  errors.ts
```

All IndexCore calls go through this client.

The client preserves HTTP contract semantics including:

- not_found;
- invalid_cursor;
- stale_cursor;
- ambiguity;
- upstream unavailable / timeout.

It is a validation artifact, not a frozen SDK.

## 6. Required Q1-Q9 coverage

Gate 4 must exercise over HTTP:

- Q1 get_root;
- Q2 list_roots;
- Q3 get_resource;
- Q4 list_resources hierarchy;
- Q5 resolve_path;
- Q6 list_active_resources;
- Q7 list_removed;
- Q8 read_journal;
- Q9 get_root_status.

Required pages:

```text
/
/roots
/roots/[rootId]
/resources/[resourceId]
/resolve
/removed
/journal
```

## 7. Required behavior

Must prove:

- nested hierarchy browsing;
- parent navigation;
- pagination;
- resource detail;
- explicit path ambiguity;
- active vs removed whole-root views;
- per-root Journal ordering;
- stale cursor UX that tells the user to reload from page 1;
- IndexCore unavailable state;
- independent restart/deployment of IndexCore and Reference Web.

## 8. Hard boundary proof

Reference Web must contain:

- 0 PostgreSQL access;
- 0 IndexCore Go dependency;
- 0 AList/OpenList direct access;
- 0 rclone dependency;
- 0 CloudSite runtime/code dependency;
- 0 application database;
- 0 canonical mutation path.

## 9. CloudSite policy

Allowed:

- historical UX/requirements research;
- critical security/operational maintenance only when separately authorized.

Not Gate 4:

- IndexCore integration into CloudSite;
- CloudSite V2 continuation;
- migration of CloudSite SQLite/domain models;
- wholesale code reuse.

Rule:

> Reference CloudSite requirements; do not inherit CloudSite architecture.

## 10. Findings discipline

The final report is:

`docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`

Every discovered missing capability must first be classified:

```text
IndexCore responsibility
Consumer responsibility
Future product responsibility
Out of scope
```

Do not automatically expand IndexCore just because the Reference Web wants a feature.

## 11. Non-goals

No:

- formal successor product;
- auth/user/admin;
- search/catalog;
- favorites/history/playback;
- shares;
- preview/player/Office;
- download/302 product path;
- 115;
- AI;
- CMS;
- IndexCore write API;
- browser-direct IndexCore exposure.

## 12. Exit

**EXIT CONDITION MET — GATE 4 CLOSED.**

Issue #50 was demonstrated end-to-end against the real Gate-3 IndexCore runtime.
The final Consumer report concludes that the current public read-only `/v1`
Query Contract is sufficient for a clean new application.

Gate 4 introduced no frozen IndexCore contract change and did not authorize a
formal successor product.

The next blueprint phase is Gate 5 — Future Product Architecture, but Gate 5 is
**NOT AUTHORIZED** until a new Architect plan is created.
