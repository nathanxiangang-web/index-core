# IndexCore

**Provider-neutral canonical resource indexing kernel.**

IndexCore collects resource observations from external sources, evaluates identity/completeness/safety, maintains one trustworthy Canonical Inventory, appends a canonical Change Journal, and exposes the result through a small read-only HTTP Query API.

> **Status:** Core development is complete for the accepted Alpha scope. Gate 1–4 are closed. The project is now a **Stable Alpha Foundation / Maintenance** component. Gate 5 (future product architecture) is not authorized.

IndexCore is **not CloudSite 2**, not a search engine, not a user system, and not a downloader. Product concerns belong in consumers.

## Architecture at a glance

```text
rclone / AList / OpenList / future Collector
                    ↓
            normalized Snapshot
                    ↓
              Index Kernel
      validation / identity / safety
                    ↓
        Canonical Inventory + Journal
                    ↓
             PostgreSQL 18
                    ↓
          read-only HTTP /v1
                    ↓
        application server / BFF
                    ↓
              browser / app
```

Key rule: **one canonical resource truth**. Collectors acquire facts; consumers read truth. Neither may bypass the Kernel and redefine canonical state.

## What is implemented

- provider-neutral Snapshot/Collector boundary;
- stable canonical `resource_id` / `root_id` semantics;
- conservative Safe Reconcile and removal rules;
- PostgreSQL 18 Store with atomic canonical + Journal commits;
- per-root generation and FIFO admission;
- restart-safe durable admissions;
- append-only canonical Change Journal;
- rclone Collector;
- AList/OpenList Collector;
- standalone `indexcore` binary;
- explicit SQL migrations;
- single-writer runtime ownership;
- read-only Q1–Q9 HTTP API;
- 20k real-PostgreSQL scale validation;
- independent Reference Web integration validation;
- trusted loopback Mutation Hint ingestion;
- optional same-process/same-writer hybrid incremental runtime (default disabled).

## Quick start

### Docker Compose

```bash
docker compose up -d --build
curl -fsS http://127.0.0.1:8080/readyz
```

The current Alpha has **no built-in authentication**. Keep IndexCore on loopback/private service networking. The supplied Compose file publishes port 8080 for local testing; do not expose it directly to an untrusted network.

### Host binary

```bash
make bin
export INDEXCORE_DATABASE_URL='postgres://indexcore:indexcore@127.0.0.1:5432/indexcore?sslmode=disable'

./bin/indexcore migrate
./bin/indexcore doctor
./bin/indexcore serve
```

Full setup: [docs/QUICKSTART.md](docs/QUICKSTART.md)

## Configure a root and scan it

```bash
ROOT_ID='11111111-1111-4111-8111-111111111111'

./bin/indexcore root create --root-id "$ROOT_ID" --lifecycle ACTIVE

./bin/indexcore root config set \
  --root-id "$ROOT_ID" \
  --grace 1h \
  --move-horizon 1h

./bin/indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector rclone \
  --config '{"remote":"","path":"/srv/media"}'

./bin/indexcore scan --root "$ROOT_ID"
```

Collector documentation: [docs/COLLECTORS.md](docs/COLLECTORS.md)

## Run one manual incremental cycle

```bash
./bin/indexcore incremental run
```

`incremental run` itself is a one-shot/manual diagnostic path: it calls the
accepted P6 orchestration exactly once under the same single-writer advisory lock
as `serve` (an active `serve` writer makes it fail closed) and prints one JSON
result to stdout.

Separately, `serve` can host the accepted P10 hybrid incremental runtime when
`INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true`. That runtime is default disabled,
serialized, bounded, and uses the same writer lock; it does not create a second
daemon/writer. See [docs/CLI.md](docs/CLI.md) and
[docs/OPERATIONS.md](docs/OPERATIONS.md).

## Read the API

```bash
curl -s http://127.0.0.1:8080/v1/roots
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/resources?limit=50"
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/active?limit=50"
curl -s "http://127.0.0.1:8080/v1/roots/$ROOT_ID/journal?after_seq=0&limit=100"
```

The HTTP surface is read-only. Root administration and scans are CLI/runtime operations.

API reference: [docs/HTTP-API.md](docs/HTTP-API.md)

## Supported Collectors

| Collector | Runtime kind | Safety posture |
| --- | --- | --- |
| rclone | `rclone` | additive-safe by default |
| AList | `alist` | additive-safe by default |
| OpenList | `openlist` | additive-safe by default |

Ordinary rclone/AList/OpenList scans do not have enough positive completeness evidence to authorize destructive removal. **Missing is not deleted.**

## Integrating an application

Recommended boundary:

```text
Browser
   ↓
your Web/backend/BFF
   ↓ server-side HTTP
IndexCore /v1
```

Do not connect a product directly to IndexCore PostgreSQL and do not expose raw IndexCore publicly just to make browser calls easier.

Integration guide: [docs/INTEGRATION.md](docs/INTEGRATION.md)

Reference consumer: `nathanxiangang-web/indexcore-reference-web`

Gate 4 proved that a brand-new Web can consume Q1–Q9 through server-side HTTP with zero direct PostgreSQL, IndexCore Go, provider, or CloudSite coupling.

## Documentation

Start at [docs/README.md](docs/README.md).

Current usage docs:

- [Quick Start](docs/QUICKSTART.md)
- [CLI Reference](docs/CLI.md)
- [HTTP API](docs/HTTP-API.md)
- [Collectors](docs/COLLECTORS.md)
- [Integration Guide](docs/INTEGRATION.md)
- [Operations](docs/OPERATIONS.md)

Architecture and evidence:

- [PROJECT-CONTEXT.md](PROJECT-CONTEXT.md)
- [PROJECT-STATE.md](PROJECT-STATE.md)
- [ARCHITECTURE-INVARIANTS.md](ARCHITECTURE-INVARIANTS.md)
- [NEXT-ACTIONS.md](NEXT-ACTIONS.md)
- [docs/architecture/](docs/architecture/)
- [docs/decisions/](docs/decisions/)
- [Gate 4 final consumer report](docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md)

## Project boundary

IndexCore should remain small. Before adding functionality, ask whether it belongs in:

- a Collector;
- a storage/provider adapter;
- a consumer/application;
- an external mature tool.

Only canonical truth, safety, identity, reconcile, Journal, and the minimal read contract belong in the Kernel by default.

**Rule:** chat history is cache; Git is project memory.
