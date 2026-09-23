# Index Core — Next Actions

## Current phase

**Gate 5 — Future Product Architecture (NOT AUTHORIZED)**

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex**

Status: **GATE 4 CLOSED — no active execution**

Active execution issue:

**none — Gate 4 closed via Issue #50; Gate 5 not yet authorized**

Gate 4 (Reference Consumer Integration) is **CLOSED** and accepted
(ARCHITECT FINAL ACCEPTANCE, 2026-09-24). Merged:

- `indexcore-reference-web` PR #2 →
  `main@8f7062216dc9924f64d9ae0367e504c279704857`
- `index-core` PR #53 (verification fixture) → merged
- `index-core` PR #52 (findings report) →
  `index-core main@9d23b24f0ed715fce6128c031da99f6e111257ed`

Next action: **await Architect authorization of Gate 5**. Gate 5 is the next
blueprint phase and is **NOT authorized**; do not start any Gate-5 work.

The Gate 4 sections below are a closed historical record.

## Gate 4 objective (completed)

Validate that a brand-new Consumer can use IndexCore cleanly through the public
read-only HTTP contract without inheriting CloudSite history or IndexCore internals.

```text
Browser
   ↓
Reference Web
   ↓ server-side
IndexCore HTTP /v1
   ↓
Canonical Inventory / Journal
```

## Repository decision

Create a separate repository:

**`nathanxiangang-web/indexcore-reference-web`**

This repository is:

- disposable;
- an integration/reference client;
- not CloudSite 2;
- not the formal successor product;
- not a long-term compatibility promise.

Do not put the Reference Web inside the IndexCore repository.

## Technology lock

Reference Web:

- Next.js + TypeScript;
- App Router;
- server components / route handlers where appropriate;
- server-side `INDEXCORE_BASE_URL`;
- no separate FastAPI service;
- no database / ORM;
- no Redis;
- no auth framework;
- lightweight UI only.

## Required Consumer coverage

Exercise all frozen Query operations over HTTP:

- Q1 get_root;
- Q2 list_roots;
- Q3 get_resource;
- Q4 list_resources hierarchy;
- Q5 resolve_path and ambiguity;
- Q6 list_active_resources;
- Q7 list_removed;
- Q8 read_journal;
- Q9 get_root_status.

Minimum pages:

- `/` status summary;
- `/roots`;
- `/roots/[rootId]` hierarchy + pagination;
- `/resources/[resourceId]`;
- `/resolve`;
- `/removed`;
- `/journal`.

## Boundary rules

Reference Web must have:

- 0 direct PostgreSQL access;
- 0 IndexCore Go imports;
- 0 AList/OpenList access;
- 0 rclone dependency;
- 0 CloudSite runtime/code dependency;
- 0 application database;
- 0 canonical mutation path.

The browser must not call IndexCore directly.

## CloudSite policy

CloudSite 1.0 is now **Legacy / Frozen Product**.

Allowed:

- critical security/operational maintenance when separately requested;
- historical UX/product-requirement research.

Not part of Gate 4:

- IndexCore integration into CloudSite;
- CloudSite V2 refactor continuation;
- migration of CloudSite SQLite models into IndexCore;
- copying CloudSite backend/frontend wholesale.

## Work order

Follow Issue #50 P0-P10.

Recommended execution sequence:

1. create/reference repo + config + typed client;
2. Q1-Q9 pages and error states;
3. real IndexCore E2E + stale-cursor UX;
4. boundary proof;
5. write `docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md` in IndexCore;
6. stop for Architect review.

If the Reference Web finds a missing capability, classify it first as:

```text
IndexCore responsibility
Consumer responsibility
Future product responsibility
Out of scope
```

Do **not** silently expand IndexCore.

## Explicitly forbidden in Gate 4

- CloudSite integration/migration;
- formal successor product;
- login/register/auth/user/admin;
- search engine/catalog;
- favorites/history/playback;
- shares;
- preview/player/Office;
- download gateway / 302 product behavior;
- 115 downloader;
- AI;
- CMS;
- write APIs into IndexCore;
- direct browser-to-IndexCore public exposure;
- direct IndexCore PostgreSQL access;
- silent changes to Gate 1B/1C or Gate-3 accepted runtime semantics.

## Review handoff

Gate 4 requires:

- one implementation PR in `indexcore-reference-web`;
- one findings/report PR in `index-core` if needed for the final Gate-4 report.

The Worker does not merge either PR.

Use Issue #50 final status template and stop for ChatGPT Architect review.
