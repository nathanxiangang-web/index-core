# IndexCore Current Architecture

> Current status: **Stable Alpha Foundation / Incremental Hardening**
>
> Gate 1–4: **CLOSED / ARCHITECT_ACCEPTED**
>
> Incremental P0–P11: **ARCHITECT_ACCEPTED**
>
> P12 Deployment Soak with Reference Consumer: **PLAN ACCEPTED / EXECUTION AUTHORIZED**
>
> Incremental runtime repository default: **disabled**
>
> Gate 5: **NOT AUTHORIZED**

This document is the current architecture entry point for operators, integrators,
and future maintainers.

Historical design contracts and Gate evidence remain under
[`docs/architecture/`](architecture/), but this file describes the system as it
exists now.

---

## 1. What IndexCore is

IndexCore is a provider-neutral canonical resource indexing service.

Its responsibilities are intentionally narrow:

- collect external resource observations;
- normalize them into Snapshot evidence;
- evaluate identity, completeness, and safety;
- maintain one Canonical Inventory;
- append a per-root canonical Change Journal;
- expose that canonical truth through a small read-only HTTP Query API.

It is **not** a full application backend.

It does not own:

- product users/auth/sessions;
- search UX;
- catalog presentation;
- favorites/history;
- media playback;
- download gateways;
- sharing;
- AI/recommendations;
- product-specific business state.

Those belong in consumers/applications unless a later architecture decision
explicitly moves a responsibility into IndexCore.

---

## 2. High-level topology

```text
External provider
(rclone / AList / OpenList)
        │
        ▼
Collector / scoped refresh
        │ normalized evidence
        ▼
Snapshot
(DRAFT -> SUBMITTED -> admitted)
        │
        ▼
Kernel / reconcile
(identity / completeness / safety)
        │
        ├──────────────► Change Journal
        │
        ▼
Canonical Inventory
        │
        ▼
PostgreSQL 18
        │
        ▼
read-only Query /v1
        │
        ▼
application server / BFF
        │
        ▼
browser / app
```

The canonical rule is:

> Collectors acquire facts. The Kernel decides canonical truth. Consumers read
> canonical truth.

No Collector or consumer may bypass the Kernel and redefine canonical state.

---

## 3. Three interface planes

IndexCore has three distinct operational/interface planes.

### 3.1 Query Plane — application-facing, read-only

Default listener:

```text
127.0.0.1:8080
```

Endpoints:

```text
GET /healthz
GET /readyz

GET /v1/roots
GET /v1/roots/{root_id}
GET /v1/roots/{root_id}/status
GET /v1/roots/{root_id}/resources
GET /v1/roots/{root_id}/active
GET /v1/roots/{root_id}/removed
GET /v1/roots/{root_id}/resolve
GET /v1/roots/{root_id}/journal
GET /v1/resources/{resource_id}
```

This is the only plane ordinary applications should consume.

The public Query transport holds only read capabilities. It does not receive a
PostgreSQL Store mutation capability, provider credential, Kernel mutation
capability, or Hint ingester.

See [HTTP-API.md](HTTP-API.md).

### 3.2 Administration Plane — CLI/runtime only

Administrative/write-orchestration operations remain CLI/runtime operations:

```text
indexcore migrate
indexcore doctor
indexcore root ...
indexcore scan --root ...
indexcore incremental run
indexcore serve
```

These operations are not browser APIs.

Examples:

- root lifecycle/configuration;
- Collector binding;
- explicit migration;
- one full scan;
- one manual bounded incremental cycle.

See [CLI.md](CLI.md).

### 3.3 Trusted Hint Plane — internal loopback-only

Optional endpoint:

```text
POST /internal/v1/mutation-hints
```

This endpoint is **not part of the Query API**.

It is exposed on a separate listener configured by:

```text
INDEXCORE_HINT_ADDR=127.0.0.1:<port>
INDEXCORE_HINT_TOKEN=<secret, at least 32 bytes>
```

Hard boundary:

- literal `127.0.0.1` or `::1` only;
- bearer authenticated;
- disabled when `INDEXCORE_HINT_ADDR` is empty;
- no public/network Hint exposure;
- no Store/SQL/Kernel capability in the transport;
- one request maps only to the accepted durable P8 Hint ingestion seam.

A successful `202 Accepted` means:

> the Hint was durably merged into incremental work state.

It does **not** mean:

- provider refresh finished;
- Canonical Inventory already changed;
- the consumer can already see the new resource.

See [HTTP-API.md](HTTP-API.md#trusted-internal-hint-transport) and
[OPERATIONS.md](OPERATIONS.md).

---

## 4. Runtime ownership and the single-writer rule

IndexCore allows one active write-orchestration owner per database.

```text
indexcore serve
        │
        ▼
PostgreSQL advisory writer lock
```

While `serve` owns the lock:

- the existing admission worker may write;
- the optional hybrid incremental runtime may write through accepted P6/P5/P4/P0;
- trusted Hint handlers may durably merge P8 state;
- Query remains read-only.

A second active writer fails closed.

`indexcore incremental run` uses the same writer ownership rule and therefore
cannot run concurrently with an active `serve` on the same database.

This is intentional.

---

## 5. Normal full-scan path

A normal scan uses the configured Collector:

```text
indexcore scan --root <root_id>
        │
        ▼
Collector.Scan
        │
        ▼
DRAFT Snapshot
        │
        ▼
SubmitAndAdmitSnapshot
        │
        ▼
Kernel Coordinator
        │
        ▼
Canonical Inventory + Journal
```

Supported runtime Collector kinds:

- `rclone`;
- `alist`;
- `openlist`.

Ordinary scans are additive-safe by default.

Missing data in an ordinary rclone/AList/OpenList scan is not automatically proof
of deletion.

See [COLLECTORS.md](COLLECTORS.md).

---

## 6. Accepted hybrid incremental path

P0–P11 established the current opt-in incremental path.

```text
known changed scope
        │
        ▼
trusted Mutation Hint
        │
        ▼
P8 durable MergeSignal
        │
        ▼
P10/P11 runtime Wake
        │
        ▼
P6 scheduler orchestration
        │
        ▼
P5 bounded executor loop
        │
        ▼
P4 one-shot dirty executor
        │
        ▼
P0 AList/OpenList scoped refresh
        │
        ▼
PARTIAL additive-safe Snapshot
        │
        ▼
existing admission + Kernel + reconcile
        │
        ▼
Canonical Inventory + Journal
```

Important limits:

- scoped refresh is currently AList/OpenList only;
- rclone is not a P0 scoped-refresh provider;
- scoped refresh observes direct children only;
- it is PARTIAL/additive-safe;
- absence in a scoped refresh does not create removal evidence;
- no native provider delta/cursor exists;
- one P6 cycle runs at a time;
- runtime remains default disabled.

Enable explicitly:

```bash
export INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true
export INDEXCORE_INCREMENTAL_WAKE_INTERVAL=5s
```

The wake interval is a scheduler check interval, not provider polling cadence.

Persisted durable state remains authoritative for eligibility.

---

## 7. Runtime startup order

When the incremental runtime is enabled:

```text
schema compatibility
        ↓
AcquireWriterLock
        ↓
startup stale-IN_FLIGHT recovery
        ↓
existing admission worker
        ↓
hybrid incremental runtime
        ↓
bind read-only Query listener
        ↓
optional trusted Hint listener LAST
        ↓
service readiness = true
```

The service is not marked ready until the entire startup sequence succeeds.

Startup recovery performs no provider I/O.

A recovery error or recovery non-progress fails `serve` closed.

---

## 8. Shutdown / fatal order

When shutdown or a fatal runtime path begins:

```text
service readiness = false
        ↓
close/drain Hint admission
        ↓
cancel/join incremental runtime
        ↓
cancel/join admission worker
        ↓
shutdown Query server
        ↓
release writer lock
```

The writer lock is never intentionally released while write-capable actors may
still be running.

During a long drain the Query listener may remain reachable briefly, but
`/readyz` returns 503 so an orchestrator/load balancer can stop routing traffic.

---

## 9. Consumer architecture

The accepted application integration boundary is:

```text
Browser / mobile client
        ↓
application server / BFF
        ↓
typed application-owned IndexCore client
        ↓ server-side HTTP
IndexCore read-only /v1
```

Do not:

- expose the private IndexCore address to browser code;
- let UI code construct raw IndexCore origins;
- read IndexCore PostgreSQL directly from a product;
- import IndexCore Go domain types into an application;
- treat `canonical_path` as identity;
- decode opaque cursors as product state;
- flatten all IndexCore errors into a generic failure.

The reference implementation is:

`nathanxiangang-web/indexcore-reference-web`

It is intentionally separate from IndexCore and demonstrates the boundary above.

See [INTEGRATION.md](INTEGRATION.md).

---

## 10. Reference Web call flow

The accepted Reference Web shape is:

```text
Browser
   ↓
Next.js server component / application server
   ↓
server-only typed IndexCore client
   ↓
INDEXCORE_BASE_URL
   ↓
IndexCore /v1
```

The browser never needs `INDEXCORE_BASE_URL`.

Typical page flow:

```text
/roots
  -> Q2 GET /v1/roots

/roots/{rootId}
  -> Q1 GET /v1/roots/{rootId}
  -> Q9 GET /v1/roots/{rootId}/status
  -> Q4 GET /v1/roots/{rootId}/resources

/roots/{rootId}?view=active
  -> Q6 GET /v1/roots/{rootId}/active

/resources/{resourceId}
  -> Q3 GET /v1/resources/{resourceId}

/resolve
  -> Q5 GET /v1/roots/{rootId}/resolve

/removed
  -> Q7 GET /v1/roots/{rootId}/removed

/journal
  -> Q8 GET /v1/roots/{rootId}/journal
```

Visibility query options must be propagated when navigating retained/audit state.

---

## 11. Query consistency model

### Path is not identity

Q5 may return multiple PRESENT resources for one canonical path.

Consumers must preserve `ambiguous=true` rather than invent a winner.

### Pagination is generation-bound

Q4/Q6/Q7 cursors are opaque and generation-bound.

If the root generation changes:

```text
409 stale_cursor
```

The consumer restarts from page 1.

Do not combine pages from different generations.

### Journal is ordered per root

Q8 uses per-root `event_seq`.

For the next page:

> pass the last `event_seq` actually returned as the next `after_seq`.

Do not add 1.

There is no canonical global cross-root journal order.

---

## 12. Readiness vs liveness

`GET /healthz`

means:

> the process is alive.

`GET /readyz`

means:

> the process is currently ready to serve as a healthy IndexCore instance.

Readiness checks include:

- PostgreSQL reachability;
- schema compatibility;
- runtime initialization/lifecycle state.

During shutdown/fatal drain:

```text
/healthz may still respond
/readyz -> 503
```

Consumers and deployment platforms should use the distinction correctly.

---

## 13. Current P12 validation phase

P12 is the current cross-repository deployment-soak phase.

It combines:

- accepted P11 hybrid runtime;
- a controlled AList/OpenList-compatible fixture;
- trusted loopback Hints;
- sustained incremental mutations;
- provider transient/retry validation;
- graceful restart;
- controlled crash/startup recovery;
- continuous read-only observations through `indexcore-reference-web`.

P12 does not change the production architecture.

It validates the architecture already accepted.

The authoritative plan is:

[`architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md`](architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md)

---

## 14. Still not authorized

The following remain outside the current architecture:

- incremental runtime default-on;
- provider-native delta/cursor;
- network/public Hint;
- second writer / multi-daemon HA;
- parallel P6 cycles;
- automatic INTERNAL retry;
- automatic BLOCKED/SUSPENDED repair;
- destructive scoped removal;
- direct 115 integration;
- Gate 5 successor-product architecture.

Any such change requires a separate Architect decision.

---

## 15. Document map

For current usage:

- [QUICKSTART.md](QUICKSTART.md) — start and run IndexCore;
- [CLI.md](CLI.md) — administration/runtime commands;
- [HTTP-API.md](HTTP-API.md) — exact Query + internal Hint HTTP contracts;
- [INTEGRATION.md](INTEGRATION.md) — application/Reference Web integration;
- [COLLECTORS.md](COLLECTORS.md) — provider configuration;
- [OPERATIONS.md](OPERATIONS.md) — deployment/recovery/runtime operations.

For frozen contracts/history:

- [architecture/](architecture/);
- [decisions/](decisions/);
- [gate2/](gate2/);
- [gate3/](gate3/);
- [gate4/](gate4/);
- [incremental/](incremental/).

The durable architecture guardrails remain in
[`../ARCHITECTURE-INVARIANTS.md`](../ARCHITECTURE-INVARIANTS.md).
