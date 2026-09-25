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

Enabled address must be an explicit non-zero port on **exactly** the literal
loopback hosts `127.0.0.1` or `::1` (`127.0.0.1:<port>` / `[::1]:<port>`). The
wider loopback range is deliberately rejected — `127.0.0.2`, IPv4-mapped forms
(`[::ffff:127.0.0.1]`), and expanded `::1` spellings fail — along with wildcard
(`0.0.0.0`, `[::]`), other non-loopback IPs, hostnames, and missing/invalid/zero
ports. A non-loopback address is never silently rewritten. The existing Query
`HTTPAddr` behavior is unchanged.

## 3. Authentication

`Authorization: Bearer <INDEXCORE_HINT_TOKEN>` is required. Missing, malformed
scheme, and wrong token all return the same `401 unauthorized` shape with
`WWW-Authenticate: Bearer` and never reach P8. The comparison hashes both the
supplied and configured values with SHA-256 and compares the two fixed 32-byte
digests, so any input length goes through a constant-length comparison (a raw
`ConstantTimeCompare` would return early on length mismatch and leak the token
length). The configured or supplied token never appears in a response or log.

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

The Query address is bound (`net.Listen`) **before** the Hint listener is created,
so a Query bind failure can never leave a reachable Hint listener.

Every inbound request is admitted into the shutdown lifecycle at the HTTP handler
entry point, before authentication, body read, or JSON parsing. Shutdown closes
admission (atomically with the admission check) and only then waits, so even a
request that is still uploading its body is tracked. This closes the window where
a slow-body handler could reach P8 after the writer lock was released.

On shutdown the Hint transport is drained **before** the worker is stopped:
`close admission → Shutdown → wait in-flight handlers → stop/join worker → release
writer lock`. If the graceful timeout expires while a handler is still active, the
runner keeps holding the writer lock and continues waiting (`WaitHandlers`). An
explicit-but-unbindable Hint address fails `serve` closed; an enabled Hint server
exiting unexpectedly is fatal (no silent degraded mode).

## 7. Tests and evidence

P9 tests = **26**:

- config (5): disabled default; invalid loopback/token rejection (including
  `127.0.0.2` and IPv4-mapped `[::ffff:127.0.0.1]`) and `127.0.0.1:<port>` /
  `[::1]:<port>` acceptance; env-only token (no `--hint-token` flag, `--hint-addr`
  present); env load.
- hintapi unit (9): `New` dependency/token rejection; auth failures (missing /
  wrong / short) → 401 with zero ingester calls and no token echo; malformed
  `Basic` scheme → 401; valid request → 202 + exact response contract + single
  ingester call; strict parsing table (415/413/400 cases + charset allowed);
  opaque 503 error privacy; concurrency backpressure (4 blocked + fifth 429 with
  `Retry-After: 1`, then a later request accepted after release); Hint listener
  exposes no Query routes (404); shutdown waits for a request still reading its
  body before the lifecycle gate drains.
- hintapi boundary (1): `Deps` has no write-capable type.
- hintapi timeout (1): shortened ingestion timeout cancels without retry.
- app (9, real PostgreSQL): disabled-transport regression; writer-lock required
  before the Hint address binds; occupied Query port fails serve before the Hint
  listener is exposed; occupied Hint port fails `serve` closed;
  unexpected Hint server failure is fatal; authenticated POST → P8 →
  `DirtyScopeWork` with duplicate HTTP hints coalescing to one row / `signal_seq`
  twice and **no Canonical resource** (no provider, no executor); shutdown with an
  in-flight Hint handler keeps the writer lock unacquirable until the handler
  returns; a cancellation-resistant worker plus a failed Query bind, and the same
  with a failed Hint bind, both keep the writer lock held until the worker
  actually stops and release it only afterwards.
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
## 12. Round 1 rework (Issue #91 review)

Four blockers from the P9 Round 1 review were fixed inside the authorized
`internal/transport/hintapi`, `internal/runtime/config`,
`internal/runtime/app`, and P9 tests/docs:

1. **Writer-lock handler lifetime (hardest).** Handler admission moved to the
   HTTP handler entry point (`Server.ServeHTTP` → `begin`) *before*
   authentication/body-read/JSON parsing. `Shutdown` closes admission under the
   same mutex (race-safe with `begin`) and only then waits via `WaitHandlers`, so a
   request still uploading its body is tracked. Proved by
   `TestP9ShutdownWaitsForRequestStillReadingBody` (WaitHandlers stays blocked
   while a slow-body request is in progress, and returns only after it completes).
2. **Query-before-Hint startup order.** `runServe` now binds the Query address
   with `net.Listen` first and serves it via `srv.Serve(queryLn)`; the Hint
   listener is created only afterwards. Proved by
   `TestP9QueryBindFailurePreventsHintExposure` (occupied Query port → serve fails,
   Hint address never reachable).
3. **Exact loopback restriction.** `validateHintAddr` now accepts only the literal
   hosts `127.0.0.1` / `::1`, rejecting `127.0.0.2`, IPv4-mapped
   `[::ffff:127.0.0.1]`, and expanded `::1` spellings
   (`TestP9HintAddrValidation`).
4. **Constant-time authentication hardening.** `authenticate` hashes both supplied
   and configured tokens with SHA-256 and compares fixed 32-byte digests, so any
   input length goes through a constant-length comparison.

No Store/P8/P4/P5/P6/worker/migration change; `FROZEN_CONTRACT_CHANGES: NONE`.
## 13. Round 2 rework (Issue #91 review)

One startup-lifecycle blocker from the P9 Round 2 review was fixed inside
`internal/runtime/app/**` (tests/docs included); the four Round 1 fixes remain
PASS.

**Startup-error worker join before writer-lock release.** The Round 1
Query-before-Hint rework started the worker before the Query bind, so a Query
bind, `newHintIngester`, `newHintTransport`, or Hint bind failure returned
through `defer cancelWorker()` and the writer-lock release defer **without**
joining the worker — a cancellation-resistant worker could still be alive while
writer ownership was released.

All post-worker-start startup errors now funnel through one `failStartup` helper
that closes any bound Query listener, cancels the worker, and calls
`joinWorker` (which waits for the worker to actually stop) **before** returning,
so the deferred writer-lock release runs only after the started write-capable
actor has stopped. The Query-before-Hint guarantee is preserved (the Query
listener is closed first and Hint is never exposed on a failed startup).

Proof (real PostgreSQL): `TestP9QueryBindFailureWaitsForWorkerBeforeLockRelease`
and `TestP9HintBindFailureWaitsForWorkerBeforeLockRelease` use a
cancellation-resistant worker (exits 400ms after cancellation) plus an occupied
Query / Hint port, and assert that while the worker is still running a second
Store cannot acquire the writer lock, that `runServe` returns only after the
worker stops, and that the lock becomes acquirable only afterwards.

P9 tests = 26 (config 5 / hintapi 9 + boundary 1 + timeout 1 / app 9 / httpapi 1).

`FROZEN_CONTRACT_CHANGES: NONE`