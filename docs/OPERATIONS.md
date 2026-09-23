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

See [.env.example](../.env.example).

## Startup sequence

Recommended:

```bash
indexcore migrate
indexcore doctor
indexcore serve
```

`serve` never auto-migrates and rejects incompatible/missing/future schema state.

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
