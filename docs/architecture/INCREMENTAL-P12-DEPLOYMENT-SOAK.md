# Incremental P12 — Deployment Soak with Reference Test Web

> Status: **ARCHITECT PLAN — IMPLEMENTATION AFTER PLAN MERGE**
>
> Parent incremental architecture: #57
>
> Predecessor: P11 Production Hybrid Runtime Hardening — **ARCHITECT_ACCEPTED**
>
> P11 implementation merge: `ec980a5e00aa562cc3bd6d74c5e6f03aa46f280c`
>
> P11 closeout main: `90567935e47716980da5b810883c533ac196bbd8`
>
> P11 exit decision: **AUTHORIZE_DEPLOYMENT_SOAK**
>
> Reference test consumer repository:
> `nathanxiangang-web/indexcore-reference-web`
>
> Reference test consumer baseline:
> `3fea8a83a8c3faa78e713de71a110b9757c07bc1`
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. Purpose

P11 proved the hybrid incremental runtime is internally bounded, fail-closed,
observable, readiness-aware, race-tested, and independently verified in
PostgreSQL 18 CI.

P12 asks the next question:

> Can the accepted P11 runtime stay operationally correct over a sustained
> deployment interval while an independently-owned **test/reference consumer**
> continuously reads the frozen Q1–Q9 contract?

P12 is a **validation/soak phase**, not a feature phase.

It must exercise the real accepted chain:

```text
provider state changes
    ↓
trusted same-host Mutation Hint
    ↓
P8 durable MergeSignal
    ↓
P10/P11 runtime Wake
    ↓
P6 -> P5 -> P4
    ↓
P0 AList/OpenList scoped refresh
    ↓
PARTIAL additive-safe Snapshot
    ↓
existing admission + Kernel + reconcile
    ↓
Canonical Inventory + Journal
    ↓
read-only /v1 Q1–Q9
    ↓
indexcore-reference-web server-side client
    ↓
rendered consumer state
```

A normal full `indexcore scan` does **not** count as P12 incremental evidence.

## 2. Why the Reference Test Web is the soak consumer

`indexcore-reference-web` is the accepted Gate-4 **test/reference consumer** baseline.
It is deliberately a validation Web, not a production Web, product frontend, or
future production UI template.

It already proves:

- server-side-only IndexCore access;
- Q1–Q9 coverage;
- typed consumer-owned wire DTOs;
- runtime enum validation;
- hierarchy vs active-list semantics;
- ambiguity handling;
- removed/root-visibility propagation;
- generation-bound stale cursors;
- per-root Journal pagination;
- degraded behavior when IndexCore is unavailable;
- recovery after independent IndexCore restart;
- zero IndexCore PostgreSQL/Go/provider coupling.

P12 reuses that boundary instead of building another fake client.

The Reference Test Web is an **observer only**.

It must not:

- send Mutation Hints;
- call IndexCore CLI;
- read PostgreSQL;
- import IndexCore Go code;
- know provider credentials;
- write Canonical state;
- gain an application database just for soak;
- become the soak orchestrator.

## 3. P12 authority boundary

### Authorized in IndexCore verification/operations side

- a deployment-soak orchestration script;
- a verification-only AList/OpenList-compatible mutable fixture service;
- fixture controls for:
  - add/update direct children;
  - temporary provider 5xx/throttle responses;
  - controlled blocking of one scoped request for crash recovery validation;
- same-container/loopback Hint delivery from the soak driver;
- log/result collection;
- verification-only Store inspection at the end of the soak;
- a Docker Compose soak overlay/profile if useful;
- result documentation.

### Authorized in Reference Test Web

Only validation-support changes are expected:

- a read-only soak observer script;
- a soak runbook;
- optional `package.json` script entry for the observer;
- tests for the observer helper itself where practical.

No `src/app/**` or `src/lib/indexcore/**` **test-Web application/client behavior**
change is expected.

If a test-Web application/client change appears necessary, stop for Architect review and
classify whether it is:

```text
IndexCore responsibility
consumer responsibility
operations responsibility
out of scope
```

### Not authorized

- changing Q1–Q9;
- adding public IndexCore writes;
- changing Hint from exact-loopback-only;
- exposing Hint to the Reference Web;
- new IndexCore binary/daemon;
- second writer / HA;
- parallel P6 cycles;
- automatic INTERNAL retry;
- automatic BLOCKED/SUSPENDED repair;
- native provider delta/cursor;
- direct 115 integration;
- destructive removal from scoped refresh;
- migration/schema expansion;
- runtime default-on;
- product auth/search/download/preview/AI work;
- Gate 5.

## 4. Soak baselines

Every P12 result must record exact commits.

Initial accepted baselines:

```text
IndexCore:
90567935e47716980da5b810883c533ac196bbd8

Reference Web:
3fea8a83a8c3faa78e713de71a110b9757c07bc1
```

If either main moves before the soak run, record the actual tested commits and
show that the P12 branches are based/rebased correctly.

## 5. Deployment topology

Preferred topology:

```text
                     ┌─────────────────────────┐
                     │ Reference Test Web     │
Browser/observer --->│ Next.js server         │
                     │ INDEXCORE_BASE_URL     │
                     └───────────┬─────────────┘
                                 │ read-only Q1–Q9
                                 ▼
┌──────────────────────────────────────────────────────────┐
│ IndexCore serve                                          │
│                                                          │
│ public/private Query :8080  <-----------------------------┤
│                                                          │
│ P11 hybrid runtime ENABLED EXPLICITLY FOR SOAK           │
│                                                          │
│ Hint 127.0.0.1:<port>  <--- same-container soak driver   │
│                                                          │
│ P6 -> P5 -> P4 -> P0 scoped refresh                      │
└────────────────────────────┬─────────────────────────────┘
                             │
                             ▼
                     PostgreSQL 18

IndexCore P0 scoped refresh
        │
        ▼
controlled AList/OpenList-compatible fixture
(test/verification only)
```

Reference Test Web never sees the Hint endpoint or provider-control endpoint.

## 6. Soak runtime configuration

The repository default remains:

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=false
```

The **soak deployment only** explicitly enables:

```text
INDEXCORE_INCREMENTAL_RUNTIME_ENABLED=true
INDEXCORE_INCREMENTAL_WAKE_INTERVAL=1s
INDEXCORE_HINT_ADDR=127.0.0.1:8090
INDEXCORE_HINT_TOKEN=<ephemeral >=32-byte soak token>
INDEXCORE_LOG_FORMAT=json
INDEXCORE_LOG_LEVEL=info
```

The 1s wake interval is an accelerated validation setting, not a production
default recommendation and not provider polling cadence.

No committed secret value.

## 7. Provider fixture requirements

P0 scoped execution supports AList/OpenList only, so P12 must not use rclone as
the incremental source.

The fixture must be **AList/OpenList API compatible enough for accepted runtime
paths**, while remaining test-only.

Minimum behavior:

- initial tree enumeration sufficient to establish canonical parent directories;
- `/api/fs/list` semantics used by the accepted adapter;
- scoped `refresh=true` returns one bounded direct-child response;
- `total == len(content)`;
- mutable direct-child state;
- deterministic 5xx/transient failure mode;
- deterministic request-block mode;
- no fake successful truncation.

The fixture control surface is test-only and must not be reachable through
IndexCore or Reference Test Web application routes.

## 8. Soak roots and scopes

Use at least **2 ACTIVE roots** and at least **2 hot scopes**.

Example:

```text
root A
  /hot-a
  /cold-a

root B
  /hot-b
```

Initial full AList/OpenList scans establish canonical directory parents.

All P12 incremental mutations occur under known PRESENT directory scopes.

Scoped refresh remains additive-only:

- additions/updates may become visible;
- absence/removal is not P12 evidence;
- empty scoped result must not delete anything.

## 9. Steady-state soak workload

Minimum mandatory soak:

```text
duration                         >= 30 minutes
successful mutation->visibility  >= 100
roots                            >= 2
scopes                           >= 2
```

Each normal iteration:

1. mutate fixture direct-child state;
2. record mutation timestamp;
3. send authenticated Hint to the exact scope;
4. require HTTP `202` without waiting for provider execution;
5. poll Reference Test Web rendered output until the new resource/state is visible;
6. record visibility timestamp and latency;
7. confirm Reference Test Web stays operational.

Prefer unique additive filenames for deterministic identity.

Record latency distribution:

- min;
- median/p50;
- p95;
- max.

These are soak observations, **not a production SLA**.

A single mutation not visible within 60 seconds is a soak failure unless the
iteration is inside an explicitly injected failure/restart scenario.

## 10. Live Hint burst

At least once during the soak:

- issue >=20 valid Hints rapidly across the hot scopes, including repeated same
  scope hints;
- do not wait between Hint responses.

Prove:

- Hint admission remains bounded/non-blocking;
- P11 runtime does not fatal;
- one P6 cycle at a time;
- burst/cooldown semantics remain active;
- all durable positive mutations eventually become visible;
- Reference Test Web does not observe malformed contract responses.

HTTP 429 during an intentionally saturated Hint ingress is acceptable only if
recorded and retried by the **soak driver** after the server's Retry-After. It
must not be hidden.

## 11. Provider transient/retry scenario

Inject one bounded provider transient failure on a hot scope.

Expected accepted behavior:

```text
Hint durable accepted
  -> P4 scoped provider call
  -> TRANSIENT_PROVIDER (or THROTTLED)
  -> RETRY_WAIT
  -> accepted P11 retry maintenance promotes when due
  -> later P6/P4 attempt
  -> provider recovered
  -> Canonical/Journal visibility
  -> Reference Test Web sees result
```

Requirements:

- no manual DB edit;
- no manual `incremental run` to rescue it;
- no automatic INTERNAL promotion;
- eventual recovery after the configured accepted retry delay;
- structured logs show retry promotion and later success.

## 12. Graceful restart scenario

Keep Reference Test Web running.

Trigger a normal IndexCore shutdown/restart while soak data exists.

Prove:

- `/readyz` becomes 503 before long drain completes;
- writer lock remains held until write-capable actors stop;
- Reference Test Web remains HTTP-serving and shows its accepted degraded state while
  IndexCore is unavailable;
- no private IndexCore address leaks into rendered HTML;
- after IndexCore returns ready, Reference Test Web recovers without restart;
- subsequent Hint->incremental->Reference Test Web visibility still works.

## 13. Controlled crash / startup recovery scenario

At least once:

1. fixture blocks one scoped provider request after P4 has claimed work;
2. confirm the runtime is inside the blocked request;
3. terminate the IndexCore process/container without graceful shutdown;
4. unblock/restore fixture;
5. restart IndexCore using the same PostgreSQL volume;
6. allow P11 startup stale-IN_FLIGHT recovery to run before listener exposure;
7. verify the durable work becomes executable again;
8. send/retain the necessary durable signal and prove eventual visibility through
   Reference Web.

Prove:

- no second writer is active;
- startup recovery makes progress;
- no permanent IN_FLIGHT residue remains;
- no inline duplicate execution before recovery boundary;
- Reference Test Web degrades then recovers.

The soak harness must make this deterministic; do not rely on killing at a random
moment.

## 14. Continuous consumer observations

The Reference Test Web soak observer must remain read-only.

During steady state it should sample rendered routes such as:

```text
/
 /roots
 /roots/<active-root>
 /roots/<active-root>?view=active
 /journal?root=<active-root>
```

At selected mutation checkpoints also exercise:

- Q3 resource detail;
- Q5 resolve;
- pagination where present;
- stale cursor behavior at least once if practical.

The observer fails on:

- rendered contract-malformed state;
- unexpected 5xx from the Reference Test Web;
- lost root/resource that should be visible;
- leaked private IndexCore address;
- unexpected Q1–Q9 semantic drift.

Expected degraded HTML while IndexCore is intentionally down is not a failure.

## 15. Gate-4 regression remains required

P12 does not replace the accepted Gate-4 regression.

Before final P12 acceptance run:

```bash
cd indexcore-reference-web
npm run typecheck
npm run lint
npm test
npm run build
INDEXCORE_SRC=/path/to/index-core npm run e2e:real
```

Expected baseline:

```text
e2e:real PASS=52 FAIL=0
```

If the baseline changes legitimately, stop for Architect review rather than
silently editing the expected count.

## 16. Final durable-state verification

The IndexCore verification side may inspect Store state at the end of the soak.

Required:

- no unexpected IN_FLIGHT rows;
- no unresolved PENDING work caused by the completed workload;
- no overdue allowed RETRY_WAIT left after provider recovery;
- no BLOCKED/SUSPENDED introduced by normal steady workload;
- all roots remain ACTIVE unless a scenario explicitly says otherwise;
- writer lock released after final shutdown.

Direct Store inspection remains in IndexCore verification code. The Reference Web
must not do it.

## 17. Logs and evidence

Capture:

- exact IndexCore commit;
- exact Reference Web commit;
- exact PostgreSQL version;
- runtime/hint/wake configuration without secrets;
- soak start/end/duration;
- successful mutation count;
- Hint 202 / 429 / unexpected response counts;
- mutation->Reference Test Web visibility latencies;
- retry scenario timeline;
- graceful restart timeline;
- crash/recovery timeline;
- `incremental_runtime_wake` counts/reasons;
- `incremental_cycle_finished` counts/stop reasons;
- `incremental_retry_promotion` counts;
- `incremental_inflight_recovered` events;
- `incremental_runtime_fatal` count;
- Reference Test Web observer failures;
- Gate-4 E2E PASS/FAIL summary.

Secret values must not be recorded.

## 18. P12 acceptance criteria

P12 passes only if:

1. mandatory soak runs >=30 minutes;
2. >=100 normal durable mutation->Reference Test Web visibility confirmations pass;
3. both roots and multiple scopes are exercised;
4. Reference Web remains read-only and zero-internal-coupling;
5. Hint remains exact-loopback-only;
6. one timer/hint runtime remains one serialized P6 executor;
7. Hint burst does not bypass runtime safety bounds;
8. provider transient path reaches RETRY_WAIT and later recovers automatically;
9. graceful restart produces expected readiness/degraded/recovery behavior;
10. controlled crash demonstrates startup stale-IN_FLIGHT recovery;
11. no unexpected runtime fatal occurs;
12. no secret/private origin leaks into consumer output/logs;
13. final durable incremental state is drained/healthy;
14. Gate-4 Reference Test Web regression remains green;
15. IndexCore PostgreSQL 18 CI remains green;
16. no production contract or migration change is introduced.

## 19. Expected IndexCore implementation surface

Preferred verification-only additions:

```text
scripts/soak/**
internal/runtime/e2e/**            # verification fixture/helper only
docker-compose.soak.yml            # optional overlay
docs/incremental/P12-DEPLOYMENT-SOAK-RESULT.md
docs/OPERATIONS.md                 # runbook link only if useful
```

Production changes are **not expected**.

Stop for Architect review before changing production behavior under:

```text
cmd/indexcore/**
internal/runtime/incrementalruntime/**   # production logic
internal/runtime/app/**
internal/runtime/incrementalexec/**
internal/runtime/incrementalorch/**
internal/runtime/scan/**
internal/store/postgres/**
internal/transport/httpapi/**
internal/transport/hintapi/**
internal/kernel/**
internal/domain/**
migrations/**
```

Test/verification imports of existing internals remain allowed inside
`internal/runtime/e2e/**`.

## 20. Expected Reference Test Web implementation surface

Preferred:

```text
scripts/soak-observer.sh
docs/SOAK-RUNBOOK.md
package.json                        # optional script entry only
tests/**                            # observer helper regression if needed
```

Not expected:

```text
src/app/**
src/lib/indexcore/**
next.config.ts
new database/ORM/provider dependency
```

If the existing Reference Test Web cannot observe the required P12 state without a
test-Web application/client code change, stop and classify the gap.

## 21. Cross-repository delivery model

P12 uses two implementation PRs:

### IndexCore PR

Owns:

- provider fixture;
- Hint/workload driver;
- deployment orchestration;
- durable-state verification;
- consolidated P12 result report.

### Reference Test Web PR

Owns only:

- read-only observer/runbook/script support.

Both PRs must remain unmerged until Architect review.

The P12 result report in IndexCore is authoritative and records the exact
Reference Test Web PR/head used.

## 22. Result report

Create in IndexCore:

`docs/incremental/P12-DEPLOYMENT-SOAK-RESULT.md`

It must include all §17 evidence and:

```text
INDEXCORE_CONTRACT_CHANGES: NONE
REFERENCE_WEB_BOUNDARY_CHANGES: NONE
RUNTIME_DEFAULT_ON: NO
NATIVE_DELTA: NO
NETWORK_HINT: NO
SECOND_WRITER: NO
MIGRATION: NO
GATE5: NO
FROZEN_CONTRACT_CHANGES: NONE
```

## 23. P12 exit decision

After soak evidence the Architect chooses exactly one:

```text
STOP
AUTHORIZE_LONGER_DEPLOYMENT_SOAK
AUTHORIZE_INCREMENTAL_RUNTIME_DEFAULT_ON_DESIGN
AUTHORIZE_NATIVE_DELTA_DESIGN
RESEARCH_FURTHER
```

No exit is pre-authorized.

A P12 pass does not itself make the runtime default-on.

## 24. Current authorization

```text
P0–P11 incremental phases                      ARCHITECT_ACCEPTED
P12 deployment soak with reference consumer   AUTHORIZED AFTER PLAN MERGE

incremental runtime default-on                 NOT AUTHORIZED
native delta/provider cursor                   NOT AUTHORIZED
network/public Hint                            NOT AUTHORIZED
second writer / HA                             NOT AUTHORIZED
destructive scoped removal                     NOT AUTHORIZED
migration/schema expansion                     NOT AUTHORIZED
direct 115                                     NOT AUTHORIZED
Gate 5                                         NOT AUTHORIZED

FROZEN_CONTRACT_CHANGES                        NONE
```
