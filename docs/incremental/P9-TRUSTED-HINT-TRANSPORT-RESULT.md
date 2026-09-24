# Incremental P9 — Trusted Hint Transport Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #91 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P9-TRUSTED-HINT-TRANSPORT-PROTOTYPE.md`
>
> Planning PR: #90 · Predecessor: P8 mutation hint ingestion — Issue #88 / PR #89 — ARCHITECT_ACCEPTED (merge `ef93ed9`)
>
> **SEPARATE LISTENER · DEFAULT DISABLED · LOOPBACK ONLY · BEARER TOKEN REQUIRED · SAME SERVE PROCESS / SAME WRITER LOCK · QUERY /V1 REMAINS READ-ONLY · MAX 4 IN-FLIGHT · 5S INGEST TIMEOUT · NO AUTO EXECUTION · NO SECOND WRITER · NO MIGRATION**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Endpoint and package

```text
POST /internal/v1/mutation-hints
```

Served only by the dedicated `internal/transport/hintapi` listener. The public
read-only Query listener (`internal/transport/httpapi`) does not expose this
route and its production code is unchanged.

`hintapi.Deps` holds only the narrow P8 ingress interface plus token/logger:

```go
type Ingester interface {
    IngestOne(ctx context.Context, req incrementalhint.Request) (state.DirtyScopeWork, error)
}
type Deps struct {
    Ingester Ingester
    Token    string
    Logger   *slog.Logger
}
```

No Store, pool, SQL, Scan, P4/P5/P6, or Kernel capability reaches the transport.

## 2. Configuration

New env/flag config (env-only token):

| Env | Flag | Default | Rule |
|---|---|---|---|
| `INDEXCORE_HINT_ADDR` | `--hint-addr` | empty | empty disables the transport |
| `INDEXCORE_HINT_TOKEN` | _(none)_ | empty | required when enabled, `>= 32` bytes |

`Config` fields: `HintAddr string json:"hint_addr,omitempty"`, `HintToken string
json:"-"`. The token is never serialized, never logged, and there is deliberately
no `--hint-token` flag.

Enabled address must be a literal loopback IP with an explicit non-zero port:
`127.0.0.1:<port>` or `[::1]:<port>`. Wildcard (`0.0.0.0`, `[::]`), non-loopback
IPs, hostnames, missing/invalid/zero ports are rejected at config validation; a
non-loopback address is never silently rewritten. The existing Query `HTTPAddr`
behavior is unchanged.

## 3. Authentication

`Authorization: Bearer <INDEXCORE_HINT_TOKEN>` is required. Missing, malformed
scheme, and wrong token all return the same `401 unauthorized` shape with
`WWW-Authenticate: Bearer`, using constant-time comparison, and never reach P8.
The configured or supplied token never appears in a response or log.

## 4. Request contract and limits

- `Content-Type: application/json` (media-type parameters such as
  `charset=utf-8` allowed) else `415 unsupported_media_type`.
- Body max 4096 bytes else `413 request_too_large`.
- Strict JSON: unknown fields, malformed JSON, and trailing/second JSON values are
  rejected with `400 invalid_request`.
- Transport-level validation mirrors P8 (`root_id` non-empty, scope via
  `state.ValidateScopeKey`, reason empty or one of the four hint reasons); P8
  validates again and remains authoritative. No `path.Clean`, no file→parent
  reinterpretation.
- Bounded ingress: **max 4 in-flight** authenticated ingestion calls, **5s**
  timeout per P8 call, no internal queue, no server-side retry. A fifth valid
  request while four are active returns `429 busy` with `Retry-After: 1`
  immediately. Failures return an opaque `503 ingest_unavailable` and never leak
  P8/Store/PostgreSQL error text.

## 5. Success contract

P8 success returns `202 Accepted`:

```json
{"status":"accepted","root_id":"...","scope_key":"/downloads","work_state":"PENDING","signal_seq":12}
```

`202` means the hint was durably merged into `DirtyScopeWork` only. It does not
mean provider verification ran or Canonical state changed. No Store internals,
provenance buckets, or secrets are exposed.

## 6. Serve topology and shutdown (hard gate)

Accepted order: schema compatible → `AcquireWriterLock` → worker → read-only
Query listener → optional Hint listener. The Hint listener is created only after
writer ownership; a writer-lock failure returns before any listener binds and the
Hint address is never reachable.

On shutdown the Hint transport is drained **before** the worker is stopped:
`stop accepting new hints → Shutdown → wait in-flight handlers → stop/join worker
→ release writer lock`. If the graceful timeout expires while a handler is still
active, the runner keeps holding the writer lock and continues waiting
(`WaitHandlers`). An explicit-but-unbindable Hint address fails `serve` closed; an
enabled Hint server exiting unexpectedly is fatal (no silent degraded mode).

## 7. Tests and evidence

P9 tests = **22**:

- config (5): disabled default; invalid loopback/token rejection and
  `127.0.0.1:<port>` / `[::1]:<port>` acceptance; env-only token (no
  `--hint-token` flag, `--hint-addr` present); env load.
- hintapi unit (8): `New` dependency/token rejection; auth failures (missing /
  wrong / short) → 401 with zero ingester calls and no token echo; malformed
  `Basic` scheme → 401; valid request → 202 + exact response contract + single
  ingester call; strict parsing table (415/413/400 cases + charset allowed);
  opaque 503 error privacy; concurrency backpressure (4 blocked + fifth 429 with
  `Retry-After: 1`, then a later request accepted after release); Hint listener
  exposes no Query routes (404).
- hintapi boundary (1): `Deps` has no write-capable type.
- hintapi timeout (1): shortened ingestion timeout cancels without retry.
- app (6, real PostgreSQL): disabled-transport regression; writer-lock required
  before the Hint address binds; occupied Hint port fails `serve` closed;
  unexpected Hint server failure is fatal; authenticated POST → P8 →
  `DirtyScopeWork` with duplicate HTTP hints coalescing to one row / `signal_seq`
  twice and **no Canonical resource** (no provider, no executor); shutdown with an
  in-flight Hint handler keeps the writer lock unacquirable until the handler
  returns.
- httpapi regression (1): the Query listener does not expose the hint route;
  `TestDepsExposeNoWriteCapability` remains green.

## 8. No automatic execution

P9 does not invoke P4/P5/P6. After `202 Accepted`, the `DirtyScopeWork` may remain
`PENDING` until a separately authorized actor executes it. Prototype limitation
documented in `docs/OPERATIONS.md`: with `serve` holding the writer lock,
`indexcore incremental run` cannot run concurrently, so a deployment using the P9
listener must stop `serve` before manual execution; continuous in-process
execution requires a future separately authorized scheduler/hybrid phase.

## 9. Changed files

```text
internal/transport/hintapi/server.go              (new)
internal/transport/hintapi/server_test.go         (new)
internal/transport/hintapi/boundary_test.go       (new)
internal/transport/hintapi/timeout_internal_test.go (new)
internal/runtime/config/config.go                 (HintAddr/HintToken + validation)
internal/runtime/config/hint_test.go              (new)
internal/runtime/app/app.go                       (optional Hint listener in serve)
internal/runtime/app/serve_hint_test.go           (new)
internal/transport/httpapi/boundary_test.go       (route-separation regression)
.env.example                                      (P9 env)
docs/HTTP-API.md                                  (P9 note)
docs/OPERATIONS.md                                (P9 config + operations)
docs/incremental/P9-TRUSTED-HINT-TRANSPORT-RESULT.md (new)
```

No `internal/transport/httpapi/**` production change, no
`internal/runtime/incrementalhint/**`, no `internal/store/postgres/**`, no
`internal/incremental/state/**`, no `internal/runtime/incrementalexec/**`, no
`internal/runtime/incrementalorch/**`, no `internal/runtime/scan/**`, no
`internal/runtime/worker/**`, no `internal/kernel/**`, no `internal/query/**`, no
`internal/domain/**`, no `internal/collector/**`, no migration, no `cmd/**`, and
no `go.mod`/`go.sum` change.

## 10. Regression results

```text
gofmt -l <Go files>             clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

Existing P3/P4/P5/P6/P7/P8 tests remain green.

## 11. Boundary statement

P9 adds one separate, loopback-only, bearer-authenticated Hint listener inside the
existing `indexcore serve` writer process. It does not turn the Query `/v1` API
into a write API, does not add write capability to `httpapi.Deps`, does not expose
a non-loopback/public hint transport, does not start a second writer/sidecar, does
not auto-execute P5/P6, and adds no production scheduler, ticker, native
delta/provider cursor, direct 115 integration, destructive removal, migration, or
Gate 5.

`FROZEN_CONTRACT_CHANGES: NONE`