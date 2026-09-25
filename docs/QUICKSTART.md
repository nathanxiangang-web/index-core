# IndexCore Quick Start

IndexCore is a provider-neutral resource indexing runtime. It turns Collector
observations into a trustworthy Canonical Inventory and exposes that truth through
a read-only HTTP API.

Current maturity:

```text
Gate 1–4                         ARCHITECT_ACCEPTED
Incremental P0–P11              ARCHITECT_ACCEPTED
P12 Deployment Soak             PLAN ACCEPTED / EXECUTION AUTHORIZED
Hybrid incremental runtime      default disabled
Gate 5                          NOT AUTHORIZED
```

For the current system picture, read [ARCHITECTURE.md](ARCHITECTURE.md).

## 1. Requirements

For a host build:

- Go 1.27.x;
- PostgreSQL 18.x;
- rclone on PATH when using the rclone collector.

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

The supplied Compose file publishes port 8080 for local testing.

IndexCore has no built-in application authentication in the current Alpha. Do
not expose it directly to an untrusted network.

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

Set PostgreSQL:

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

## 5. Configure a Collector

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

### AList/OpenList

AList/OpenList configuration and secret-reference examples are in
[COLLECTORS.md](COLLECTORS.md).

Use environment-variable references for provider secrets; do not persist plaintext
provider credentials in root adapter JSON.

## 6. Run one full scan

```bash
./bin/indexcore scan --root "$ROOT_ID"
```

A successful command returns JSON containing the root, snapshot, outcome, and
generation.

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

A normal full scan is distinct from the accepted scoped incremental path.

## 7. Start the runtime

```bash
./bin/indexcore serve
```

Default Query bind:

```text
127.0.0.1:8080
```

Useful probes:

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

`/healthz` is liveness.

`/readyz` represents DB/schema/runtime readiness and becomes 503 during
shutdown/fatal drain before the process necessarily exits.

## 8. Optional accepted hybrid incremental runtime

The repository default is disabled:

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false
```

To enable it explicitly:

```bash
export INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true
export INDEXCORE_INCREMENTAL_WAKE_INTERVAL=5s
```

The runtime:

- runs inside `indexcore serve`;
- uses the same PostgreSQL writer advisory lock;
- serializes P6 cycles;
- does not create a second daemon;
- keeps Query Q1–Q9 read-only.

Current P0 scoped refresh execution supports AList/OpenList only.

Do not treat rclone full scans as scoped incremental execution.

### Optional trusted Hint listener

```bash
export INDEXCORE_HINT_ADDR='127.0.0.1:8090'
export INDEXCORE_HINT_TOKEN='replace-with-at-least-32-random-bytes'
```

The Hint listener is separate from `/v1`, exact-loopback-only, and disabled
unless configured.

Example:

```bash
curl -i \
  -H "Authorization: Bearer $INDEXCORE_HINT_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"root_id":"'"$ROOT_ID"'","scope_key":"/","reason":"POSSIBLE_CHANGE"}' \
  http://127.0.0.1:8090/internal/v1/mutation-hints
```

A 202 response means durable Hint acceptance only. It does not mean the provider
refresh or Canonical update has already completed.

Ordinary products and the Reference Web should not call this endpoint.

See [HTTP-API.md](HTTP-API.md) and [OPERATIONS.md](OPERATIONS.md).

## 9. Query resources

List roots:

```bash
curl -s http://127.0.0.1:8080/v1/roots
```

Browse hierarchy root-level resources:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/resources?limit=50"
```

List whole-root active resources:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/active?limit=50"
```

Resolve a path:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/resolve?path=/docs/report.txt"
```

Read Journal events:

```bash
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/journal?after_seq=0&limit=100"
```

The complete Query contract is documented in [HTTP-API.md](HTTP-API.md).

## 10. Connect a Web/BFF

Recommended boundary:

```text
Browser
  -> your application server / BFF
  -> IndexCore read-only /v1
```

Do not expose the private IndexCore origin as browser configuration.

A complete accepted consumer example is:

`nathanxiangang-web/indexcore-reference-web`

It uses:

```bash
INDEXCORE_BASE_URL=http://127.0.0.1:8080
```

server-side only.

See [INTEGRATION.md](INTEGRATION.md).

## 11. Manual incremental diagnostic

A one-shot manual bounded cycle is available:

```bash
./bin/indexcore incremental run
```

It requires the same writer lock as `serve`.

If `serve` is active on the same database, the manual command fails closed.

See [CLI.md](CLI.md).

## 12. Where to go next

- Current architecture: [ARCHITECTURE.md](ARCHITECTURE.md)
- CLI reference: [CLI.md](CLI.md)
- Collector configuration: [COLLECTORS.md](COLLECTORS.md)
- HTTP API: [HTTP-API.md](HTTP-API.md)
- Application integration: [INTEGRATION.md](INTEGRATION.md)
- Operations: [OPERATIONS.md](OPERATIONS.md)
- P12 soak plan: [architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md](architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md)
