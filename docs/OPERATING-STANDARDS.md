# IndexCore Operating Standards

This document defines the operational safety policy. The detailed command and runtime reference remains OPERATIONS.md; the development procedure is in DEVELOPMENT.md.

## 1. Operating posture

IndexCore is a Stable Alpha Foundation / Incremental Hardening service. Its application-facing Query API is read-only. The current Alpha has no built-in authentication, authorization, or TLS termination.

- Bind the Query listener to loopback or a trusted private service network.
- Do not expose raw IndexCore directly to the public Internet.
- Put application authentication and authorization in the consumer/BFF or trusted network edge.
- Keep PostgreSQL private. Consumer applications must not connect to it.
- Keep provider credentials in environment-backed secret references, never plaintext adapter JSON, logs, Issues, or PRs.

## 2. Three interface planes

| Plane | Purpose | Rule |
| --- | --- | --- |
| Query | /v1 and health endpoints | Read-only; ordinary consumers use this plane |
| Administration | indexcore CLI and the single writer runtime | Operator-only; not a browser API |
| Trusted Hint | /internal/v1/mutation-hints | Separate exact-loopback listener, bearer token, disabled unless configured |

A Hint response of HTTP 202 means only that the Hint was durably accepted. It does not mean the provider was refreshed or Canonical/Journal state has changed.

The Hint listener must bind to literal loopback (127.0.0.1 or ::1) and use an environment-only token of at least 32 bytes. Never forward the token to a browser, product application, Reference Test Web, or external network.

## 3. Single-writer and command safety

- One active write-orchestration process is allowed per PostgreSQL database.
- indexcore serve owns the PostgreSQL advisory writer lock for its lifetime.
- A second writer must fail closed. Do not work around the lock.
- indexcore incremental run uses the same lock and cannot run while serve owns it.
- indexcore scan --root ... performs real Collector work and may generate upstream provider requests. It is not a harmless read-only diagnostic.
- indexcore root diagnose is bounded/read-only and must not be described as source freshness or downstream consumer proof.
- serve never auto-migrates. Migration is an explicit operator action.

Never perform ad hoc SQL updates to Root, Snapshot, Admission, Work, Canonical Inventory, or Journal in production. Never clear queues, bulk-retry INTERNAL work, fabricate coverage, force a generation, or synthesize Journal events.

## 4. Runtime defaults and performance controls

The repository defaults are intentionally conservative:

| Setting | Repository default | Operational meaning |
| --- | --- | --- |
| INDEXCORE_INCREMENTAL_RUNTIME_ENABLED | false | hybrid runtime disabled unless explicitly enabled |
| INDEXCORE_INCREMENTAL_WAKE_INTERVAL | 5s | scheduler check interval, not provider polling cadence |
| INDEXCORE_INCREMENTAL_READ_WORKERS | 1 | serial source reads; 2/4/8 are explicit opt-ins |
| INDEXCORE_SOURCE_GLOBAL_INTERVAL | 100ms | process-wide minimum source HTTP spacing |
| INDEXCORE_SOURCE_ROOT_INTERVAL | 100ms | per-Root minimum source HTTP spacing |
| INDEXCORE_SOURCE_PROVIDER_INTERVAL | 100ms | per-source-group minimum spacing |
| INDEXCORE_INCREMENTAL_FIRST_DISCOVERY_ENABLED | false | F1 first-discovery scheduling disabled |
| INDEXCORE_INCREMENTAL_MIRROR_REVISIT | 0 | F2 mirror revisit disabled |

Important distinctions:

- These are process-local budgets, not a distributed reservation ledger and not proof of the upstream account's actual quota.
- A provider-group key is not automatically the same as a 115 account/quota identity.
- OpenList refresh=true may reach 115 Open. Count actual HTTP starts, response status, retries, and provider grouping.
- Mock throughput does not authorize production concurrency.
- Lower PENDING counts or more completed scopes do not prove better freshness.
- F1/F2 and reader counts above 1 require isolated evidence, a measured request envelope, fairness/capacity analysis, rollback-to-serial proof, and separate operator authorization.
- The last recorded production receipt (2026-10-26) used one reader and 200ms global/Root/provider spacing; F1/F2 were off. This is historical evidence, not a claim about today's live process.

## 5. Preflight before any production change

A production operation must have a bounded Issue/task and an operator with legitimate host access.

### Read-only preflight

Record, with secrets redacted:

- actual running executable path, embedded version/commit, and SHA256;
- actual process owner/UID, PID, restart count, and service/container identity;
- actual runtime configuration and effective feature flags;
- PostgreSQL identity/version/schema migration level;
- single writer process and granted advisory lock;
- /healthz and /readyz;
- Root lifecycle/generation and bounded Q4/Q5/Q8/Q9 / Journal anchors;
- current Work cohorts, due-age distribution, retry/blocked states;
- source route, request budget grouping, observed HTTP starts and 429/5xx where instrumented;
- verified backup and a restore test on an isolated PostgreSQL 18 instance;
- exact old binary/config recovery artifact and state compatibility.

Do not infer the writer from an image tag or read-facade container. Inspect the actual mounted executable/process. If live evidence is unavailable, report LIVE_METRICS_NOT_OBSERVED.

### Controlled release

1. Build only from the reviewed exact commit and supported target architecture.
2. Verify embedded version/commit and artifact SHA256.
3. Confirm service UID, file ownership, permissions, config, mount, and rollback path before stopping the current writer.
4. Drain only the owned IndexCore writer cleanly. Never stop unrelated application, database, proxy, or provider services.
5. Ensure no second writer can start. Preserve the advisory lock boundary.
6. Apply a migration only when it is part of the reviewed release and has a tested upgrade/restore plan. Do not infer that a code-only fix needs a migration.
7. Install the artifact atomically and start the single writer.
8. Verify actual process SHA, UID, one writer, /readyz, and Q4/Q5/Q8/Q9/Journal continuity.
9. Observe a prospective interval with explicit start/end UTC, fatal/cycle/restart deltas, source request rates/statuses, queue age, and changed-source outcomes.
10. Publish a redacted operator receipt with exact artifact and evidence hashes.

A green CI run, a successful container health check, or one ready response is not a production acceptance window.

## 6. Rollback and recovery

- Preserve the old executable, config, service definition, and consistent database backup before cutover.
- Verify restore on an isolated PostgreSQL 18 instance; a backup file existing is not restore proof.
- Roll back only to a binary known to be compatible with the current persisted state and migrations.
- If the old binary may not understand newer persisted state, do not blindly downgrade. Stop, preserve evidence, and obtain a state-compatible recovery plan.
- Never rewrite Work, Admission, Snapshot, Canonical, or Journal rows to make an old binary start.
- A rollback that was not executed must be reported as NOT_EXERCISED, not PASS.

## 7. Incident response

When startup, executor, source, database, or Query failures occur:

1. Preserve the original failure and exact timestamp.
2. Identify the actual process and binary SHA.
3. Capture bounded logs and typed error families; use validated SQLSTATE only where available.
4. Separate process/ownership/permission errors from application runtime errors.
5. Separate provider 429/5xx/timeouts from DB/transaction/CAS errors.
6. Record whether the source request was actually issued or deferred by budget.
7. Preserve queue and Journal evidence before any recovery action.
8. Reproduce in an owned PostgreSQL 18 fixture with deterministic barriers where possible.
9. Make one narrow fix and retain a red-before/green-after regression.
10. Keep performance, capacity, and correctness incidents separate unless evidence proves a shared cause.

Public evidence must not contain raw errors, SQL, DSNs, tokens, provider bodies, source URLs/paths, user file names, or credentials. Use bounded, redacted, hashed correlation identifiers.

Do not suppress a first fatal because later probes pass. Do not convert missing telemetry into zero. Do not repeatedly restart or retry blindly.

## 8. Acceptance language

Use exact dispositions:

- PASS — all stated predicates have evidence.
- NOT_VERIFIED — evidence window or workload is incomplete.
- BLOCKED — required authorization, source, or rollback prerequisite is missing.
- NO_GO — a required predicate failed or risk exceeds the approved boundary.
- NOT_TRACKED — the system does not currently measure the requested metric.

Never describe an isolated mock result as production performance, a shortened validation as a full soak, or a deployment receipt as proof of future health.

## 9. Current evidence links

- Project state: ../PROJECT-STATE.md
- P0-144 startup handoff: p0/P0-144-STARTUP-HANDOFF.md
- Issue #132 production release / stability gate: https://github.com/nathanxiangang-web/index-core/issues/132
- Issue #134 first-discovery / mirror-revisit performance: https://github.com/nathanxiangang-web/index-core/issues/134
- Issue #138 bounded fetch/read-pool work: https://github.com/nathanxiangang-web/index-core/issues/138
- P12 shortened validation result: incremental/P12-DEPLOYMENT-SOAK-RESULT.md