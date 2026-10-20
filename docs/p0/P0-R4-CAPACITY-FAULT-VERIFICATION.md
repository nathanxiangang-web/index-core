# P0-R4 isolated capacity and real crash verification (Issue125)

Status: **PARTIAL /50k matrix BLOCKED by the bounded time budget**. This is NOT
Architect acceptance or permission to merge. Ordinary green CI cannot clear the
missing50k results. Request an Architect-approved safer time/resource allocation
before any heavy rerun; do not reduce correctness checks or start R5.
Authorized baseline: `f5df97e3f6ec5d54389da39debf0c5937a3d3d9b` (R3 accepted,
merged-main CI39389632052). Parent120 and Issue125 remain OPEN. No R5, production
deployment, provider writes or CloudSite change is included.

## Modules and extension constraints

- `testutil/r4guard`: test-only authorization/instance attestation. Before ANY
  new R4 destructive fixture or reused legacy scale reset, require explicit
  opt-in, purpose-named DB, exact isolated host/user/port, no TLS fallbacks, and
  a protected regular attestation file. SELECT-only verification checks PG18,
  server startup nonce, physical system identifier, DB OID and an out-of-public
  marker bound to the exact Docker container. DSN text alone never authorizes
  a reset. Negative tests mutate each identity field and reject unsafe DSNs.
- `scripts/p0-r4/run-isolated.py`: owns only its labeled container/network and
  isolated workspace. Verifies container ID, labels, mounts, limits and absence
  of published ports before lifecycle actions. Supplies a read-only attestation,
  mock-only environment and resource/time budgets. No production config mounts.
  Keeps source, logs and data on failure/stop; never recursively deletes paths.
  Explicitly refuses optimized Python (`-O` /PYTHONOPTIMIZE) before parsing or
  workspace/Docker access, so its safety assertions cannot silently disappear.
  CI checks that refusal separately, then executes the normal positive path.
- `internal/runtime/scale/p0_r4_linux_test.go`: independent nested source oracle,
  real Store/Coordinator and public query contracts, exhaustive identity checks.
  Mock snapshots are deliberately synthetic; they do not certify a live provider
  or substitute a fake Kernel/Store implementation.
- `p0_r4_fault_linux_test.go`: owns and kills its exact subprocess, pauses at
  deterministic persisted/transaction fences, and verifies retained truth plus
  idempotent recovery. Only the Python owner stops/restarts its attested PG18.
- Existing R2 HTTP adversarial tests and R3 diagnostics remain unchanged and are
  rerun. Production Collector/Kernel/Store/runtime, contracts and migrations are
  unchanged. New probes must use these existing interfaces, retain the guard,
  remain serial, and report missing observations instead of inventing authority.

## Safety, resource envelope and reproduction

Execution host: designated `.50` (192.168.178.50), not public runtime. Host RAM
7,889MiB; initial available RAM6,763,319,296 bytes and disk19,305,725,952 bytes.
Go1.27.1; PostgreSQL18.6. Dedicated internal Docker network, no published DB
ports, no host networking, no provider credentials. Only one R4 phase at a time.
Runner2CPU/2GiB/PIDs256/GOMEMLIMIT1500MiB; DB1CPU/768MiB/PIDs128,
shared_buffers64MB/max_connections40. Test pool4 connections. PostgreSQL's
default READ COMMITTED applies to normal writes; R3 uses its existing bounded
READ ONLY REPEATABLE READ. fsync/synchronous_commit were observed ON; only
plpgsql is installed in pg_extension. Prepare requires >=10GiB disk and >=4GiB available
RAM. Every phase monitors DB disk against4GiB, logs against32MiB, and wall time
against25min (compile10min); Go test deadline20min. These are stop budgets,
not provider-performance guarantees. Shared services are never stop targets.

```sh
# On .50, from a clean checkout of this PR (never a production checkout/DSN).
# An existing verified Go1.27.1 test image can be supplied with --image.
runner=scripts/p0-r4/run-isolated.py
lab=/home/nathan/indexcore-r4-125-review
python3 "$runner" prepare --workspace "$lab" --image indexcore-p0-r2-test:go1.27.1
python3 "$runner" compile --workspace "$lab"
python3 "$runner" smoke --workspace "$lab"       # full 2k, NOT --ci-smoke
python3 "$runner" capacity20 --workspace "$lab"  # requires same-source 2k PASS
python3 "$runner" capacity50 --workspace "$lab"  # requires same-source 20k PASS
python3 "$runner" legacy20 --workspace "$lab"    # existing flat harness
python3 "$runner" fault --workspace "$lab"       # real SIGKILL + owned DB restart
python3 "$runner" regression --workspace "$lab"  # full enabled PG18 + race
python3 "$runner" compile --workspace "$lab" --race
python3 "$runner" smoke --workspace "$lab" --ci-smoke --race
python3 "$runner" stop --workspace "$lab"        # stop only owned DB; retain data
```

`prepare` refuses workspace overwrite and builds a separate tracked-source copy.
All destructive phases require the attestation; never invoke the old scale test
against a shared DSN. The legacy test's old standalone command now requires this
same guard. Use `status` for the exact owned environment. A CI smoke marker
deliberately prevents subsequent heavy runs in that workspace; use a fresh lab
to run capacity after CI smoke. Heavy tests are not normal-CI acceptance.

## Capacity methodology

Fixture is constructed BEFORE Canonical state. N counts both files and folders:
three prefix-adjacent/sibling branches `/a`, `/ab`, `/other`, groups of20 entries,
1..5 nested group folders, plus root tail files. Exact depth distribution is
emitted per N. Stable provider IDs and distinct content hashes are synthetic.
The identical-content copy has a different provider ID and must remain distinct.
No fixture is reconstructed from Canonical or a production resource count.

Every checkpoint exhausts Q6 pages257, Q5 for every PRESENT path, Q4 children
pages127 for every parent, Q8 pages503 from sequence0 through the full journal,
and Q3 for every historical identity including tombstones. It checks hierarchy,
size, unique paths/IDs, ADD/REMOVE transition legality, monotonic generation and
sequence, exact independent source inventory and stable survivor identity.
Repeated checkpoints execute three additional identical observations and require
no new event, ID or generation. It does not merely compare total counts.

Cold/full/noop/scoped timings start before CreateDraftSnapshot and end after
SubmitAndAdmit/ProcessHead; sorting/collecting mock entries and verification are
excluded from those pipeline samples. Delta log's separate elapsed value includes
all changed scopes AND complete verification. Changes are0.1% and1% of initial
N (minimum1), distributed across sorted file paths and their direct parents.
One pipeline sample per scope; root comparison is a full positive refresh after
delta, not a claim that doing the same update via root is always faster/slower.

The synthetic source helper actually visits all N map entries per scope; it
reports this O(N*scopes) work, not O(changes). Reconcile can load existing root
truth, and query verification itself is exhaustive. Reported API page/sample
counts describe effective client/database work; physical SQL plans, WAL/IOPS and
statement-level p95 are UNKNOWN unless separately measured. No pg_stat_statements
extension is installed. Existing scheduler/discovery/CAS and HTTP authority
matrices are rerun at bounded fixture size, not misrepresented as50k HTTP tests.

Latency quantiles: monotonic wall clock around individual successful API/pipeline
calls, sorted sample index floor((n-1)*p), with actual sample counts. A one-sample
cold p95 is the same single value, not an estimated population percentile. RSS
is Linux getrusage(RUSAGE_SELF).Maxrss high-watermark in KiB (not Go heap).
Container sampled mean/peak memory and CPU% are Docker stats sampled about every3s;
short phases can have zero samples. CPU-time is OS user+system, separate from
sampled Docker CPU%. Database size includes retained snapshots and all fixtures,
and is cumulative within this lab, not compacted production steady state.

## Scenario matrix and limitations

Execution results and immutable receipts are recorded below after each run.
No ordinary green suite is evidence that an opt-in probe ran.

| Scenario | Execution/result |
| --- | --- |
| Purpose DB / nonce / physical instance / OID guard | PASS, positive and negative real-instance checks |
| Nested2k /20k | PASS, full nested matrix |
| Nested50k full matrix | BLOCKED: raw test FAIL/exit2 at20min, incomplete matrix |
| Legacy flat20k baseline | PASS40.738s, original assertions unchanged |
| True deletion, fresh-ID reappearance, equal-content copy, siblings, three cycles | Part of each nested run |
| PARTIAL omissions / trusted root direct-child boundary | Part of each nested run |
| Real process SIGKILL at7 fences | PASS initial run; final receipt below |
| Owned PG18 stop/restart during Journal transaction | PASS initial run; final receipt below |
| R2 HTTP virtual/masked/topology/timeout, delayed generation/config/time, rollback | PASS, unchanged full suite + R2 race |
| Scheduler/discovery/CAS/retry/watch | Reused existing bounded regression, not50k scheduling certification |
| R3 cap at scale | Part of each nested run; no false OK for capped inventory |
| Real115 mutation / real OpenList failures / production rollback / soak | NOT_RUN, excluded |

Reused coverage is explicit, not counted as newly added R4 scenarios:

| Concern | Existing tests rerun without changes |
| --- | --- |
| Actual fake-HTTP refresh/namespace/topology authority | `TestP0R2UntrustedFailureMatrixAndSourceRecovery` (19 faults, repeated4 times each) |
| True subtree/empty-dir deletion, move/rename, reappearance, equal-content copies | `TestP0R2TrustedSourceChangesAndThreeCycleReplay` (8 changes, three replay cycles) |
| Root proof cannot remove absent deep descendants | `TestP0R2ParentCoverageCannotInferDescendantAbsence` |
| Delayed/reordered old generation/config/time | `TestP0R2DelayedObservationFencesAndThreeCycleReplay` |
| Journal trigger rollback / fresh-client recovery | `TestP0R2JournalRollbackAndFreshStoreRecovery` (NOT a process crash claim) |
| Bounded recovery, history, stale retry CAS | `TestP0R2RetryPolicyMatrixPreservesHistory` |
| Discovery rollback and stale claim | `TestDiscoveryCompletionAtomicRollbackAndStaleClaim`, `TestDiscoveryScopeBoundaries` |
| Due selection, budget, stale-CAS consumes budget | `TestP6DueSnapshotHardBound`, `TestP6CASStaleSkipsAndContinues`, `TestP6StaleConsumesBudgetNoReplacement` |
| Retry selection/watch attribution | `TestP10ListDueRetryWorkAllowlist`, `TestP3DueWatchSelection`, `TestP3WatchAttributionSuccess` |
| External provider/consumer lag cannot be invented | R3 diagnostic tests and unchanged UNKNOWN/NOT_TRACKED output |

These reused bounded cases do not prove scheduler latency under50k due work
items or all failure combinations simultaneously with50k Canonical rows.
No fixed watch-lag SLA or real-provider throughput is claimed.

### Crash mechanics and state distinctions

Each process case seeds live survivors plus an existing removed identity, then
starts one exact test-binary subprocess in the same disposable instance. Parent
waits for an explicit fence, reads durable state while child remains parked,
sends SIGKILL to that owned PID, and verifies the OS signal on wait. stdin stays
open so a parked child cannot accidentally continue on EOF. Writer-lock cleanup
is bounded5s; no second writer is made active while the child owns the lock.

Fences: before Draft, after Draft, legacy SUBMITTED without admission,
admitted-not-applied, after Journal INSERT inside uncommitted reconcile,
after COMMIT but before returning the result, and claimed IN_FLIGHT.
DRAFT stays inert until an explicit operator-style submission (not automatic
Draft recovery). A sole legacy candidate is recovered by the existing resolver.
Pending admissions resume once; committed output has no second pending head.
IN_FLIGHT uses the actual RecoverStartupInflight API, preserving attempt history
and clearing the claim without inventing a provider failure. Three replay cycles
must produce no duplicate events. Old tombstones and survivor IDs are checked.

DB case stops only the owned container after a successful Journal INSERT while
the transaction is open. Reconcile must fail; bounded diagnostics while down
must be DEGRADED/incomplete. Restart same container/data; re-attest physical
identity, verify prior Canonical/Journal before replay, recover the durable
admission once, and check three no-op replays. This is a real DB process restart,
not backup-restore or production rollback certification. Startup readiness wait
is separate from reported recovery-after-ready duration.

Two initial harness failures are retained rather than hidden: a test migration
pool explicitly listing pg_catalog first accidentally targeted CREATE there;
test-only search_path now uses public (implicit pg_catalog resolution first).
An exec.Cmd with nil stdin read EOF at crash fences; a held StdinPipe fixes the
test barrier. Production diagnostics/search_path and all core implementations
are unchanged. No confirmed core defect has been established by these failures.

## Normal CI / heavy skips

Existing normal CI retains PostgreSQL18 full enabled suite, format/vet/build,
runtime/app and R2/R3 race. A separate15min CI job creates its own internal PG18
lab, runs the guard, compiles race, and executes bounded200-entry nested R4
correctness. It never runs heavy capacity or destructive DB/process probes.
Artifacts include redacted logs/results only, not owner files, attestation,
database, environment or credentials. `persist-credentials:false` is used for
the isolated-smoke checkout.

The16 inherited skips are retained and accounted for in
[R2 verification](P0-R2-VERIFICATION.md):6 real-provider flag probes,8 unavailable
rclone probes,2 optional fixtures (one is legacy scale, executed separately here).
New heavy capacity, process-crash, DB-restart and real-attestation-mismatch probes
explicitly emit NOT_RUN when their isolated environment/profile is absent. These
are additional skips in ordinary CI, not hidden passes. CrashChild's inactive
helper return is never counted as a crash result. Final observed skip counts and
separate heavy executions must be read together.

## Execution receipts

Test/runner implementation checkpoint: `e444cc4`. All six changed Go/runner
blobs were byte-hash compared against the actual copied source on.50; all match.
Source-tree fingerprint for completed2k/fault/20k/50k/legacy and first regression:
`2187bb44f6c43e7fe6725f7b19ead7df2e8f85ebf9bdb1a16b2457d507d07cf9`.
This includes non-Markdown copied source; final report/CI hook changes do not
retroactively change an old receipt. Final review HEAD/CI is recorded separately.
Test image ID: `sha256:a0d9851bc0031edd71c37420cff4d681f2f2894764c72346b40e601910785fd0`.
The final runner adds only the early optimized-Python refusal after this heavy
checkpoint; normal phase logic and every tested Go blob stay byte-identical.
Final full regression/guard/race smoke is repeated with that refusal and final
CI hook. Heavy results retain their original fingerprint, not a rewritten HEAD.

Completed early receipts (whole R4 still pending):

| Phase | Result / wall time | Redacted log SHA-256 |
| --- | --- | --- |
| Guard tests + compile | PASS1.161s | `6a1a8daecc4c5af2a040d321da2d974083e3614fe8c1574689d4acde2c0e1289` |
|2k full nested | PASS34.657s | `250bcc2cc7dc0fe4464250d70e13de52a63e77f3df51619503fc144fb4d97c18` |
|20k full nested | PASS969.297s | `bb46e032785a4f246a17b16da506d6c1b84bad990425af33b5be6ef8ffe11cbd` |
|50k bounded attempt | TIMEOUT1200.360s, raw exit2, NOT PASS | `fc764f871e53e7e6100283b76c65f4680f67a177977053f59ab302619fa381a0` |
| Legacy flat20k | PASS40.738s | `3ce6c4fcbfb24a26122266d894d97ce4c29286e265f3a8be9064966dcf669aca` |
| Full format/vet/build/enabled PG18 + runtime/app and R2/R3 race | PASS172.346s | `d64a744da2752ef6940b2edde2f56eb6b8d59b797c1560af2720c9dc74c60f76` |
| Bounded200 nested race | PASS5.712s | `4c29a8b934f7d3fec6209da98fcca5b82cf56a0c8bfd036fe9d448b3ee4eff0b` |
|7 process crashes + PG restart | PASS2.805s | `38107d7888cfbf218563318cd12b848de40167372832239f413573e5236b90b8` |

2k exact fixture:298 folders/1,702 files. Depth histogram (slash count):
1:20,2:99,3:459,4:419,5:379,6:339,7:285. Final DB size22,329,023 bytes;
process peak RSS28,048KiB; user CPU0.191664s/system CPU15.482058s. Docker11
samples: peak15,802,040.32 bytes/mean13,171,544.44 bytes, CPU mean47.1873%.
OS RSS and Docker sampled working set differ in accounting and sample timing.
Total mock source map visits48,008; identity/hierarchy/Journal mismatches0.
Cold pipeline1,905.940ms(1sample); final full-refresh pipeline538.331ms(1).
Q5 latency p50/p95/p99=.293533/.443286/.541702ms(n58,011);
Q4=.558181/.760412/.888411ms(n8,671);
Q6=2.095594/2.924482/3.169283ms(n232);
Q8=.426469/.775254/1.011326ms(n164).
These samples include repeated complete checks, not58,011 distinct files.

20k exact fixture:2,998 folders/17,002 files. Depth histogram:
1:20,2:999,3:4,599,4:4,199,5:3,799,6:3,399,7:2,985. Final PRESENT20,001;
Canonical all20,002 (one retained tombstone), Q8 events20,223, generation18,
36 snapshots/admissions and100,552 persisted snapshot-entry rows. These exact
SQL row counts are database work/cardinality evidence, not a count of physical
SQL execution plans. Source map visits700,008; mismatches0. DB91,141,823 bytes;
OS peak RSS132,804KiB, user CPU4.176558s/system CPU179.695378s. Runner Docker316
samples: peak126,143,692.8 bytes/mean70,757,543.47 bytes, CPU mean20.0409%.
A point-in-time DB pressure sample during this run observed96.17% CPU and
234.8MiB working set under its1CPU/768MiB limits; this is NOT a peak or mean.

|20k operation | Samples | p50 ms | p95 ms | p99 ms |
| --- | ---: | ---: | ---: | ---: |
| Q4 page |87,145|.392756|.583467|.715653|
| Q5 path |580,011|1.587457|1.736005|2.005917|
| Q6 page |2,262|5.463881|8.248139|13.228261|
| Q8 page |1,208|.482551|.731423|1.117513|
| Cold pipeline |1|42,561.400|42,561.400|42,561.400|
| Scoped-delta pipeline |14|109.614232|136.443418|136.443418|
| NOOP pipeline, mixed root/scoped |15|97.370664|2,619.779079|2,619.779079|
| Full-refresh-after-delta pipeline |1|4,425.391|4,425.391|4,425.391|

0.1%:20 files/2 scopes/40,000 map visits,36,978.881ms including exhaustive
verification.1%:200 files/12 scopes/240,000 visits,38,472.164ms including
verification. Q8 changed exactly20 then200 times; unrelated identities survived.
True delete/reappearance/copy added one event each. Root-only authoritative and
empty non-authoritative PARTIAL observations kept all surviving IDs. Three-cycle
checks after each relevant change and final full refresh produced no duplicate
events/generations. NOOP samples mix root and scoped workloads; do not compare
that median against a single full-root observation or treat it as a fixed SLA.
The580,011 Q5 calls (full oracle checks repeated29 times) dominate total probe
time; total wall duration is not one user query or one ingestion latency.

###50k: incomplete, do not promote to capacity acceptance

Fixture50,000 =7,498 folders +42,502 files, independently confirmed in SQL.
Cold full check, before-repeat full check, and two completed repeat checks all
reported PRESENT50,000 /all IDs50,000 /Q8 events50,000 /generation1 /mismatches0.
During the third repeat's exhaustive Q5 validation the Go20min alarm fired.
The captured stack is `r4Lab.noopCycles -> verify -> QueryReader.ResolvePath ->
pgxpool.BeginTx`. This is a matrix wall-time exhaustion, not a reported identity
assertion failure or proof of a particular SQL/index defect. The exact SQL-plan
or scheduling cause of latency is NOT established; do not invent one or insert
a core performance fix into this test-only branch.

Post-stop exact state:50,000 PRESENT /0 REMOVED,50,000 Journal events,
generation1,4 snapshots,200,000 snapshot-entry rows, DB158,275,263 bytes.
PG container remained running and OOMKilled=false. Runner391 samples: peak
269,693,747.2 bytes/mean166,827,637.07 bytes, CPU mean9.4794%. A separate DB
point sample observed101.58% CPU/244.5MiB under its1CPU/768MiB envelope.
Final OS RSS/CPU-time, full latency quantiles and final source traversal totals
are UNKNOWN: final R4_CAPACITY emission was not reached. Never substitute the
container samples for missing OS metrics. No OOM or physical disk exhaustion
was observed; no claim that one product request needs20min is justified.

50k0.1%/1% deltas, delete/reappear/copy, later coverage boundaries and remaining
three-cycle checks are **NOT_RUN**, not inferred from20k PASS. No retry was made.
Before the smaller bounded regressions reset public, an owned-only custom-format
pg_dump retained this exact isolated state privately; SHA-256:
`325f536cbc97d6873cc740a7b7a5ddc5aa220a3fed30d249ebbd21a5c8eec1df`.
It contains test attestation material and is NOT uploaded. It was not used for
recovery; backup restore and production rollback remain NOT_RUN.

### Legacy comparable baseline (not the nested matrix)

The original flat20k harness was reused unchanged except the mandatory guard.
It passed:20,001 PRESENT,20,002 events,3 snapshots,20,001 rows paginated,
46.1MiB DB and1.7MiB sampled Go heap (not peak RSS). Existing assertions were
not weakened. Docker13 samples: mean45,044,405.17/peak76,567,019.52 bytes,
CPU mean20.9808%. This harness does not provide OS high-watermark/CPU-time or
query quantiles; these remain unavailable for this legacy execution.

| Flat20k operation | Historical Gate3 report | Post-fix R4 observation |
| --- | ---: | ---: |
| Cold full ingestion |16.29s|10.783338s (draft2.270831s/admit2.484307ms/reconcile8.510017s)|
| Identical full repeat |12.91s|13.244972s (draft2.104183s/admit1.122830ms/reconcile11.139663s)|
| Full-input small delta |17.00s|16.152998s (draft2.251033s/admit1.058803ms/reconcile13.900904s)|
| Q6 pagination |.143s|.121274s|

Historical source: [Gate3 verification](../gate3/GATE3-ALPHA-VERIFICATION-REPORT.md).
Both time the real Draft->admission->Coordinator pipeline on PG18.6, but old
host/resource/cache details are not controlled; do not claim a causal speedup,
regression factor or frozen SLO from this single comparison. Flat20k has no
nested folders and far fewer query-oracle calls than R4's nested matrix; the
40.738s vs969.297s totals are different workloads, not a product slowdown factor.

Process recovery times in fence order:29.457/29.004/30.028/26.643/27.221/
17.064/18.262ms. First six cases Q8 tail4->5, IN_FLIGHT4->4. Every case
retained the removed seed identity and survivor identities, observed actual
SIGKILL, and completed three no-duplicate replay cycles. Owned DB stop/restart
count1/1; recovery-after-ready41.499ms, Q8 tail4->5. Down diagnostic was
non-green; uncommitted changes did not escape. No production rollback claimed.

Full enabled.50 suite observed19 skips:16 inherited, plus the explicit heavy
R4 capacity/process/DB profile skips. Real identity-mismatch tests executed here
because the isolated attestation was supplied. Ordinary GitHub's shared full
suite has one further explicit real-attestation NOT_RUN, while its separate
guarded R4 job exercises that guard and the200-resource race smoke. These skips
do not erase separately recorded heavy results, especially the50k timeout.

Final head validation/CI receipts are published in the PR, not implied from a
previous run. This section is evidence, not acceptance.

Final harness repeat after optimized-Python refusal and CI hook:
source fingerprint `4b69f4e064cee9e47b056f4096bbe6ba6e5436af03d825f61d0b5288eafc16af`.
Format/vet/build/full enabled PG18 + runtime/app and R2/R3 race PASS140.050s,
log SHA `f4d5a2635ad8037a315a4b8910c5dc1465b5e087462e10184e242b635b7ee59d`.
Guard tests + race compilation PASS.893s, log SHA
`80a4c30889586fa43f2a8e86576c5bb8ec766ade938f2541b231211e48c6a390`.
Bounded200 nested race PASS5.842s, log SHA
`4cbe906671d02ae202374f8c9464ede739e0cf1f541d34deb24f720dc6120971`.
The separate optimized-interpreter negative test refused with exit2 and the
expected fixed message before any workspace/Docker access. All Go blobs are
unchanged from the heavy-tested implementation checkpoint. This final repeat
does NOT rerun or clear the50k incomplete matrix. Dedicated PG18 is stopped
after validation; private source/log/data/dump artifacts are retained.
