# Issue138 first PR: bounded read extraction and ordered commit proof

Status: IMPLEMENTATION CANDIDATE / isolated proof, not Architect acceptance.
Runtime pool, production canary and whole-Issue138 completion: NOT DONE.
Proposal: [ADR010](../decisions/ADR-010-BOUNDED-FETCH-SEAM.md).

## Original extraction baseline and prerequisites

Baseline main c5bb6a68c5f4b8b98d28d348a604f4b2f82c361a. Main CI40070457955
attempt2 SUCCESS, after one controlled failed-job rerun. Failed attempt1 stays
retained; original Hint startup cause UNKNOWN. Independent test-only PR139 head
5f8a55daadbea8455322fec931797c052b6c8b1a CI40072203409 SUCCESS was separate
from the initial extraction head0da97cd6. After PR139 was accepted and merged,
this branch incorporated main07492838128f through merge commitbb6c866, without
rewriting published history. No PR126 or Issue133 work is incorporated.
Task: Issue138, superseding the earlier research-only deferral of read concurrency
in Issue134 comment6381017272. F4 scheduling and Watch lifecycle remain separate.

### Post-PR139 synchronization boundary

The original prototype CI40076090617 SUCCESS is verified, but is not acceptance
of the synchronized HEAD. The only merge conflict was the appended project-state
ledger: both prerequisite and extraction records are retained. The imported Go
changes are exactly PR139's test fixtures; no additional runtime implementation
changes were made during synchronization. Validation is rerun on the synchronized
code in an owned PG18 environment, with fresh logs separate from the original
extraction evidence below. Updated exact-head CI and independent Architect review
remain gates. PR140 stays Draft, unmerged and undeployed.

The source/transaction review reconfirms: provider I/O ends before commit locks;
the handle is opaque with shared single-consumption state; both Stage1 and Stage2
validate captured root/config/parent/claim and original source time; the legacy
unfenced path and trusted deletion rules remain unchanged. Constructor references
outside its definition remain test-only. This is a scoped executor cross-check,
not independent Architect acceptance or certification of same-root concurrency.

## Modules and dependency direction

```text
legacy ScanScope -> private readScope -> private commitScope (serial wrapper)

unwired FetchPrototype -> readScope [concurrent across roots only]
                       -> opaque FetchedScope (bounded facts + source time)
                       -> serial commitScope
                       -> existing DRAFT -> SubmitAndAdmit -> Coordinator/Kernel
                       -> existing Canonical + per-root Q8
caller -> existing ClaimWork and typed completion/startup recovery
```

Fetch reads Store metadata and the provider, never mutates Work, Watch, Snapshot,
admission, generation, Canonical or Journal. The immutable handle exposes only
scalar timings/counts. Value copies share one consumption token. Foreign, stale,
discarded and duplicate callbacks cannot consume another owner's handle or
release a newer in-memory reservation. Discard changes memory only.

The prototype bounds retained observations to1..8, one per root,1000 direct
children and8 scope levels; each read/commit has a finite context budget. The
test-only driver has N read goroutines, <=2N queued jobs and <=N returned results;
each reader waits for serial commit acknowledgement before reusing its slot.
There is no app/CLI/config wiring, runtime Worker Pool, new Job Engine or migration.
No environment flag can enable this prototype in the production application.

### Important single-root limitation

This first proof does NOT pipeline multiple scopes in one Root. Doing so would
let an earlier commit invalidate a later fetched observation's captured generation
and directory-coverage authority. The prototype rejects the second same-root
reservation before source I/O instead of rebasing generation or observed_at.
Cross-root acceleration is not evidence that the current single production Root
is faster. A later per-root ordered lineage design needs separate review before
more than one speculative same-root observation is admitted.
The driver claims only one scope per root per wave. The extraction API is NOT a
durable cross-instance claim sequencer; callers must not preclaim another scope
in that root while its earlier observation/claim remains unresolved.

### Authoritative transaction checks

Optional `scoped_fetch_fence` Snapshot provenance carries version, captured root
generation/admission frontier, adapter digest + bounded-lifetime tuple revision,
canonical parent ID/path, and the exact Work claimed sequence/attempt/start time.
Both short Stage1 and final Stage2 check it under Root -> adapter -> Watch -> Work
locks. A late Hint may change pending version/sequence without revoking the old
claimed prefix; it is preserved for subsequent work, not falsely acknowledged.

Before-admission mismatch leaves at most an inert DRAFT. Post-admission mismatch
uses existing STALE_INPUT terminal handling, never half-applies canonical changes.
PostgreSQL clock_timestamp checks original source observed_at against120s/30s
past/future bounds. Its microsecond precision survives storage; commit does not
invent a newer source timestamp. The existing Collector topology before/after
checks and directory-coverage validation remain authoritative; no second source
list is made inside commit. Unknown topology does not gain absence authority.

Adapter xmin is a conservative physical tuple revision over that120s lifetime,
not a permanent logical epoch. Config rewrites/maintenance may invalidate a read.
No durable global request ledger, admission queue replacement or hidden table was
added. Fetch-slot/one-use protection is process-local: callers must use ONE fetch
per durable claim attempt and existing recovery before a later attempt. This is
not persistent crash-safe upstream request accounting across arbitrary instances.

Legacy Snapshots without a fence keep the accepted path. Rollback-to-serial means
staying on a fence-aware binary and using legacy ScanScope, not downgrading to an
old executable while fenced SUBMITTED Snapshots remain. An old binary cannot be
assumed to understand this new optional provenance. No binary rollback is certified.

## Isolated evidence and reproducibility

Run on ssh dsh/.50, dedicated owner-labeled index138-fetch-pg18, PostgreSQL18-alpine,
CPU1, memory1GiB, pids128,512MiB tmpfs, restart=no, dynamic loopback port. Go1.27.1,
GOMAXPROCS2/GOMEMLIMIT512MiB (soft Go target, not a hard Go memory cap). Purpose
guard requires exact loopback index138_test, owner/current role, PG18 and database
comment indexcore-issue138-owned-disposable BEFORE any schema reset or population.

```sh
export INDEXCORE_ISSUE138_DISPOSABLE=owned-disposable-only
# INDEXCORE_TEST_DATABASE_URL must name that already purpose-marked owned DB.
go test -race -p 1 -count=3 -timeout=3m -run '^TestIssue138' ./internal/runtime/scan
INDEXCORE_ISSUE138_SCALE=2000 go test -race -p 1 -count=1 -timeout=5m \
  -run '^TestIssue138CapacityReadPrototype$' ./internal/runtime/scan -v
# Repeat separately with20000 and50000. Never fall back to a shared/live DB.
```

The matrix uses8 Roots,400 real aged durable Work rows, and2k/20k/50k SQL-preloaded
canonical directories evenly distributed among roots. These are NOT2k/20k/50k
successful source scans or a full traversal. Each case fetches/commits16 scopes
over two finite waves, through actual forced HTTP with1250ms wall-clock delay.
The1% offered change set is20/200/500; only16 targeted changes are observed, leaving
4/184/484 unobserved.384 background rows stay PENDING. No steady queue slope,
eventual full-population freshness, sustained workload or soak is established.

For every N=1/2/4/8, assert16 actual requests,0 duplicates, peak source requests<=N,
16 VERIFIED/384 PENDING, correct canonical count, Q5 identity, exact producing
Snapshot, one corresponding Q8 addition per file, ascending per-root generation
and event sequence, no extra/removal Journal. The four-reader source-latency-heavy
fixture must exceed1.5x its same-fixture serial baseline or records NO-GO/fails.
Raw JSON receipts and final numbers are in the associated PR/Issue138 Worker report.

### Local measured matrix

[Raw receipts](evidence/issue138-fetch-receipts.ndjson) contain12 capacity records
and3 resource checkpoints. Each capacity cell below is a16-scope batch,8 Roots,
NOT a full-population traverse. All12 integrity cases passed, including the
four-reader >1.5x threshold.192 actual forced lists belong to these12 reported
capacity cases only; regression HTTP calls are not included in that count.

| Preloaded directories | 1 reader wall seconds | 2 readers | 4 readers | 8 readers | 4-reader speedup |
| --- | ---: | ---: | ---: | ---: | ---: |
| 2,000 | 20.555 | 10.388 | 5.382 | 2.986 | 3.819x |
| 20,000 | 21.133 | 10.716 | 5.700 | 3.514 | 3.708x |
| 50,000 | 22.072 | 11.270 | 6.305 | 4.487 | 3.501x |

The raw receipts are authoritative for exact values; these are rounded display
values. Four-reader batch detect-to-Q5/Q8 p95 is5.382/5.699/6.305s respectively,
each N16; its read p95 is1.264/1.268/1.274s and commit p95 is0.042/0.073/0.140s.
No sampled blocked lock was observed; temp_files/temp_bytes checkpoints were0.
Peak source concurrency equalled1/2/4/8, duplicates were0, and each case retained
384 PENDING work rows. None of this establishes a steady queue/freshness bound.

For the three full four-concurrency runs including setup, owned PG observed CPU
peaks33.10/62.52/82.18% and memory peaks389.6/389.3/390.0MiB. Go-command high-water
RSS was239572/236340/236092KiB. Docker refresh frames may repeat; these are
checkpoint/campaign observations with cached prior tests, not per-worker memory
or independent statistical samples. Request rates increased to178.4/168.4/152.3
forced lists/min at4 readers: fake-source benefit is NOT permission to send that
rate to real115 Open. No production upstream budget is established.

Measured read time includes metadata reads and source parsing; commit time includes
Store, Coordinator, Go and client round trips, not an isolated pure DB critical
path. Batch detect-to-Q5/Q8 quantiles have N16 correlated samples; nearest-rank p95
and p99 are the sample maximum, not general first-discovery SLA percentiles.
Summed HTTP wait overlaps under concurrency; it is not elapsed wall time.
DB lock waits are point samples, not proof that no transient blocking occurred.
Database/temp statistics are observed at checkpoints, not per-operation budgets.
`/usr/bin/time -v` measures Go command/toolchain + child process high-water RSS,
not only steady fetch-result memory. Container metrics concern only the owned PG.

Correctness regression covers read-only fetch, immutable handles, copied/foreign
handles, duplicate concurrent commit, entry/depth caps, config ABA/removal, root
generation/lifecycle changes, claim recovery, late Hint pending prefixes,
expiration by DB time, changed config BETWEEN admission and reconcile, preserved
parent identity under path reuse, cancel-before-admission, known source429/5xx/
decode/timeout/cancel, plus virtual-only topology vs qualified mirror deletion.
Restart evidence reopens Store at a real durable admission boundary and uses
unmodified recovery; it is a process-boundary simulation, not an OS crash test.

Local final validation: vet/build PASS; full PG18 suite24 packages,737 passing
test events (including subcases),22 optional skips,0 failures. The13 dedicated
prototype methods plus their10 subcases passed3 race repetitions, including the
final mirror regression. The four-package race suite (incrementalruntime, app,
scan, postgres) also passed; the final added mirror method has its separate
three-repeat race proof. The22 full-suite skips are inherited real-provider/
rclone/live/reference/optional scale/purpose-gated Issue134 crash fixtures plus
the opt-in Issue138 capacity case; capacity was run separately at all3 sizes.
These local counts are not a GitHub CI or full-Issue138 acceptance claim.

## Remaining gates / NOT_RUN

- No same-root read pipeline, production request-rate cap or provider-specific
  adaptive429 fallback; no real115/OpenList concurrency or provider-safety claim.
- No F4 capacity/freshness-debt ledger, rolling scheduler or Watch retirement.
- No new-fence EXPLAIN/continuous lock monitoring, full2k/20k/50k source traversal,
  sustained1% churn,30–60m soak, OS process crash, real upstream or production canary.
- No new scheduler fairness proof from these targeted claims; existing selector,
  aged NORMAL/root/Hint/retry regressions remain separate and unchanged.

The broad Issue138 end-state is NOT complete. First extraction/proof PR requires
exact-head CI and independent Architect review before any bounded runtime pool.
F1/F2 production defaults and all existing services are untouched. Keep134/138 OPEN.
