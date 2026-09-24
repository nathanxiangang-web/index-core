# Incremental P9 — Trusted Hint Transport Prototype

> Status: **ARCHITECT AUTHORIZED — BOUNDED IMPLEMENTATION PROTOTYPE AFTER PLAN MERGE**
>
> Parent: Issue #57
>
> Predecessor: P8 Mutation Hint Ingestion Prototype — Issue #88 / PR #89 — **ARCHITECT_ACCEPTED**
>
> P8 merge: `ef93ed94072314213d1ef0f64005ba7c0d3c4859`
>
> P8 exit decision: **AUTHORIZE_TRUSTED_HINT_TRANSPORT_DESIGN**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P8 proved a trusted in-process Mutation Hint can be durably merged through the
existing DirtyScopeWork state machine without provider traversal or a second
execution lane.

P9 answers one transport/topology question:

> How can a trusted external process on the same host deliver one mutation hint
> into the already-active single-writer `indexcore serve` process without
> turning the existing read-only Query API into a write API and without creating
> a second writer process?

Selected prototype shape:

```text
trusted same-host caller
        ↓
loopback-only dedicated Hint HTTP listener
        ↓
Bearer-token authentication
        ↓
bounded strict JSON handler
        ↓
P8 MutationHintService.IngestOne
        ↓
existing Store.MergeSignal
        ↓
DirtyScopeWork
```

The Hint listener runs **inside the same `indexcore serve` process that already
owns the database writer advisory lock**.

P9 does not execute the resulting dirty work.

## 2. Preserve the frozen read-only Query transport

The existing:

```text
internal/transport/httpapi
```

remains read-only.

Its frozen dependency boundary remains:

```text
Query reader
Readiness probe
Logger/version/readiness callback
NO Store mutation capability
```

P9 must not add Mutation Hint write capability to `httpapi.Deps`.

P9 must not add POST/PUT/PATCH/DELETE write routes to the existing application
Query listener.

The existing `/v1` HTTP API remains the same public/read-only contract.

## 3. Dedicated Hint listener

Preferred new package:

```text
internal/transport/hintapi/
```

The Hint listener is:

- a separate `net/http` server/listener;
- inside the same `indexcore serve` process;
- created only after the process owns the existing writer advisory lock;
- disabled by default;
- loopback-only in P9;
- authenticated even on loopback;
- connected only to the accepted P8 ingress interface.

Preferred dependency shape:

```go
type Ingester interface {
    IngestOne(context.Context, incrementalhint.Request) (state.DirtyScopeWork, error)
}

type Deps struct {
    Ingester Ingester
    Token    string
    Logger   *slog.Logger
}
```

The Hint transport must not receive:

- `*postgres.Store`;
- `*pgxpool.Pool`;
- generic SQL/Exec capability;
- ScanScope;
- P4/P5/P6 executor/orchestrator;
- Canonical/Kernel write interfaces.

It receives only the narrow P8 `Ingester`.

## 4. Configuration

Add runtime configuration:

```text
INDEXCORE_HINT_ADDR
INDEXCORE_HINT_TOKEN
```

Preferred Config fields:

```go
HintAddr  string `json:"hint_addr,omitempty"`
HintToken string `json:"-"`
```

### 4.1 Default

```text
HintAddr  = ""
HintToken = ""
```

Empty HintAddr means:

```text
Hint transport DISABLED
```

Existing deployments therefore remain unchanged.

### 4.2 Address

`INDEXCORE_HINT_ADDR` / `--hint-addr` may enable the listener.

P9 requires a literal loopback IP host:

```text
127.0.0.1:<port>
[::1]:<port>
```

Reject:

```text
0.0.0.0:...
[::]:...
non-loopback IP
hostname-based binding
missing/invalid port
port 0
```

Do not silently rewrite a non-loopback address to loopback.

The existing Query `HTTPAddr` behavior is unchanged.

### 4.3 Token

If HintAddr is enabled, `INDEXCORE_HINT_TOKEN` is mandatory.

Requirements:

- at least 32 bytes;
- env-only secret in P9;
- **no `--hint-token` CLI flag**;
- never serialized;
- never logged;
- never included in error messages or responses.

If HintAddr is empty, HintToken may be ignored by commands that do not start the
transport.

## 5. Endpoint

Exactly one P9 write endpoint:

```http
POST /internal/v1/mutation-hints
```

No batch endpoint.

No GET listing/history endpoint.

No delete/update endpoint.

No endpoint exists on the normal Query listener.

## 6. Authentication

Require:

```http
Authorization: Bearer <INDEXCORE_HINT_TOKEN>
```

Rules:

- missing Authorization -> `401 unauthorized`;
- malformed Bearer scheme -> `401 unauthorized`;
- wrong token -> `401 unauthorized`;
- all three use the same stable response shape;
- set `WWW-Authenticate: Bearer`;
- comparison uses constant-time comparison;
- unauthenticated requests never reach P8;
- token value is never logged.

Loopback binding is a network boundary; bearer auth is an additional trust
boundary.

P9 does not authorize network-reachable token auth.

## 7. Request contract

Required media type:

```text
application/json
```

Allow standard media-type parameters such as `charset=utf-8`.

Body limit:

```text
4096 bytes
```

Reject larger bodies with `413 request_too_large`.

Use strict decoding:

- unknown fields rejected;
- malformed JSON rejected;
- multiple/trailing JSON values rejected;
- one request object only.

Request body:

```json
{
  "root_id": "11111111-1111-4111-8111-111111111111",
  "scope_key": "/downloads",
  "reason": "POSSIBLE_CHANGE"
}
```

`reason` may be omitted/empty and then follows P8 defaulting to
`POSSIBLE_CHANGE`.

Transport-level validation mirrors the P8 request contract for a stable
`400 invalid_request` response:

- root_id must be non-empty;
- scope_key must pass existing `state.ValidateScopeKey`;
- reason must be empty or one of:
  - POSSIBLE_CHANGE
  - DELETE_HINT
  - MOVE_UNCERTAIN
  - METADATA_UNCERTAIN

P8 remains authoritative and validates again.

Do not call `path.Clean`.

Do not reinterpret a file path as a parent scope.

## 8. Success contract

P8 success returns:

```http
202 Accepted
Content-Type: application/json
```

Preferred response:

```json
{
  "status": "accepted",
  "root_id": "...",
  "scope_key": "/downloads",
  "work_state": "PENDING",
  "signal_seq": 12
}
```

Important:

> `202 Accepted` means the Mutation Hint was durably merged into
> DirtyScopeWork. It does **not** mean provider verification has run and does
> **not** mean Canonical state changed.

P9 does not call P4/P5/P6.

## 9. Error contract

Stable transport-level responses:

```text
400 invalid_request          invalid JSON/request/scope/reason
401 unauthorized             missing/malformed/wrong Bearer token
413 request_too_large        body >4096 bytes
415 unsupported_media_type   non-JSON body
429 busy                     bounded ingress concurrency is full
503 ingest_unavailable       P8/Store failure or P9-owned ingress timeout
```

Do not return raw Store/P8 error strings to the caller.

Do not leak PostgreSQL details.

Do not leak token/configuration.

For `429`, set:

```http
Retry-After: 1
```

For P9, root-not-found is not a separately frozen HTTP status. If it reaches the
P8/Store layer it is returned as the same opaque `503 ingest_unavailable`.
Typed external rejection can be designed later without coupling Hint transport
to PostgreSQL sentinels.

## 10. Bounded concurrency / backpressure

P9 hard prototype limit:

```text
max in-flight authenticated ingestion calls = 4
```

Rules:

- fixed hard cap in P9;
- no unbounded goroutine fan-out;
- no internal queue;
- no background retry queue;
- when all four slots are occupied, a new authenticated valid request returns
  `429 busy` immediately;
- no request waits in an application-level queue for a slot.

This is ingress backpressure, not provider rate limiting.

No token-bucket/global RPS algorithm is added in P9.

## 11. Request timeout / cancellation

P9 hard prototype ingestion timeout:

```text
5 seconds per P8 IngestOne call
```

The handler derives a child context from the HTTP request context.

No server-side retry.

If the timeout expires:

- return `503 ingest_unavailable` when a response can still be written;
- do not call P8 again;
- do not inspect/repair DirtyScopeWork.

A DB commit racing client disconnect/timeout can make the caller uncertain
whether the signal committed.

This is acceptable because P8 is at-least-once safe:

```text
caller retries
  -> same Work row
  -> signal_seq may increment again
  -> provenance coalesces
```

P9 does not claim exactly-once delivery.

## 12. Replay / idempotency

P9 does **not** add:

- request IDs;
- idempotency keys;
- receipt/history table;
- dedup cache.

Two authenticated HTTP requests are two accepted hint attempts.

If both reach P8 successfully:

```text
signal_seq increments twice
```

while still coalescing into the same Work row.

This behavior is explicitly documented.

## 13. Serve topology

Accepted topology:

```text
indexcore serve
    ↓
schema compatible
    ↓
AcquireWriterLock
    ↓
construct Store
    ├── existing Gate-3 admission worker
    ├── existing read-only Query HTTP server
    └── optional P9 Hint HTTP server
             ↓
         P8 IngestOne
```

The Hint listener must never start before writer ownership is acquired.

If writer-lock acquisition fails:

- `serve` fails closed;
- Hint listener is not bound.

If HintAddr is configured but its listener cannot bind:

- `serve` fails;
- worker/query transport are shut down if already started;
- writer lock remains held until all started write-capable work is stopped.

If the Hint server exits unexpectedly after startup:

- treat it as a fatal `serve` component failure;
- stop the other runtime components;
- do not silently continue without hint ingestion.

## 14. Shutdown safety

The writer lock must never be released while a Hint handler may still call P8.

P9 Hint server therefore tracks in-flight ingestion handlers.

Shutdown order must preserve:

```text
stop accepting new Hint requests
        ↓
cancel/shutdown transport
        ↓
wait for all in-flight Hint handlers to finish
        ↓
stop/join existing worker
        ↓
only then allow writer-lock release
```

The normal read-only Query server may shut down in the same overall sequence but
does not affect write ownership.

If graceful shutdown timeout expires while a Hint handler is still active:

- log the timeout;
- keep holding the writer lock;
- continue waiting for the bounded Hint handler to return;
- never release the writer lock early.

This mirrors the accepted worker shutdown safety principle.

## 15. Query transport remains independent

The P9 implementation must preserve:

```text
TestDepsExposeNoWriteCapability
```

for `httpapi.Deps`.

Add an explicit regression proving:

- `POST /internal/v1/mutation-hints` on the Query listener is not a supported route;
- Query listener receives no P8 Ingester/write capability;
- Hint listener does not expose Q1–Q9 routes.

The two surfaces are deliberately separate.

## 16. Hint transport dependency boundary

Add a boundary test for `hintapi.Deps`.

It may contain only:

- narrow P8 Ingester;
- logger;
- token/auth configuration required by the transport.

It must not contain types/capabilities matching:

```text
postgres.Store
pgxpool.Pool
pgx.Conn
database/sql
scan.Service
incrementalexec.Executor
incrementalorch.Runner
Kernel Coordinator
```

P9 transport owns no direct persistence/execution capability.

## 17. No automatic incremental executor

P9 does not connect Hint ingress to P5/P6 automatically.

After `202 Accepted`, the dirty work may remain PENDING until a separately
authorized actor executes it.

Current prototype reality:

- P7 manual `indexcore incremental run` can execute dirty work only when it
  owns the writer lock;
- therefore a deployment using P9 Hint listener under `serve` must stop
  `serve` before using P7 manual execution;
- a future production scheduler/hybrid runtime phase is required for continuous
  in-process execution.

P9 must document this limitation rather than secretly adding a scheduler.

## 18. Security

P9 prototype security model:

```text
loopback-only bind
+
required high-entropy Bearer token
+
strict body limit/JSON parsing
+
bounded in-flight concurrency
```

P9 explicitly does not provide:

- TLS termination;
- mTLS;
- user/account auth;
- browser auth;
- public Internet exposure;
- tenant authorization;
- root-level ACL policy.

Do not use P9 as a public application API.

## 19. Logging

Allowed structured log fields:

- event/class;
- root_id;
- scope_key;
- status/error_class such as auth/busy/ingest.

Never log:

- Authorization header;
- HintToken;
- full runtime config;
- database URL;
- provider credentials.

Unauthorized failures should not echo supplied token material.

## 20. Expected implementation surface

Expected production changes:

```text
internal/transport/hintapi/**
internal/runtime/config/config.go
internal/runtime/app/app.go
.env.example
docs/HTTP-API.md
docs/OPERATIONS.md
```

Result report:

```text
docs/incremental/P9-TRUSTED-HINT-TRANSPORT-RESULT.md
```

Tests may update/add:

```text
internal/transport/hintapi/*_test.go
internal/runtime/config/*_test.go
internal/runtime/app/*_test.go
internal/transport/httpapi/boundary_test.go  (regression only if useful)
```

Not expected / not authorized production changes:

```text
internal/transport/httpapi/**
internal/runtime/incrementalhint/**
internal/store/postgres/**
internal/incremental/state/**
internal/runtime/incrementalexec/**
internal/runtime/incrementalorch/**
internal/runtime/scan/**
internal/runtime/worker/**
internal/kernel/**
internal/query/**
internal/domain/**
internal/collector/**
internal/store/postgres/migrations/**
cmd/**  (no new command)
```

If implementation requires changes to these production areas, stop for Architect
review.

## 21. Required tests

### 21.1 Config defaults

Prove:

- HintAddr empty by default;
- Hint transport therefore disabled by default;
- existing serve behavior remains unchanged.

### 21.2 Config validation

Prove enabled transport rejects:

- no token;
- token shorter than 32 bytes;
- non-loopback bind;
- wildcard bind;
- hostname bind;
- missing/invalid/zero port.

Accept literal:

- `127.0.0.1:<port>`;
- `[::1]:<port>`.

Prove no `--hint-token` flag exists.

### 21.3 Authentication

Fake Ingester.

Prove missing/malformed/wrong token:

- 401;
- same public error shape;
- Ingester call count zero;
- response never includes configured/supplied token.

Correct token reaches the request path.

### 21.4 Strict request parsing

Prove:

- non-JSON -> 415;
- body >4096 -> 413;
- malformed JSON -> 400;
- unknown field -> 400;
- trailing second JSON value -> 400;
- malformed scope/reason/root -> 400 before Ingester;
- valid request reaches Ingester exactly once.

### 21.5 Mapping to P8

Fake/real P8 seam.

Prove HTTP request maps only:

```text
root_id
scope_key
reason
```

Caller cannot send source/priority/seen_at/not_before through JSON.

### 21.6 Accepted response

Fake Ingester returns representative Work.

Assert:

- 202;
- status=accepted;
- root_id/scope_key;
- actual work_state;
- signal_seq;
- no Store internals/provenance buckets/secrets.

### 21.7 Ingester error privacy

Fake Ingester returns sentinel containing secret-looking text.

Assert:

- caller gets only stable 503 `ingest_unavailable`;
- raw error text absent;
- token absent.

### 21.8 Concurrency/backpressure

Block four authenticated P8 calls.

Fifth valid authenticated request:

- returns 429 immediately;
- does not call Ingester;
- includes Retry-After.

Release one call and prove a later request may enter.

No queue growth.

### 21.9 Request timeout

Block Ingester past the 5s prototype timeout using a controllable clock/seam if
needed.

Assert:

- request is cancelled;
- no second IngestOne call;
- stable 503;
- no repair/retry behavior.

### 21.10 HTTP replay semantics

Real PostgreSQL + real P8.

Send the same authenticated request twice.

Assert:

- both may return 202;
- one Work row;
- signal_seq increments twice;
- source/reason remain coalesced.

No idempotency claim.

### 21.11 Writer ownership before bind

Real PostgreSQL/app seam.

Hold the existing writer advisory lock from another owner.

Start `serve` with HintAddr enabled.

Assert:

- serve returns writer-lock error;
- Hint address never becomes available;
- no Hint request can reach P8.

### 21.12 Same-process real transport -> P8

Real PostgreSQL + real P8 + real Hint HTTP handler.

Authenticated POST:

```text
HTTP
 -> hintapi
 -> P8 IngestOne
 -> Store.MergeSignal
 -> DirtyScopeWork
```

Assert:

- 202;
- Work persisted;
- no provider request;
- no P4/P5/P6 invocation;
- no Canonical mutation.

### 21.13 Query/Hint surface separation

Prove:

- existing Query Q1–Q9 tests remain green;
- Query `httpapi.Deps` still has no write capability;
- mutation-hint POST is not exposed on Query listener;
- Hint listener does not expose Q1–Q9.

### 21.14 Hint listener bind failure

Configure an already-occupied loopback Hint address.

Assert:

- serve startup/runtime fails closed;
- no silent fallback to disabled hints;
- writer ownership is eventually released only after started write work stops.

### 21.15 Unexpected Hint server failure

Use app/server seam to inject failure.

Assert:

- serve treats it as fatal;
- worker/query runtime shutdown begins;
- no silent degraded mode with Hint transport missing.

### 21.16 Shutdown / in-flight writer safety

Real writer lock + blocking fake P8 Ingester.

Start serve with Hint listener.

Begin authenticated Hint request and block inside IngestOne.

Trigger serve shutdown.

Assert:

- no new Hint requests accepted;
- writer lock cannot be acquired by a second Store while the first Hint handler
  remains active;
- release the handler;
- serve completes shutdown;
- writer lock then becomes acquirable.

This is a hard P9 acceptance gate.

### 21.17 Disabled transport regression

HintAddr empty.

Assert:

- no second listener;
- existing Query/worker serve behavior and shutdown remain unchanged.

### 21.18 No automatic execution

Real PostgreSQL.

POST a valid hint and receive 202.

Without separately invoking P5/P7:

- Work remains in its merged operational state;
- no provider request occurs;
- no Canonical resource appears.

This proves 202 means durable hint acceptance, not verification.

### 21.19 Regression

Required:

```text
gofmt
go vet ./...
go test -p 1 -count=1 ./...
```

Existing P3/P4/P5/P6/P7/P8 tests remain green.

## 22. Acceptance criteria

P9 passes only if:

1. existing Query HTTP remains read-only and unchanged;
2. Hint transport is a separate listener/package;
3. Hint transport is disabled by default;
4. enabled listener accepts only literal loopback binding;
5. strong bearer token is mandatory and env-only;
6. auth failure never reaches P8;
7. request body/JSON parsing is strictly bounded;
8. one valid request calls P8 at most once;
9. accepted response means durable hint merge only;
10. max four ingestion calls are active; no internal queue exists;
11. timeout/cancellation never causes server-side retry;
12. duplicate HTTP requests follow accepted P8 at-least-once/coalescing semantics;
13. Hint server starts only while the process owns the writer lock;
14. active Hint handlers finish before writer lock release;
15. Hint listener failure is fatal when explicitly enabled;
16. transport has no direct Store/Scan/executor/Kernel write capability;
17. no provider I/O occurs at ingress;
18. no automatic incremental scheduler/executor is introduced;
19. no migration/Canonical/Query contract changes occur;
20. no non-loopback/public Hint transport is authorized.

## 23. P9 exit decision

After evidence, Architect selects exactly one:

```text
STOP
AUTHORIZE_PRODUCTION_SCHEDULER_DESIGN
AUTHORIZE_HYBRID_RUNTIME_DESIGN
AUTHORIZE_NETWORK_TRUSTED_HINT_RESEARCH
RESEARCH_FURTHER
```

No exit is pre-authorized.

## 24. Current authorization

```text
P0 scoped refresh                     ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility      ARCHITECT_ACCEPTED
P2 durable scope-state design         ARCHITECT_ACCEPTED
P3 state persistence prototype        ARCHITECT_ACCEPTED
P4 one-shot dirty executor            ARCHITECT_ACCEPTED
P5 bounded executor loop              ARCHITECT_ACCEPTED
P6 scheduler orchestration            ARCHITECT_ACCEPTED
P7 manual incremental command         ARCHITECT_ACCEPTED
P8 mutation hint ingestion            ARCHITECT_ACCEPTED
P9 trusted hint transport             AUTHORIZED AFTER THIS PLAN MERGES

existing Query HTTP write expansion    NOT AUTHORIZED
network-reachable Hint transport       NOT AUTHORIZED
public/unauthenticated Hint API        NOT AUTHORIZED
second writer process                  NOT AUTHORIZED
automatic incremental executor        NOT AUTHORIZED
production polling scheduler           NOT AUTHORIZED
ticker/cadence daemon                  NOT AUTHORIZED
native delta/provider cursor           NOT AUTHORIZED
direct 115 integration                 NOT AUTHORIZED
destructive delta/removal              NOT AUTHORIZED
Gate 5                                 NOT AUTHORIZED
```
