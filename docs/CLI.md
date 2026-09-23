# IndexCore CLI Reference

The runtime is a single binary:

```text
indexcore <command> [flags]
```

Configuration is environment variables plus flags. Flags override environment values.

## Top-level commands

| Command | Purpose |
| --- | --- |
| `migrate` | Apply SQL-first PostgreSQL migrations explicitly |
| `doctor` | Verify PostgreSQL connectivity and schema compatibility |
| `serve` | Run the read-only HTTP API plus the single write-orchestration worker |
| `root` | Root lifecycle, policy, and Collector configuration |
| `scan` | Run one Collector scan for a root |
| `version` | Print build identity |
| `help` | Print top-level help |

## Runtime flags / environment

| Environment | Flag | Default |
| --- | --- | --- |
| `INDEXCORE_DATABASE_URL` | `--database-url` | required |
| `INDEXCORE_HTTP_ADDR` | `--http-addr` | `127.0.0.1:8080` |
| `INDEXCORE_RCLONE_PATH` | `--rclone-path` | `rclone` |
| `INDEXCORE_RCLONE_CONFIG` | `--rclone-config` | empty |
| `INDEXCORE_MAX_CONCURRENT_ROOTS` | `--max-concurrent-roots` | `4` |
| `INDEXCORE_SCAN_TIMEOUT` | `--scan-timeout` | `30m` |
| `INDEXCORE_SHUTDOWN_TIMEOUT` | `--shutdown-timeout` | `15s` |
| `INDEXCORE_LOG_LEVEL` | `--log-level` | `info` |
| `INDEXCORE_LOG_FORMAT` | `--log-format` | `text` |

Valid log levels: `debug|info|warn|error`.

Valid log formats: `text|json`.

## Database

### migrate

```bash
indexcore migrate
```

Migration is explicit. `serve` validates schema compatibility but does not mutate schema.

### doctor

```bash
indexcore doctor
```

Checks PostgreSQL connectivity and schema compatibility.

## Root administration

### create

```bash
indexcore root create \
  --root-id <uuid> \
  --scope '{}' \
  --lifecycle ACTIVE
```

`--lifecycle` may be `NEW` or `ACTIVE`.

A root ID is immutable and must never be reused.

### list

```bash
indexcore root list
indexcore root list --include-deprecated --include-deleted
```

Output is tab-separated with root ID, lifecycle, generation, and owning collector.

### lifecycle

```bash
indexcore root activate  --root-id <uuid>
indexcore root deprecate --root-id <uuid>
indexcore root delete    --root-id <uuid>
```

Lifecycle transitions are constrained by the frozen domain model. Deleted roots are retained as audit/history partitions; deletion does not cascade child resources.

### root policy

Set:

```bash
indexcore root config set \
  --root-id <uuid> \
  --grace 1h \
  --move-horizon 1h \
  --min-consecutive 1 \
  --min-independent 1
```

Read:

```bash
indexcore root config get --root-id <uuid>
```

The removal grace period must remain compatible with the move-recognition horizon and the frozen reconcile policy.

### Collector configuration

Set:

```bash
indexcore root adapter set \
  --root-id <uuid> \
  --collector rclone \
  --config '{"remote":"myremote","path":"/data"}'
```

Read:

```bash
indexcore root adapter get --root-id <uuid>
```

Supported runtime collector kinds:

- `rclone`
- `alist`
- `openlist`

See [COLLECTORS.md](COLLECTORS.md) for configuration shapes and secret handling.

## Scan

```bash
indexcore scan --root <uuid>
```

The command:

1. recovers/drains existing work for that root safely;
2. runs the configured Collector;
3. persists DRAFT Snapshot + entries;
4. submits and admits the Snapshot;
5. lets the Kernel Coordinator process canonical state;
6. returns a JSON outcome.

A Collector source failure exits non-zero while preserving the audit trail.

A Kernel policy rejection is reported separately from a Collector source failure.

## Serve

```bash
indexcore serve
```

`serve` starts:

- the read-only HTTP Query API;
- the background write-orchestration worker;
- single-writer database ownership through a PostgreSQL advisory lock.

A second active writer daemon for the same database fails closed.

Default bind is loopback. There is no built-in authentication in the current Alpha, so keep the service on a trusted/private boundary.

## Build identity

```bash
indexcore version
```

The Makefile injects version, commit, and build date when using `make bin`.
