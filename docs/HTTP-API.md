# IndexCore HTTP API

IndexCore exposes a deliberately small **read-only** HTTP surface.

There are no canonical mutation endpoints. Root administration and scans are CLI/runtime responsibilities.

> The trusted Hint transport is a **separate, loopback-only,
> bearer-authenticated listener** (`POST /internal/v1/mutation-hints`), not part
> of the application-facing read-only `/v1` API. It is disabled by default.
> Normal applications and Reference Web consumers must not call it. See
> [ARCHITECTURE.md](ARCHITECTURE.md), [INTEGRATION.md](INTEGRATION.md), and
> [OPERATIONS.md](OPERATIONS.md).

Default address:

```text
http://127.0.0.1:8080
```

Current Alpha has no built-in authentication. Keep this interface private/server-side.

## Interface separation

IndexCore deliberately separates three interfaces:

| Plane | Interface | Caller |
| --- | --- | --- |
| Query | `GET /v1/**`, `/healthz`, `/readyz` | application server / BFF / Reference Web |
| Administration | `indexcore root/scan/migrate/doctor/incremental` CLI | operator/runtime |
| Trusted Hint | `POST /internal/v1/mutation-hints` on a separate loopback listener | trusted same-host integration only |

Do not turn the Query API into a write API and do not give a browser access to
the Hint listener.

## Endpoint summary

| Query | Method/path | Purpose |
| --- | --- | --- |
| Health | `GET /healthz` | process liveness |
| Readiness | `GET /readyz` | DB/schema/runtime readiness |
| Q1 | `GET /v1/roots/{root_id}` | one root |
| Q2 | `GET /v1/roots` | list roots |
| Q3 | `GET /v1/resources/{resource_id}` | one canonical resource |
| Q4 | `GET /v1/roots/{root_id}/resources` | hierarchy children |
| Q5 | `GET /v1/roots/{root_id}/resolve` | resolve canonical path |
| Q6 | `GET /v1/roots/{root_id}/active` | whole-root PRESENT listing |
| Q7 | `GET /v1/roots/{root_id}/removed` | removed/tombstone listing |
| Q8 | `GET /v1/roots/{root_id}/journal` | per-root canonical Journal |
| Q9 | `GET /v1/roots/{root_id}/status` | root generation/status |

For application integration, call these from the application server side, not
directly from public browser JavaScript.


## Health

### `GET /healthz`

Process liveness.

Example:

```json
{"status":"alive","version":"0.3.0-alpha ..."}
```

### `GET /readyz`

Database reachability, schema compatibility, and runtime readiness.

Successful example:

```json
{"status":"ready","schema_applied":4}
```

May return `503 not_ready`.

---

# Query API: Q1–Q9

## Q1 — Get root

```http
GET /v1/roots/{root_id}
```

Optional query parameters:

- `include_deprecated_root=true`
- `include_deleted_root=true`

Example:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID"
```

Response:

```json
{
  "root_id": "11111111-1111-4111-8111-111111111111",
  "lifecycle_state": "ACTIVE",
  "current_generation": 3,
  "created_at": "2026-09-24T00:00:00Z"
}
```

## Q2 — List roots

```http
GET /v1/roots
```

Optional query parameters:

- `include_deprecated=true`
- `include_deleted=true`

Default visibility is NEW + ACTIVE.

Example:

```bash
curl -s "http://127.0.0.1:8080/v1/roots?include_deprecated=true&include_deleted=true"
```

Response envelope:

```json
{"items":[{"root_id":"...","lifecycle_state":"ACTIVE","current_generation":3,"created_at":"..."}]}
```

## Q3 — Get resource

```http
GET /v1/resources/{resource_id}
```

Optional query parameters:

- `include_removed=true`
- `include_deprecated_root=true`
- `include_deleted_root=true`

Default resource visibility is PRESENT only.

Example response:

```json
{
  "resource_id": "22222222-2222-4222-8222-222222222222",
  "root_id": "11111111-1111-4111-8111-111111111111",
  "canonical_path": "/docs/report.txt",
  "parent_resource_id": "33333333-3333-4333-8333-333333333333",
  "name": "report.txt",
  "is_dir": false,
  "size": 128,
  "mtime": "2026-09-24T00:00:00Z",
  "resource_presence": "PRESENT",
  "introduced_at_generation": 1,
  "last_confirmed_generation": 3
}
```

Some nullable resource fields are omitted from JSON when unavailable.

## Q4 — List hierarchy resources

```http
GET /v1/roots/{root_id}/resources
```

Query parameters:

- `parent_id=<resource_id>` — omit for root-level children
- `cursor=<opaque>`
- `limit=<n>`
- `include_removed=true`
- `include_deprecated_root=true`
- `include_deleted_root=true`

This is a **hierarchy** query, not a flattened whole-root query.

Example:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/resources?limit=50"
```

Page response:

```json
{
  "items": [],
  "next_cursor": "opaque-value"
}
```

`next_cursor` is omitted on the final page.

## Q5 — Resolve canonical path

```http
GET /v1/roots/{root_id}/resolve?path=/docs/report.txt
```

Optional visibility parameters:

- `include_removed=true`
- `include_deprecated_root=true`
- `include_deleted_root=true`

Response:

```json
{
  "matches": [],
  "ambiguous": false
}
```

A path is a coordinate, **not identity**. If multiple PRESENT canonical resources occupy the same path, all matches are returned and `ambiguous=true`. A consumer must not silently choose a winner.

## Q6 — List active resources

```http
GET /v1/roots/{root_id}/active
```

Query parameters:

- `cursor=<opaque>`
- `limit=<n>`

Returns PRESENT resources across the entire default-visible root, regardless of parent.

Q6 has no DEPRECATED/DELETED root visibility option. For retained lifecycle partitions, use Q4/Q3 audit reads with explicit root visibility.

## Q7 — List removed resources

```http
GET /v1/roots/{root_id}/removed
```

Query parameters:

- `cursor=<opaque>`
- `limit=<n>`

Returns REMOVED tombstones for the whole root.

When following a returned resource into Q3, pass `include_removed=true`. If the root is DEPRECATED/DELETED, also pass the corresponding root visibility option.

## Q8 — Read change journal

```http
GET /v1/roots/{root_id}/journal
```

Query parameters:

- `after_seq=<event_seq>`
- `limit=<n>`

Events are strictly ordered **within the root** by `event_seq`.

There is no canonical global order across roots.

Important HTTP cursor rule:

> The query returns events where `event_seq > after_seq`. To fetch the next page, pass the **last event_seq you actually saw** as the next `after_seq`.

Do not add one yourself or the next event will be skipped.

Example event:

```json
{
  "event_seq": 7,
  "generation_number": 3,
  "intra_generation_seq": 1,
  "event_type": "resource-added",
  "resource_id": "22222222-2222-4222-8222-222222222222",
  "payload": "{}",
  "committed_at": "2026-09-24T00:00:00Z"
}
```

Frozen event types:

- `resource-added`
- `resource-updated`
- `resource-renamed`
- `resource-moved`
- `resource-removed`
- `root-deprecated`
- `root-deleted`

## Q9 — Root status

```http
GET /v1/roots/{root_id}/status
```

Optional query parameters:

- `include_deprecated_root=true`
- `include_deleted_root=true`

Response:

```json
{
  "root_id": "11111111-1111-4111-8111-111111111111",
  "lifecycle_state": "ACTIVE",
  "current_generation": 3,
  "last_applied_admission_seq": 9
}
```

---

# Pagination

Q4, Q6, and Q7 return generation-bound opaque cursors.

Rules for consumers:

1. treat `next_cursor` as opaque;
2. round-trip it unchanged;
3. do not decode it for product logic;
4. if the root generation changes, IndexCore returns `409 stale_cursor`;
5. on `stale_cursor`, restart paging from page 1.

Pages from generation G and generation G+1 must never be mixed.

# Error model

Stable transport cases include:

| HTTP | Error code | Meaning |
| ---: | --- | --- |
| 400 | `invalid_cursor` | Cursor cannot be decoded / request cursor invalid |
| 400 | `invalid_request` | Required request input is missing/invalid |
| 404 | `not_found` | Object does not exist or is hidden by visibility rules |
| 409 | `stale_cursor` | Cursor generation no longer matches current root generation |
| 500 | `internal_error` | Query failed unexpectedly |
| 503 | `not_ready` | Readiness probe failed |

Typical error envelope:

```json
{"error":"stale_cursor","message":"cursor generation is no longer current"}
```

# Visibility summary

- default roots: NEW + ACTIVE;
- DEPRECATED root: explicit opt-in;
- DELETED root: explicit opt-in; retained partition remains audit-readable;
- default resources: PRESENT only;
- REMOVED: explicit `include_removed=true` or Q7;
- root deletion does not cascade child presence state.

# Consumer reference implementation

The Gate 4 reference client is:

`nathanxiangang-web/indexcore-reference-web`

It demonstrates:

- server-side-only IndexCore access;
- typed wire validation;
- Q1–Q9;
- hierarchy navigation;
- stale cursor UX;
- path ambiguity;
- removed resources and journal;
- retained DEPRECATED/DELETED partitions;
- unavailable/restart behavior.

For integration rules, also read [INTEGRATION.md](INTEGRATION.md).


# Trusted internal Hint transport

This transport is operationally separate from the Query API.

Enable it only on an exact loopback literal:

```bash
export INDEXCORE_HINT_ADDR='127.0.0.1:8090'
export INDEXCORE_HINT_TOKEN='replace-with-at-least-32-random-bytes'
```

Request:

```http
POST /internal/v1/mutation-hints
Authorization: Bearer <INDEXCORE_HINT_TOKEN>
Content-Type: application/json
```

Body:

```json
{
  "root_id": "11111111-1111-4111-8111-111111111111",
  "scope_key": "/downloads",
  "reason": "POSSIBLE_CHANGE"
}
```

Allowed `reason` values are:

- empty string;
- `POSSIBLE_CHANGE`;
- `DELETE_HINT`;
- `MOVE_UNCERTAIN`;
- `METADATA_UNCERTAIN`.

Example with curl from the same host/process namespace:

```bash
curl -i \
  -H "Authorization: Bearer $INDEXCORE_HINT_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"root_id":"'"$ROOT_ID"'","scope_key":"/downloads","reason":"POSSIBLE_CHANGE"}' \
  "http://127.0.0.1:8090/internal/v1/mutation-hints"
```

Successful response:

```json
{
  "status": "accepted",
  "root_id": "11111111-1111-4111-8111-111111111111",
  "scope_key": "/downloads",
  "work_state": "PENDING",
  "signal_seq": 7
}
```

The exact `work_state` depends on the durable work row after the P8 merge.

`202 Accepted` means only that the Hint was durably accepted. It does not wait
for P6/P4/provider execution and does not guarantee the new Canonical state is
already visible.

The transport is bounded:

- request body: max 4096 bytes;
- authenticated ingestion concurrency: max 4;
- ingestion timeout: 5 seconds;
- no internal unbounded queue;
- `429 busy` includes `Retry-After: 1`;
- `503 ingest_unavailable` hides internal Store/provider details;
- invalid auth returns `401 unauthorized`;
- unsupported media type returns `415 unsupported_media_type`.

When the accepted hybrid runtime is enabled, a successful durable Hint merge
also issues a non-blocking coalesced runtime wake. The HTTP response still keeps
its durable-acceptance-only meaning.

Ordinary products, browsers, and `indexcore-reference-web` must not call this
transport.

