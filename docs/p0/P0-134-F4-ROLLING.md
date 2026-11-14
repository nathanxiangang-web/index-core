# Issue134 F4: bounded rolling candidate, capacity remains NO-GO

Date:2026-11-13, Asia/Shanghai. Base main:9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e.
State: implementation candidate for independent review, NOT architecture acceptance.
No production deployment, flag change, real provider request or source mutation.
PR146/147 remain separate/unmerged; neither is incorporated into this branch.

## Behavior and module boundaries

[ADR013](../decisions/ADR-013-BUDGETED-MIRROR-ROLLING.md) is the proposed contract.
It covers the Owner-requested budgeted rolling, Watch lifecycle and Runtime cost
work. It adds operational migration0006; historical migrations are untouched.

| Module | Responsibility | Dependency/extension rule |
| --- | --- | --- |
| incremental/rolling | validated share/spacing/lease policy | no Store, provider or Kernel dependency |
| store/postgres mirror_* | durable cursors, admission clock, per-Root F4 reservation, binding and maintenance | Root/adapter/Watch/Work lock order; global ledger last; no canonical writes |
| incrementalorch | at most5 maintenance visits, ordinary work plus at most1 managed candidate/cycle | P5 still executes when managed emission is deferred |
| sourcebudget | extra actual background HTTP pacing, within existing total ceilings and429 handling | process-local hard HTTP envelope; exact durable poll epoch classification |
| scan | supply background context in existing serial/sibling read paths | original immutable fetch/admission fences and serial commit lane retained |
| query_cost | bounded redacted pgx query aggregates | no SQL/arguments/paths/secrets/errors in output; optional at serving pool |

`INDEXCORE_MIRROR_ROLLING_PERCENT` defaults0/OFF. Explicit1..50 requires hybrid
runtime and F2. Admission spacing estimates eight calls at the largest configured
spacing/share; this is NOT an actual HTTP count. Actual background requests also
obey the fraction of global/Root/provider HTTP ceilings. A merged foreground
Hint is not downgraded. Source readers are reused, not reimplemented or increased.
One-shot `incremental run` rejects rolling configuration before database I/O.

The per-Root reservation bounds NEW F4 poll materialization and survives restart,
failure/cancel and claim recovery. It releases only on a covered verified prefix;
newer Hint intent is preserved. Root/scope cursors rotate around protected scopes.
Existing legacy poll intents are retained, not relabeled/discarded: transition
with a populated legacy POLL backlog is NOT_VERIFIED and requires a separate
drain/transition gate. No claim is made that arbitrary inherited/foreground work
is bounded or that a permanently blocked Root's background service resumes.

Only exact managed policy is eligible. Bindings retain canonical ID, path,
adapter digest/revision and epoch. Parent publication and rebind never pay child
freshness debt. Successful own-scope observation renews the24h lease; expiry
records EXPIRED while retaining schedule. Retirement to COLD requires committed
Kernel removal/relocation and an accepted producing Snapshot/Journal anchor,
matching Watch/epoch/config and no unresolved Work/Hint or path replacement.
Operator COLD/DISABLED policy is preserved. Reappearance requires a fresh binding
and recovery intent; all histories remain. No physical Watch garbage collection.

`INDEXCORE_RUNTIME_QUERY_COSTS=true` is optional/default-off. Serve logs bounded
fingerprints/classes/calls/rows/errors/client elapsed at shutdown. These are not
server CPU, pool acquisition time, or untraced CopyFrom/Batch measurements.

## Reproducible primary experiment

Owned local Linux/PG18.6, Go1.27.1, GOMAXPROCS2. Same PostgreSQL container hard
CPU1/memory768MiB, loopback only. Shared host load and cache state are not isolated.
Both modes: Reader1, global/Root/provider200ms, F1 OFF, F2 requested120s;
one HOT Root/30s,401 actual API-enqueued NORMAL epochs,20K directory population,
12 directories injected after six actual old-scope lists, late known-file source
change after90s.20K is hierarchical SQL Canonical/verified-work/Watch history,
not20K actual initial traversals or accepted Snapshot histories. Groups/depth and
each source response remain within the existing1000-entry scoped cap.

Each mode runs the actual app-composed P10 event loop for a nominal120s, with
receiver HTTP records, persisted claim anchors, Q5/Q8 checks, generation/Snapshot
audit and PG resource samples. Root/source arrivals and workload are identical;
the rolling treatment adds share25%. Same primary source manifest:
`9a4dca6e50ee703ff9d55623adcea8cbba7dcc0b43c6be7cccd436a94fda0e2f`.
Both primary source manifests are retained. Subsequent production change is the
unrelated-to-P10 one-shot CLI rejection guard; extra fault tests were also added.
Hosted CI repeats final head; its timings are an independent environment.

| Actual120s collection | existing F2 | F4 rolling25% |
| --- | ---: | ---: |
| Receiver HTTP / actual lists |561 /112|555 /111|
| Peak receiver arrivals/anchored second |5|5|
| Peak concurrent receiver handlers |1|1|
| HTTP429 /5xx |0 /0|0 /0|
| Repeated scopes/list count, including intentional HOT visits |2.68%|2.70%|
| Managed POLL work peak |4|1|
| Managed Watch first→last |20000→20012|20000→20012|
| Total Watch rows final, including HOT Root |20013|20013|
| Automatic retirement events in this unchanged-source population |NOT_TRACKED legacy|0|
| Debt overdue-seconds first→last |69729993.57→71651411.36|69735845.42→71660969.41|
| Debt growth/elapsed second, sampled window |16708.16|16740.38|
| Source introduction→directory Q5 P50/P95/P99 s, N12 |27.70/27.72/27.72|27.27/27.28/27.28|
| New directory→Claim / child Q5+Q8 |0/12, RIGHT_CENSORED|0/12, RIGHT_CENSORED|
| Observed NORMAL wait P50/P95/max s |60.01/115.11/120.06|60.81/115.35/120.33|
| Claimed known cohort /401 |108/401|107/401|

NORMAL figures are conditional on claims reaching an actual list. The unclaimed
remainder is censored; whole401 P50/P95 cannot be claimed. Discovery→Claim and
child visibility quantiles are not fabricated for0/12. Raw visibility includes
the producing parent Snapshot's first-observation timestamp. Q5 visibility is a
polling upper bound with Q8 identity checks, not exact commit time. Heavy-fixture
known late-file Q5 convergence is NOT_TRACKED; the small actual Runtime late-file
test proves that mechanism separately, not overloaded-population convergence.
Exact client wire-starts and client timeout totals remain NOT_TRACKED. Counts
come from receiver records, never PENDING counts or estimated eight-call costs.

**Capacity: NO-GO in BOTH modes.** Bounded work is not sustainable freshness.
The rolling sample does not improve throughput or new-child visibility. Under
these caps, the measured safe observation uses about five HTTP calls: the
request-envelope lower bound for20K observations alone is about5.6h at100%, or
22.2h at25%, before foreground, retries or conservative admission. These are
arithmetic lower bounds, not measured traversal times or a changed freshness SLA.
No fixed short all-directory service guarantee is claimed; the requested120s
debt stays visible. No F1+F2 performance combination or production GO.

## Real Runtime query/resource findings

The tracer is on the actual Runtime pool. Observer Q5/Stats and receiver claim
reads use the separate fixture read pool and are excluded from tracer aggregates;
they still consume database resources. Client query elapsed16.258s→17.379s is
not additive with HTTP waiting or a measure of database CPU.

The exact fingerprint `67536c1fdbc15337` reproduces from the unchanged
`store/postgres/prior.go:priorQuery("c.root_id=$1::uuid")` template. It identifies
the authoritative whole-Root prior load, not the earlier exhausted F1 selector:

| Whole-Root prior | existing F2 | rolling |
| --- | ---: | ---: |
| Calls |112|111, including one canceled query|
| Command-tag returned rows |2240972|2200948|
| Client query/row-consumption elapsed s |9.552|9.489|

This demonstrates repeated20K-context materialization for small scoped work.
It includes transport/Go row decoding and is NOT proof of9.5s PostgreSQL execution
CPU. Changing this authority/identity context requires separate reviewed design;
no narrowed Kernel prior or speculative cache/index rewrite is made here.

PG container samples21 per mode: CPU peak58.72%/46.71%; Docker working-set range
264–285.3MiB /268.5–286MiB. Lock-waiter sample peak0/0, deadlock counter delta0/0;
these point samples do not exclude unsampled waits. Database counter deltas
include fixtures and observers: block hits7999327/8200748, temp bytes80076800 /
145145856. The temp-write difference is retained, not attributed to a particular
SQL without evidence. No resource cap was raised between trials.

## Correctness, failures and verification

Verified: durable admission/global race/Root fairness; reservation retention on
claim loss, cancel/recovery and failures; newer Hint preservation; no debt reset
on parent/config ABA; operator overrides; unique-ID/path replacement fences;
real Kernel removal→retirement/reappearance; transactional rollback of both
emission and retirement; mixed same-Root foreground/background pooled reads;
Q5/Q8, producing-Snapshot/generation and serial Journal consistency. Actual
HTTP/API429,HTTP/API503,403,timeout and cancellation cases retain intent and
produce no partial canonical history. F4-specific OS process kill and long
churn/lease soak remain NOT_RUN; inherited crash/fence gates are separate.

| Local command group | Exit | Seconds | Test/subtest events |
| --- | ---: | ---: | --- |
| vet /build |0 /0|1.61 /0.65|n/a|
| full PG18, `go test -p 1 -count=1 ./...` |0|213.83|769 pass,125 skip|
| sourcebudget +Runtime +app Race |0|34.61|111 pass,49 skip|
| F4 six-package Race, count3 |0|60.40|87 pass,3 skip|
| Issue134 related Race |0|129.02|47 pass,13 skip|
| Existing16-read P10 pool Race |0|49.68|5 pass|

No DATA RACE report. Optional scale/replay/purpose-DB skips are disclosed, not
counted as passed capacity. Exact-head CI is authoritative on the submitted PR;
CI was not yet available when this source document was committed. The CI replay
job succeeds only as an evidence/correctness collection; its NO-GO capacity
verdict is independent and retained in artifacts.

The first leaf Runtime regression found missing lease renewal through the legacy
zero-child completion path; F4 now routes positive zero-child mirror observations
through mirror completion, without changing default legacy behavior. Debt-clock
red/green tests retain a second correction: parent/rebind cannot advance coverage,
and both modes count successful verification from all accepted trigger sources.
Two pre-correction legacy explorations remain local historical evidence, excluded
from the primary comparison. A host reboot/Docker startup interrupted one initial
environment check; the owned PG container was resumed at its original caps.

## Evidence and reproduction

All public evidence is synthetic. Compressed raw receiver NDJSON, PG samples,
Go test logs, red/green debt regression and source manifests are indexed by
[the manifest](evidence/issue134-f4-manifest-20261113.json). The manifest records
compressed/uncompressed SHA256; decompression is round-trip checked. Earlier
failed/partial attempts remain in the local full archive, not relabeled PASS.

Use only a separately provisioned owner-marked loopback PG18 `index138_test`
per `testutil/issue138.go`; never inherit a production DSN. Both runs reset the
owned schema sequentially. For each mode choose a NEW evidence directory:

```bash
export INDEXCORE_TEST_DATABASE_URL='postgres://indexcore:indexcore@127.0.0.1:55443/index138_test?sslmode=disable'
export INDEXCORE_ISSUE138_DISPOSABLE=owned-disposable-only
INDEXCORE_F4_REPLAY=1 INDEXCORE_F4_MODE=legacy INDEXCORE_F4_EVIDENCE_DIR=/tmp/f4-legacy-new \
  go test -json -p 1 -count=1 -timeout=4m -run '^TestIssue134RollingRuntimeEvidence$' ./internal/runtime/app
INDEXCORE_F4_REPLAY=1 INDEXCORE_F4_MODE=rolling INDEXCORE_F4_EVIDENCE_DIR=/tmp/f4-rolling-new \
  go test -json -p 1 -count=1 -timeout=4m -run '^TestIssue134RollingRuntimeEvidence$' ./internal/runtime/app
python3 scripts/perf/analyze-f4.py /tmp/f4-legacy-new/rolling-legacy.ndjson /tmp/f4-rolling-new/rolling-rolling.ndjson --output /tmp/f4-comparison.json
```

The analyzer also reads the committed `.ndjson.gz` and matching PG files directly.
It reports observed/censored cohorts and source-matched query fingerprints.
The dedicated CI retains raw failed-run records and independently repeats both
120s modes. Hosted CPU/memory caps and absolute times differ from the local lab.

## Review, rollout and next gate

Operational/correctness gates PASS; latency/scalability objective FIX/NO-GO.
Keep Issue134 open and production flags/readers untouched. Required next work:
review authoritative prior-load scaling, F1-only same-workload profiling,
inherited POLL transition and blocked-reservation handling, sustained multi-Root
churn/real24h lease evidence, full traversal and a separately budgeted real-source
freshness gate. Green mock/CI is not a real upstream quota approval.

Migration0006 requires explicit review/application. Old binaries reject the new
schema even with flags off; serve never auto-migrates. Flag-off cannot undo COLD
retirements or committed Work. For rollback prefer the new binary with F2/rolling
disabled and a normal drain; old-binary rollback needs a separately validated
consistent backup restore. No such production rollout/restore is performed here.
