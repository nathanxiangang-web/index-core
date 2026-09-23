# IndexCore Quick Start

IndexCore is a provider-neutral resource indexing runtime. It turns Collector observations into a trustworthy Canonical Inventory and exposes that truth through a read-only HTTP API.

Current maturity: **Stable Alpha Foundation / Maintenance**. Core development for the accepted Gate 1–4 scope is complete.

## 1. Requirements

For a host build:

- Go 1.27.x
- PostgreSQL 18.x
- rclone available on PATH when using the rclone collector

Docker builds bundle rclone.

## 2. Fastest start: Docker Compose

The repository includes PostgreSQL 18, one-shot migration, and IndexCore:

```bash
docker compose up -d --build
curl -fsS http://127.0.0.1:8080/readyz
```

Expected ready response resembles:

```json
{"status":"ready","schema_applied":4}
```

The supplied Compose file publishes port 8080. IndexCore has **no built-in authentication** in the current Alpha, so do not expose it to an untrusted network.

Stop without deleting PostgreSQL data:

```bash
docker compose down
```

Delete the local database volume as well:

```bash
docker compose down -v
```

## 3. Host build

```bash
make bin
./bin/indexcore version
```

Set a PostgreSQL DSN:

```bash
export INDEXCORE_DATABASE_URL='postgres://indexcore:indexcore@127.0.0.1:5432/indexcore?sslmode=disable'
```

Apply schema explicitly and verify it:

```bash
./bin/indexcore migrate
./bin/indexcore doctor
```

`serve` never auto-migrates.

## 4. Create a root

Use a UUID as the immutable root ID:

```bash
ROOT_ID='11111111-1111-4111-8111-111111111111'

./bin/indexcore root create \
  --root-id "$ROOT_ID" \
  --lifecycle ACTIVE

./bin/indexcore root config set \
  --root-id "$ROOT_ID" \
  --grace 1h \
  --move-horizon 1h \
  --min-consecutive 1 \
  --min-independent 1
```

## 5. Configure a collector

### Local filesystem through rclone

```bash
./bin/indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector rclone \
  --config '{"remote":"","path":"/srv/media"}'
```

The path must be visible to the IndexCore process/container.

### Named rclone remote

```bash
export INDEXCORE_RCLONE_CONFIG='/path/to/rclone.conf'

./bin/indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector rclone \
  --config '{"remote":"myremote","path":"/data"}'
```

AList/OpenList examples are in [COLLECTORS.md](COLLECTORS.md).

## 6. Run one scan

```bash
./bin/indexcore scan --root "$ROOT_ID"
```

A successful command returns JSON containing the root, snapshot, outcome, and generation.

Collector observations always go through:

```text
Collector
  -> DRAFT Snapshot + entries
  -> SUBMITTED + admission
  -> Kernel evaluation
  -> safe reconcile
  -> Canonical Inventory + Journal
```

Collectors never write canonical rows directly.

## 7. Start the runtime

```bash
./bin/indexcore serve
```

Default HTTP bind:

```text
127.0.0.1:8080
```

Useful probes:

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

## 8. Query resources

List roots:

```bash
curl -s http://127.0.0.1:8080/v1/roots
```

Browse root-level resources:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/resources?limit=50"
```

List all active resources in the root:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/active?limit=50"
```

Read journal events:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/journal?after_seq=0&limit=100"
```

The complete API is documented in [HTTP-API.md](HTTP-API.md).

## 9. Where to go next

- CLI reference: [CLI.md](CLI.md)
- Collector configuration: [COLLECTORS.md](COLLECTORS.md)
- HTTP API: [HTTP-API.md](HTTP-API.md)
- Application integration: [INTEGRATION.md](INTEGRATION.md)
- Operations: [OPERATIONS.md](OPERATIONS.md)

For a real server-side Web consumer, see `nathanxiangang-web/indexcore-reference-web`.
