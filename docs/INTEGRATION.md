# Application Integration Guide

IndexCore is infrastructure, not a full product backend.

The recommended integration shape is:

```text
Browser / App client
        ↓
Your application server / BFF
        ↓
IndexCore read-only HTTP /v1
        ↓
Canonical Inventory + Journal
```

Do not make a public browser depend directly on IndexCore.

## Why server-side

Current Alpha IndexCore:

- has no built-in authentication;
- defaults to loopback;
- is intentionally a small canonical-query service;
- must not absorb product auth/session/tenant concerns.

Your application owns product behavior. IndexCore owns canonical resource truth.

## What your application should use

Most products need only these Query operations:

- Q2 list roots
- Q4 hierarchy browsing
- Q3 resource detail
- Q5 path resolution
- Q6 whole-root active listing
- Q7 removed/audit view when needed
- Q8 change journal for projections
- Q9 root/generation status

See [HTTP-API.md](HTTP-API.md) for exact endpoints.

## What your application should not do

Do not:

- connect to the IndexCore PostgreSQL database;
- import IndexCore Go internals;
- mutate canonical tables;
- infer deletion from one missing listing;
- treat canonical_path as identity;
- silently choose one Q5 match when `ambiguous=true`;
- decode generation cursors as business data;
- turn Journal `event_id` into a global ordering assumption;
- add product/user/search/download concerns to IndexCore just because the application needs them.

## Pagination

For Q4/Q6/Q7:

- keep the cursor opaque;
- round-trip it exactly;
- handle `409 stale_cursor`;
- restart at page 1 after a stale cursor.

For Q8:

- Journal ordering is per root;
- store the last processed `event_seq` per root;
- the HTTP `after_seq` parameter is exclusive, so pass the **last seen event_seq** on the next request.

## Root lifecycle visibility

Default-visible roots:

- NEW
- ACTIVE

Explicit audit visibility:

- DEPRECATED → `include_deprecated_root=true`
- DELETED → `include_deleted_root=true`

When navigating from a lifecycle root to a resource detail page, preserve the matching root visibility option.

When navigating from Q7 to Q3, also preserve `include_removed=true`.

Q6 is for default-visible active resources and has no DEPRECATED/DELETED lifecycle opt-in. Use Q4/Q3 audit reads for retained lifecycle partitions.

## Path ambiguity

Q5 can legitimately return multiple PRESENT resources at one path.

Correct application behavior:

```text
ambiguous=false + one match -> use it
ambiguous=true              -> surface/handle ambiguity
zero matches                -> not found
```

Do not manufacture uniqueness from path.

## Search, catalog, user data, downloads

These belong outside IndexCore.

A future product may have its own database/projections for:

- user accounts / permissions;
- search engine;
- catalog metadata;
- favorites / history / playback;
- sharing;
- preview/player;
- download/302 workflows;
- AI/recommendations.

Those projections may reference `resource_id`, but they do not redefine canonical existence.

## Reference implementation

Use:

`nathanxiangang-web/indexcore-reference-web`

as a disposable integration example.

It was deliberately built with:

- no application database;
- no direct PostgreSQL;
- no Go dependency on IndexCore;
- no AList/rclone dependency;
- no CloudSite code;
- server-side-only `/v1` access.

Gate 4 verified this architecture end to end with `PASS=52 FAIL=0`.
