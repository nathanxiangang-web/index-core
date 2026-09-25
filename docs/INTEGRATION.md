# Application Integration Guide

IndexCore is infrastructure, not a full product backend.

For current architecture, read [ARCHITECTURE.md](ARCHITECTURE.md) first. For exact
wire endpoints, read [HTTP-API.md](HTTP-API.md).

---

## 1. Recommended consumer boundary

```text
Browser / mobile client
        ↓
Application server / BFF
        ↓
typed application-owned IndexCore client
        ↓ server-side HTTP
IndexCore read-only /v1
        ↓
Canonical Inventory + Journal
```

Do not make a public browser depend directly on IndexCore.

Current Alpha IndexCore:

- has no built-in application authentication;
- defaults to loopback;
- is intentionally a small canonical-query service;
- must not absorb product auth/session/tenant concerns.

Your application owns product behavior. IndexCore owns canonical resource truth.

---

## 2. Reference implementation

The accepted external consumer is:

`nathanxiangang-web/indexcore-reference-web`

Its runtime boundary is:

```text
Browser
   ↓
Reference Web (Next.js server)
   ↓
server-only typed IndexCore client
   ↓
INDEXCORE_BASE_URL
   ↓
IndexCore /v1
```

The browser never receives the private IndexCore origin.

Reference Web configuration:

```bash
INDEXCORE_BASE_URL=http://127.0.0.1:8080
```

The reusable idea is not “every product must use Next.js”.

The reusable idea is:

> one server-side application-owned IndexCore adapter, with no direct DB/provider
> coupling.

---

## 3. Interface ownership

Consumers should use only the Query Plane.

| Need | Correct interface |
| --- | --- |
| List/browse canonical resources | Q1–Q9 read-only HTTP |
| Root lifecycle/configuration | IndexCore CLI/operator |
| Full Collector scan | IndexCore CLI/operator |
| Trusted mutation signal | separate loopback Hint integration, not consumer UI |
| Product search/auth/history/download | product/application layer |
| Canonical mutation | IndexCore Kernel/runtime only |

A normal application should **not** call the trusted Hint endpoint.

The current Reference Web never calls it.

---

## 4. Typical application call flow

### 4.1 Application startup

The application server knows the private IndexCore base URL:

```text
INDEXCORE_BASE_URL=http://127.0.0.1:8080
```

Before considering IndexCore healthy:

```http
GET /readyz
```

Expected healthy response:

```json
{"status":"ready","schema_applied":4}
```

Treat HTTP 503 as temporarily unavailable/not ready.

Do not hard-code a schema number into product behavior unless you explicitly own
that deployment contract.

### 4.2 Root picker

```http
GET /v1/roots
```

Q2 returns default-visible NEW/ACTIVE roots.

For an audit/admin view only:

```http
GET /v1/roots?include_deprecated=true&include_deleted=true
```

### 4.3 Root page

A typical root page may call:

```text
Q1  GET /v1/roots/{root_id}
Q9  GET /v1/roots/{root_id}/status
Q4  GET /v1/roots/{root_id}/resources
```

Q4 is a hierarchy query.

Do not treat it as the whole-root flattened resource list.

### 4.4 Whole-root active view

```http
GET /v1/roots/{root_id}/active
```

Q6 returns PRESENT resources across the default-visible ACTIVE root.

Q6 has no DEPRECATED/DELETED root opt-in.

For retained audit partitions, use Q4/Q3 with explicit root visibility instead.

### 4.5 Resource detail

```http
GET /v1/resources/{resource_id}
```

Default visibility is PRESENT resources on default-visible roots.

When following an audit link, preserve the required options:

```text
include_removed=true
include_deprecated_root=true
include_deleted_root=true
```

as applicable.

### 4.6 Resolve a canonical path

```http
GET /v1/roots/{root_id}/resolve?path=/docs/report.txt
```

Correct handling:

```text
0 matches                  -> not found
1 match + ambiguous=false  -> one resolved resource
multiple / ambiguous=true  -> surface ambiguity
```

Do not silently choose one match.

### 4.7 Journal/projection flow

```http
GET /v1/roots/{root_id}/journal?after_seq=0&limit=100
```

Journal ordering is per root.

If the last event returned has:

```json
{"event_seq": 42}
```

the next request is:

```http
GET /v1/roots/{root_id}/journal?after_seq=42&limit=100
```

Do **not** send `after_seq=43`.

IndexCore already applies the exclusive rule `event_seq > after_seq`.

---

## 5. Pagination contract

Q4/Q6/Q7 use opaque generation-bound cursors.

Correct loop:

```text
request page 1
  ↓
receive next_cursor
  ↓
round-trip next_cursor unchanged
  ↓
request next page
```

Never:

- decode the cursor for business logic;
- edit it;
- combine pages across different generations.

If IndexCore returns:

```text
HTTP 409
{"error":"stale_cursor", ...}
```

discard the pagination chain and restart from page 1.

This is a normal concurrency outcome when canonical generation advances while a
consumer is paging.

---

## 6. Root lifecycle visibility

Default-visible roots:

- NEW;
- ACTIVE.

Audit visibility:

- DEPRECATED;
- DELETED.

The option names differ by endpoint.

Root list Q2:

```text
include_deprecated=true
include_deleted=true
```

Q1/Q3/Q4/Q5/Q9 audit reads:

```text
include_deprecated_root=true
include_deleted_root=true
```

Removed resource detail additionally requires:

```text
include_removed=true
```

Your application must propagate these options through links/breadcrumbs.

Dropping them produces apparently inconsistent `404 not_found` behavior even
though the retained audit data still exists.

---

## 7. Error handling

Do not flatten all failures into one generic “IndexCore error”.

At minimum distinguish:

| Case | Consumer meaning |
| --- | --- |
| `not_found` | object absent or hidden by visibility rules |
| `stale_cursor` | canonical generation advanced during paging |
| `invalid_cursor` | malformed/invalid request cursor |
| `invalid_request` | bad request input |
| `not_ready` | service currently not ready |
| timeout/unreachable | transport failure |
| malformed response | contract/runtime validation failure |

The Reference Web intentionally keeps these distinct.

A production product may map them to different UX, retries, or fallback behavior.

---

## 8. Typed client pattern

Keep IndexCore HTTP construction in one application-owned module.

Conceptual shape:

```text
app/
  indexcore/
    server        # reads INDEXCORE_BASE_URL, server only
    client        # Q1–Q9 request methods
    types         # consumer-owned wire DTOs
    errors        # typed transport/contract errors
```

UI code should call application functions, not construct arbitrary IndexCore
URLs.

Consumer DTOs should model the HTTP contract, not import IndexCore internal Go
domain models.

Closed enums should be validated at runtime.

Examples include:

- root lifecycle;
- resource presence;
- Journal event type.

Unknown values should be treated as a contract failure rather than silently cast.

---

## 9. What your application should not do

Do not:

- connect to the IndexCore PostgreSQL database;
- import IndexCore Go internals;
- mutate canonical tables;
- call the trusted Hint listener from the browser/product Web;
- infer deletion from one missing listing;
- treat `canonical_path` as identity;
- silently choose one Q5 match when `ambiguous=true`;
- decode generation cursors as business data;
- turn Journal `event_seq` into a global cross-root ordering assumption;
- store provider credentials in browser/application UI code;
- add product/user/search/download concerns to IndexCore only because the
  application needs them.

---

## 10. Search, catalog, user data, downloads

These belong outside IndexCore.

A future product may own its own database/projections for:

- users / permissions;
- search;
- catalog metadata;
- favorites / history / playback;
- sharing;
- preview/player;
- download/302 workflows;
- AI/recommendations.

Those projections may reference canonical `resource_id`.

They do not redefine whether the canonical resource exists.

---

## 11. Degraded operation

The application server should be able to distinguish:

```text
IndexCore ready
IndexCore not ready
IndexCore unreachable
IndexCore response malformed
```

The accepted Reference Web remains HTTP-serving when IndexCore is temporarily
down and renders an explicit degraded state.

It does not invent fake resource data.

After IndexCore restarts, the Reference Web recovers without needing its own
restart.

This behavior is part of the current P12 deployment-soak validation.

---

## 12. Do not confuse Query and Hint

Query flow:

```text
Reference Web / product
  -> GET /v1/**
  -> read Canonical truth
```

Hint flow:

```text
trusted same-host integration
  -> POST /internal/v1/mutation-hints
  -> durable dirty-scope signal
  -> optional hybrid runtime execution
```

They have different trust and capability boundaries.

A `202` Hint response is not a query result and is not a canonical visibility
guarantee.

For the exact Hint contract, see
[HTTP-API.md#trusted-internal-hint-transport](HTTP-API.md#trusted-internal-hint-transport).

---

## 13. Current Reference Web verification

Gate 4 verified the separate consumer with:

```text
PASS=52 FAIL=0
```

covering:

- Q1–Q9;
- hierarchy;
- whole-root active listing;
- resource detail;
- ambiguity;
- removed state;
- Journal;
- pagination;
- stale cursor;
- retained DEPRECATED/DELETED partitions;
- IndexCore unavailable state;
- independent IndexCore restart.

P12 now reuses this consumer as a continuous read-only observer while the P11
hybrid runtime is exercised over a deployment soak.

---

## 14. Related documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — current system architecture;
- [HTTP-API.md](HTTP-API.md) — exact HTTP contracts;
- [CLI.md](CLI.md) — operator/admin commands;
- [COLLECTORS.md](COLLECTORS.md) — provider configuration;
- [OPERATIONS.md](OPERATIONS.md) — deployment/runtime operations;
- [QUICKSTART.md](QUICKSTART.md) — local startup.

Reference implementation:

`nathanxiangang-web/indexcore-reference-web`
