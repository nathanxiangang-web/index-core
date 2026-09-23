# Gate 3 — IndexCore Alpha Deployment

Single `indexcore` binary + PostgreSQL 18. rclone is an **external runtime
dependency** (never linked into the Kernel). CloudSite integration and UI are out
of scope (Gate 4+).

## Build

```bash
make bin                 # -> bin/indexcore (ldflags build identity)
make docker-build        # -> indexcore:alpha (bundles rclone 1.75.1)
```

## Configure (env + flags)

| Env | Flag | Default | Notes |
|-----|------|---------|-------|
| `INDEXCORE_DATABASE_URL` | `--database-url` | — (required) | PostgreSQL DSN; secrets stay out of canonical rows |
| `INDEXCORE_HTTP_ADDR` | `--http-addr` | `127.0.0.1:8080` | loopback by default; non-loopback is explicit and warns |
| `INDEXCORE_RCLONE_PATH` | `--rclone-path` | `rclone` | external binary path |
| `INDEXCORE_RCLONE_CONFIG` | `--rclone-config` | — | optional rclone config file |
| `INDEXCORE_MAX_CONCURRENT_ROOTS` | `--max-concurrent-roots` | `4` | bounded per-root worker concurrency |
| `INDEXCORE_SCAN_TIMEOUT` | `--scan-timeout` | `30m` | per-scan timeout |
| `INDEXCORE_SHUTDOWN_TIMEOUT` | `--shutdown-timeout` | `15s` | graceful shutdown |
| `INDEXCORE_LOG_LEVEL` | `--log-level` | `info` | debug\|info\|warn\|error |
| `INDEXCORE_LOG_FORMAT` | `--log-format` | `text` | text\|json |

Invalid configuration fails before any background work starts.

## Migrate and run

```bash
export INDEXCORE_DATABASE_URL='postgres://indexcore:indexcore@localhost:5432/indexcore?sslmode=disable'
indexcore migrate                 # explicit; serve never auto-migrates
indexcore doctor                  # connectivity + schema check
indexcore root create --root-id <uuid> --lifecycle ACTIVE
indexcore root config set --root-id <uuid> --grace 1h --move-horizon 1h
indexcore root adapter set --root-id <uuid> --collector rclone \
    --config '{"remote":"myremote","path":"/data"}'
indexcore scan --root <uuid>      # one scan: DRAFT -> SUBMITTED -> Coordinator
indexcore serve                   # read-only /v1 + single write daemon
```

Health: `GET /healthz` (process alive) and `GET /readyz` (DB reachable + schema
compatible + runtime ready). Read API: `GET /v1/...` (no mutation endpoints).

## Docker Compose (PostgreSQL 18 + IndexCore)

```bash
make compose-up      # builds image, starts postgres:18 + indexcore
docker compose exec indexcore indexcore migrate
# bind is 0.0.0.0:8080 inside the container for reachability; no auth in Gate 3
make compose-down
```

The PostgreSQL data lives in the named volume `pgdata`; stopping the stack does
not lose canonical data. Restart with `make compose-up` (or `docker compose up -d`).

## Restart / shutdown

- `indexcore serve` handles SIGINT/SIGTERM: the HTTP transport drains within
  `--shutdown-timeout`; the worker stops accepting new roots and waits for
  in-flight admissions.
- Durable `PENDING` admissions survive a restart; the worker resumes the **same**
  `admission_seq` (never renumbers) via `AdmitOrResumeSnapshot` + `ProcessHead`.

## rclone

- Provided by the image (`/usr/local/bin/rclone`, v1.75.1) or the host PATH.
- rclone remains **additive-safe**: skip evidence is UNKNOWN, failure visibility
  defaults to WEAK, so a successful scan never yields destructive-safe COMPLETE.
- Provider credentials stay in rclone's own config, never in IndexCore canonical data.