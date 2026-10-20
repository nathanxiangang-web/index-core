# P0-R3: read-only recovery diagnostics (Issue #123)

Status: implemented for review, NOT Architect acceptance.
Baseline: `54b15e988aee714d84d6b82d681b9f71ee8140bd`, R2 bounded accepted.
One branch / one PR; no production deployment or automatic recovery authorized.

## Module design and extension constraints

```
root diagnose (CLI; dedicated non-provider flag set)
  -> DiagnosticReader (PostgreSQL read-only facade)
  -> diagnostic.Report + pure evidence evaluation
  -> one JSON object
```

- `internal/diagnostic`: output DTOs, limits, pure classification. No Store,
  Collector, Kernel writer, HTTP or product dependency. Describes evidence,
  never decides new Canonical truth.
- `internal/store/postgres/diagnostic.go`: SELECT-only root-scoped reads in one
  explicit READ ONLY REPEATABLE READ transaction. Schema compatibility is checked
  inside that same snapshot. No row/advisory lock, pool export, claim or provider
  construction. Final rollback releases the reader's transaction.
- `internal/runtime/app/root_diagnose.go`: total deadline, one-connection pool
  with read-only startup default, one sanitized report. Does not initialize
  Serve/Scan or validate unrelated provider/runtime flags.
- `cmd/indexcore`: dispatches without unrelated configuration. Existing doctor,
  serve, /readyz, Q1-Q9 and all write behavior stay unchanged.

Keep this an operator CLI, not a public endpoint. Additional evidence must remain
bounded, read-only, root-scoped, secret-free and independently tested. A consumer
cursor, schema, recovery action or live probe requires separate authorization;
never add one merely to make UNKNOWN green.

## Command / bounded output

Supply INDEXCORE_DATABASE_URL through a protected environment/secret manager:

```sh
indexcore root diagnose --root-id <UUID> \
  --timeout 5s --row-limit 1000 --stale-after 5m
```

The command does not need provider credentials. `--database-url` is accepted for
consistency; protected env injection avoids a process-list-visible DSN.

- Timeout default5s, hard1ms..30s; caller cancellation respected, connection
  establishment included. Transaction cleanup has a separate maximum1s.
- Row limit default1000, hard1..10000 per dataset (work, Canonical, Journal),
  with one extra sentinel row. Migration versions cap at required+1.
- Results/memory are capped. PostgreSQL may examine/sort more underlying rows
  than LIMIT; deadline bounds execution, not a physical table-scan row budget.
  No unbounded table dump or payload output.
- Stale threshold default5m, hard1s..24h: operator advisory threshold, not a source
  SLA or Kernel deletion-authority threshold.
- One report, no ticker/listener/recovery. JSON structure/check order stable for
  identical facts; map keys sorted. Database `as_of` naturally changes each run.

Inspect query_complete, overall, checks and each complete/counts_exact flag.
Exit0 is reserved for fully green evidence. Exit1 means invalid arguments,
incomplete query, DEGRADED or UNKNOWN. In R3 otherwise healthy persisted state
still reports UNKNOWN/exit1: live-source and downstream acknowledgements are not
measured. This is not a failed scan; do not wire it to automatic restart/retry.
Static stderr contains no driver error; machine consumers use JSON check codes.

Illustrative excerpt, not production evidence:

```json
{
  "version": 1,
  "overall": "UNKNOWN",
  "query_complete": true,
  "checks": [
    {"code":"BOUNDED_CONSISTENCY","status":"OK","reason":"Generation bounds, event sequence and terminal presence checks passed; not a payload audit"},
    {"code":"SOURCE_LIVE","status":"UNKNOWN","reason":"No provider call; current source availability is not established"},
    {"code":"CONSUMER_PROJECTION_LAG","status":"NOT_TRACKED","reason":"IndexCore has no downstream Q8 acknowledgement/projection cursor"}
  ]
}
```

## Evidence interpretation / advisory manual response

| Evidence | Meaning / next step |
| --- | --- |
| DATABASE_UNAVAILABLE / SCHEMA_UNAVAILABLE / SCHEMA_INCOMPATIBLE | No green report. Check protected connection and matching binary/migration history; never migrates here. |
| ROOT_NOT_FOUND / ROOT_INACTIVE | Confirm UUID/lifecycle using existing administration; no transition. |
| WATCH_MISSING / WATCH_DISABLED | No root-scope recurring schedule; descendant watches/daemon liveness not inferred. Review explicit policy. |
| WATCH_DUE / WATCH_LAG | Effective due=max(next_due,deferred); lag exceeds operator threshold, not proof of dead daemon. Review logs. |
| SOURCE_UNAVAILABLE / SOURCE_UNAVAILABLE_WORK | Persisted provider/auth error with failures, not current source probe. Inspect protected credentials/network separately. |
| INCOMPLETE_LISTING | Not whole-root COMPLETE; normal successful scoped PARTIAL is not automatically unhealthy. |
| SOURCE_STALE / OBSERVATION_FUTURE / ROOT_FRESHNESS | Latest snapshot old/future or not root-scoped. Child activity cannot certify whole-root freshness. |
| TOPOLOGY_UNTRUSTED | Latest mirror observation lacks reconciled v2 coverage; exact rejection reason/current topology not tracked. Do not infer a specific fault or enable deletion. |
| HISTORICAL_COVERAGE | Only historical v2 proof presence, not revalidation/current config/topology or deletion authority. |
| BLOCKED_WORK / RETRY_WAIT / IN_FLIGHT / STALE_IN_FLIGHT / WORK_LAG | Durable state/timing, never ownership/liveness authorization. Investigate before separately authorized CAS recovery. |
| RETRY_BUDGET_EXHAUSTED | Only ACTIVE opted-in alist/openlist mirror ROOT INTERNAL RETRY_WAIT with failures>=3, scheduled SOURCE_MIRROR OPERATOR_POLICY watch and topology references. No generic invented budget. References do not prove usable credentials. |
| IDENTITY_AMBIGUITY | Duplicate PRESENT paths or latest reconcile conflicts. Equal hashes do not imply the same object; never merge/remove here. |
| CANONICAL_JOURNAL_MISMATCH | Generation bounds, sequence gap or terminal presence mismatch. Preserve evidence; investigate, never direct SQL edits. |
| BOUNDED_CONSISTENCY | Complete capped inventories agree on terminal presence and sequence/generation bounds; not a payload or consumer audit. |
| WORK_LIMIT / IDENTITY_LIMIT / CONSISTENCY_LIMIT | Incomplete sample / lower-bound counts, not full reconciliation. Increase limit within bound or request separate investigation. |
| CONSUMER_PROJECTION_LAG / PROCESS_READINESS | NOT_TRACKED: no downstream cursor/running-process probe in this report. |

Latest means persisted snapshot ordered by observed time/UUID, not latest attempted
provider call: failed calls can create no snapshot. HistoricalCoverageV2 reports
presence, not validation; no failed-call reason is reverse-engineered from absent
coverage. Scoped coverage never makes a snapshot whole-root COMPLETE.

Root generation need not equal maximum Journal generation. High-watermark and
maximum Journal generation use separate indexed tail reads even if row sampling
caps. Complete bounded terminal-presence comparison allows J6 corrective events;
it is not a full payload/identity-history audit. Consumer projection/acknowledgement
lag is NOT_TRACKED, never inferred from Journal rows or HTTP200.

No filenames/raw paths, DSNs, adapter JSON/env names, snapshot provenance/errors,
Journal payloads, SQL/driver errors or provider URLs emitted. Only root UUID,
schema-constrained enums, timestamps, aggregate counts and constant reasons.
Invalid flags are rejected without echoing values.

## Verification / receipts

Run on192.168.178.50 in dedicated internal-network disposable PostgreSQL18; no
host DB ports or production credentials/services mounted. No new skips permitted;
the16 inherited optional/dependency skips remain disclosed in the R2 report.
The focused suite contains66 leaf tests (31 pure classifications,24 PG18 state
cases,6 PG18 failure boundaries,5 standalone bounds/accepted-data/CLI tests),
all executed with zero new skips. Local full-suite/race and exact-head CI
receipts are recorded in the PR review packet, avoiding a self-referential HEAD
claim in this file. Evidence is same-executor operation, not independent review.

Tests cover pure classifications, PG18 state/query/deadline failures and limits,
read-only transaction trace/database-enforced read-only defaults, fixture table
fingerprints, zero HTTP calls while daemon writer lock stays held, secret-free
CLI/config failures and deterministic JSON.

First focused run failed because new fixtures used forbidden plaintext adapter
keys; fixtures now use environment references, without weakening validation.
Initial whole-tree format prechecks exposed Windows CRLF export behavior; source
archives now use Git-tree export with autocrlf=false, not unrelated source edits.
These failed attempts are not counted as passes.

Not proved: live115 mutation, OS/DB crash, production rollback,20k/50k load, soak,
source health, current topology trust or downstream freshness. Parent #120 stays
OPEN; R4/R5 and overall P0 acceptance are not authorized.
