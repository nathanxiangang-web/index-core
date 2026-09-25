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
| `incremental` | Run one bounded manual incremental orchestration cycle |
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

## Incremental manual cycle

```bash
indexcore incremental run [flags]
```

One-shot/manual only. `indexcore incremental` without a subcommand, and any
unknown or reserved subcommand (`watch`, `daemon`, ...), fail. There is no
`watch`, `daemon`, `recover`, or `hint` behavior and no second binary.

`incremental run` accepts flags only: any positional argument fails closed before
database/writer/provider work, so a stray token cannot stop flag parsing and let
an invalid budget slip through to run with defaults.

The command reuses the accepted Scan -> P4 -> P5 -> P6 chain and calls the P6
orchestration **exactly once**. It requires the same PostgreSQL single-writer
advisory lock as `serve`, so an active `indexcore serve` writer on the same
database makes it fail closed (exit 1, no provider work).

Command-local flags (defaults):

| Flag | Default | Bound |
| --- | --- | --- |
| `--max-due-watch-attempts` | `5` | `1..5` |
| `--max-execute-items` | `5` | `1..5` |
| `--max-wall-time` | `60s` | `>0..60s` |
| `--max-entries-per-scope` | `1000` | `0..10000` |
| `--retry-transient-provider` | `30s` | `>0` |
| `--retry-throttled` | `45s` | `>0` |
| `--retry-internal` | `60s` | `>0` |

These are command-local prototype safety budgets, not production cadence/SLA.
They are not added to persistent/global config or environment variables.
Existing runtime flags/env (`--database-url`, `--rclone-path`, `--rclone-config`,
`--scan-timeout`, `--shutdown-timeout`, logging) are reused.

Schema is preflighted: the command never auto-migrates and fails closed on a
missing/future/incompatible schema.

After the single P6 cycle it writes exactly one snake_case JSON object to stdout:

```json
{
  "command": "incremental run",
  "stop_reason": "COMPLETED",
  "started_at": "...", "finished_at": "...", "observed_at": "...",
  "due": { "candidates": 1, "attempted": 1, "emitted": 1, "stale": 0,
           "more_due_watches": false, "materialization_interrupted": false },
  "executor": { "ran": true, "stop_reason": "NO_ELIGIBLE_WORK",
                "invocations": 1, "selected_items": 1, "succeeded": 1,
                "failed": 0, "interrupted_in_flight": false, "last": {} }
}
```

Logs and errors go to stderr. The JSON never contains the database DSN, provider
credentials, adapter config, or secret environment values.

Delivering this JSON is part of command success: a serialization or stdout write
failure is a command error (exit 1), and if P6 also failed both errors are
preserved in the returned error chain.

Exit codes reuse the existing process contract: `0` when the command returns nil
(including a normal bounded P6 `MAX_WALL_TIME`), `1` on any error (parent
cancellation, materialization/executor error, writer-lock conflict, invalid
config/schema, unexpected positional argument, or stdout delivery failure). No
new exit-code classes are introduced.

The `incremental run` command itself performs no background scheduler/ticker or
daemon behavior and does not attach to a running `serve`. The separate accepted
P10 hybrid runtime may run inside `serve` when explicitly enabled; because both
use the same writer advisory lock, the manual command fails closed while `serve`
is active.

## Build identity

```bash
indexcore version
```

The Makefile injects version, commit, and build date when using `make bin`.
