# IndexCore Operations

Current mode: **Stable Alpha Foundation / Maintenance**.

IndexCore is a single Go runtime backed by PostgreSQL 18. The public application-facing surface is read-only HTTP.

## Deployment shape

```text
Collectors / scan command
        ↓
IndexCore runtime
        ↓
PostgreSQL 18

Application server
        ↓
read-only /v1
        ↓
IndexCore
```

## Configuration

| Env | Default | Purpose |
| --- | --- | --- |
| `INDEXCORE_DATABASE_URL` | required | PostgreSQL DSN |
| `INDEXCORE_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `INDEXCORE_RCLONE_PATH` | `rclone` | rclone executable |
| `INDEXCORE_RCLONE_CONFIG` | empty | optional rclone config path |
| `INDEXCORE_MAX_CONCURRENT_ROOTS` | `4` | bounded worker concurrency |
| `INDEXCORE_SCAN_TIMEOUT` | `30m` | one Collector scan timeout |
| `INDEXCORE_SHUTDOWN_TIMEOUT` | `15s` | graceful HTTP shutdown window |
| `INDEXCORE_LOG_LEVEL` | `info` | debug/info/warn/error |
| `INDEXCORE_LOG_FORMAT` | `text` | text/json |
| `INDEXCORE_HINT_ADDR` | empty (disabled) | P9 hint listener, literal loopback `127.0.0.1:<port>` / `[::1]:<port>` |
| `INDEXCORE_HINT_TOKEN` | empty | P9 hint bearer token, env-only, `>= 32` bytes when enabled |
| `INDEXCORE_INCREMENTAL_RUNTIME_ENABLED` | `false` | P10 in-process hybrid incremental runtime |
| `INDEXCORE_INCREMENTAL_WAKE_INTERVAL` | `5s` | P10 scheduler wake interval, `1s..60s` when enabled |

See [.env.example](../.env.example).

## Hybrid incremental runtime (P10 prototype)

Disabled by default. When `INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true`, `serve`
hosts one serialized in-process runtime that repeatedly invokes the accepted P6
`RunCycle` under the **same** single-writer advisory lock:

```text
schema compatible
  -> AcquireWriterLock
  -> bounded stale-IN_FLIGHT startup recovery
  -> existing Gate-3 admission worker
  -> P10 hybrid incremental runtime
  -> bind read-only Query listener
  -> optional P9 Hint listener LAST
```

- `INDEXCORE_INCREMENTAL_WAKE_INTERVAL` (1s..60s) is a **scheduler check**
  interval, not provider polling cadence. Actual watch eligibility remains owned
  by persisted `ScopeWatchState.next_due_at` / `effective_interval_seconds` /
  `deferred_until`.
- Only `TRANSIENT_PROVIDER` and `THROTTLED` due `RETRY_WAIT` rows are
  auto-promoted (max 5 per pass). `INTERNAL` / `BLOCKED` / `SUSPENDED` are never
  auto-repaired.
- Startup drains stale `IN_FLIGHT` roots (crash residue) in bounded batches before
  any listener is exposed; a recovery error fails `serve` closed.
- A P6 wall-time interruption with a proven committed claim recovers only that
  root after the call returned; it is never retried inline.
- Backlog continuation is capped at 4 consecutive cycles / 20 item attempts per
  burst, then a 1s cooldown. Hint floods cannot bypass the cooldown.
- An enabled runtime failure is **fatal to `serve`** (no silent degraded mode).
- Shutdown order: drain Hint handlers -> cancel/join P10 runtime -> cancel/join the
  existing worker -> shut down Query -> release the writer lock. The writer lock is
  never released while P10 or the worker may still be running.
- While `serve` owns the writer lock, manual `indexcore incremental run` still
  fails with the writer-lock error (P7 remains mutually exclusive).

## Startup sequence

Recommended:

```bash
indexcore migrate
indexcore doctor
indexcore serve
```

`serve` never auto-migrates and rejects incompatible/missing/future schema state.

## Trusted hint transport (P9 prototype)

The P9 hint transport lets a trusted same-host process deliver a Mutation Hint
into the already-running `indexcore serve` writer **without** turning the public
read-only `/v1` API into a write API and without a second writer process.

```text
POST /internal/v1/mutation-hints
Authorization: Bearer <INDEXCORE_HINT_TOKEN>
Content-Type: application/json

{"root_id":"...","scope_key":"/downloads","reason":"POSSIBLE_CHANGE"}
```

- Disabled unless `INDEXCORE_HINT_ADDR` is set; the address must be a literal
  loopback `127.0.0.1:<port>` or `[::1]:<port>`.
- `INDEXCORE_HINT_TOKEN` is mandatory when enabled, env-only (there is no
  `--hint-token` flag), and must be at least 32 bytes.
- The listener starts only after `serve` owns the single-writer advisory lock and
  binds a separate loopback listener; the public Query listener is unchanged.
- `202 Accepted` means the hint was durably merged into `DirtyScopeWork` only: it
  does **not** mean verification or Canonical mutation happened.
- `DELETE_HINT` is provenance only and is never destructive.
- Bounded ingress: max 4 in-flight calls, 5s ingestion timeout, 4096-byte body;
  `429 busy` (with `Retry-After: 1`) when full, `503 ingest_unavailable` on
  failure. There is no internal queue, retry, or idempotency table.
- Prototype limitation: the hint listener does **not** execute dirty work. With
  `serve` holding the writer lock, `indexcore incremental run` cannot run
  concurrently; stop `serve` first to execute hints manually. Continuous
  in-process execution needs a future separately authorized scheduler phase.

## Health

```text
GET /healthz
```

means the process is alive.

```text
GET /readyz
```

checks:

- PostgreSQL reachability;
- schema compatibility;
- runtime worker readiness.

## Single writer

Only one active write-orchestration daemon is allowed per database.

IndexCore holds a PostgreSQL advisory lock for the process lifetime. A second writer fails closed.

Read-only Query traffic remains conceptually separate from Store mutation capabilities.

## Shutdown / restart

On SIGINT/SIGTERM:

1. HTTP shutdown begins;
2. worker stops accepting new work;
3. IndexCore waits for in-flight orchestration;
4. the writer lock is not released while a worker is still running.

Durable PENDING admissions survive process restart and preserve their admission sequence.

Scanner traversal checkpoint/resume is **not** implemented; durable reconcile/admission recovery is.

## PostgreSQL

PostgreSQL is the current Store implementation and the canonical persisted state.

IndexCore has no custom backup format. Use normal PostgreSQL backup/restore procedures appropriate to your deployment.

Do not let applications write IndexCore tables directly.

## Logs

Logs use `log/slog`.

For machine ingestion:

```bash
export INDEXCORE_LOG_FORMAT=json
export INDEXCORE_LOG_LEVEL=info
```

Sensitive provider credentials should be injected by environment and must not be written into canonical adapter configuration.

## Security

Current Alpha has:

- no built-in authentication;
- no built-in authorization;
- no TLS termination layer.

Safe default is loopback/private service networking.

If a deployment binds non-loopback, IndexCore logs a warning. Use network isolation and an authenticated application/BFF or trusted reverse-proxy boundary as appropriate.

Do not expose the raw `/v1` endpoint directly to the public Internet.

## Scale evidence

Gate 3 validated a real PostgreSQL ingestion baseline at 20,000 resources and exercised initial population, repeat NOOP, small delta, and query pagination.

The benchmark is evidence, not a universal performance SLA.

## Known intentionally deferred areas

These are not defects in the accepted Alpha scope:

- multi-daemon HA / distributed leases;
- scanner traversal checkpoint/resume;
- provider-native delta / true incremental;
- destructive-safe COMPLETE for collectors that cannot prove completeness;
- auth/user/tenant product layer;
- search/catalog;
- previews/download workflows;
- 115 downloader;
- AI/product recommendation features.

## Upgrade discipline

Before upgrading a running environment:

1. back up PostgreSQL;
2. deploy the new binary/image;
3. run `indexcore migrate` explicitly;
4. run `indexcore doctor`;
5. start `indexcore serve`;
6. verify `/readyz`.

Future schema/contract changes should be reviewed as explicit IndexCore changes, not hidden inside a consumer feature.
