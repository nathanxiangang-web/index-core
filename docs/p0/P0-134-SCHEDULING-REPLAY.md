# Issue134: production-policy scheduling replay

State: REVIEW / evidence candidate. F1 needs FIX; F2 capacity is NO-GO.
Independent Architect review required. No self-merge or production enablement.

## Verified baseline

- Main: `9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e`.
- Branch: `perf/issue134-f1-f2-replay-evidence`, directly from main.
- PR143 merged as `17b64ba1156291af043d51e6427bc21cd2bb8d6c`;
  its bounded read pool, budgets, cooldown and ordered admission are reused.
- PR145 merged as the current main; the exact stale-head handoff is retained.
- PR146 remains OPEN at `19ba65f4439783b74752e5efb90df5ee94333d3d`;
  its CI42224591510 is SUCCESS. Its four operating documents were read from
  that PR ref; none are assumed present on main or imported into this branch.
- Issues134/138/132/144 and linked F1/F2/F3/read-pool/handoff acceptance records
  remain separate from this Executor's measurements.

## Responsibility and module boundaries

| Module | Responsibility | Dependencies |
| --- | --- | --- |
| app/issue134_replay_test.go | Wall-clock workload, real HTTP/claim/visibility evidence | existing app composition, P10 runtime, owner-marked PG18 helper |
| incrementalexec/mirror_replay_capacity_test.go | F2-only finite 20K debt investigation | existing F3 seed fixture, scanner, sourcebudget, P5/P6 |
| app/issue134_real_readonly_test.go | Separately authorized real-source, additive read proof | exact disposable DB guard, capped read-only proxy, existing scanner |
| scripts/perf | Serial reproduction and independent raw-event analysis | Go toolchain, Python standard library |
| issue134-replay.yml | Separate disposable CI matrix and retained raw artifacts | existing full regression workflow remains required |

No production Go implementation, migration, default, Kernel/Canonical/Journal
contract, writer ownership or historical migration is changed. Tests acquire
the existing database writer lock. Source collection and admission use existing
entry points. No parallel Canonical commit lane is introduced.

Extensions must keep fixture mutation, telemetry and analysis outside production
packages, retain purpose-marker checks before reset, preserve failed evidence,
and keep real-source tests explicit opt-ins with an exact request envelope.
Do not add a new source or provider route to the real-source allowlist without
fresh owner authorization. No runtime Watch-retirement API is invented here.

## Reproducible workload

All three modes use the same 401 NORMAL backlog, operational verified-history
fixture, known canonical directories at generation0, 30s HIGH/HOT root Watch,
12 new directories available after5s, nested new files and an unhinted late file
in an already-read directory after90s. Source metadata stays unchanged.
Run duration is480 real seconds. Reader1 and all three HTTP intervals200ms.
The existing P10 wake5s, cycle5 items/60s, four-cycle burst and1s cooldown apply.

Modes are independent: baseline F1=OFF/F2=OFF; f1 ON/OFF; f2 OFF/120s.
F1+F2 combination is not accepted or attempted before separate gates pass.
The fixture permits only loopback fake OpenList requests. Every source HTTP
request, including identity/topology, is counted at the actual server handler.
HTTP request arrival timestamps approximate client starts on loopback; client
transport timeouts remain NOT_TRACKED. Neither Work nor Watch counts infer I/O.

Q8 is observed every1s, with Q5 identity lookup and producing-generation/Snapshot
validation for every observed event. Visibility time is an upper observation
including up to1s polling delay. Fresh-directory quantiles have at most12 samples;
their P99 is a finite-fixture statistic, not a population SLA. NORMAL quantiles
include actual executed pending epochs; unexecuted work is censored and remains
visible in the raw queue samples. Repeated-scope execution includes intentional
periodic revisits and must not be relabeled solely as redundant work.

Raw NDJSON includes HTTP, claim epoch, discovered, Q5/Q8 visible, queue/debt,
database size/buffer reads/hits/lock waits and summary records. Freshness debt is
sum(max(0, observation time-next_due_at)) across Watch rows, measured in overdue
seconds. It is an age burden, not a count of known changed files.

## 20K gate

The separate F2-only fixture SQL-seeds20,000 nested directories and managed
120s Watches, plus one HOT root. A disclosed logical+121s offset opens the initial
due storm. Subsequent125+ seconds, HTTP1250ms list latency and200ms shared request
spacing are real elapsed time. Only the counted scans are traversed; preloading
20K rows is never called a full traversal. Positive debt growth gives NO-GO.
The existing lifecycle test proves retained Watches after trusted removal and
opt-out; automatic safe retirement remains ABSENT. Manual CAS is not an automatic
lifecycle implementation. A passing evidence test does not make capacity PASS.

## Reproduction

Use Go1.27.1 and a new dedicated PostgreSQL18 instance. The app replay helper
requires database `index138_test`, owned by the connecting role, with comment
`indexcore-issue138-owned-disposable`, exact loopback and `sslmode=disable`.
The20K fixture similarly requires `index134_test` and comment
`indexcore-issue134-owned-disposable`. Never use a shared/production server.

```sh
export INDEXCORE_TEST_DATABASE_URL='postgres://indexcore:indexcore@127.0.0.1:55439/index138_test?sslmode=disable'
export INDEXCORE_REPLAY_EVIDENCE_DIR="$PWD/evidence/unique-run"
bash scripts/perf/issue134-replay.sh

export INDEXCORE_TEST_DATABASE_URL='postgres://indexcore:indexcore@127.0.0.1:55439/index134_test?sslmode=disable'
INDEXCORE_ISSUE134_DISPOSABLE=owned-disposable-only \
INDEXCORE_ISSUE134_REPLAY_20K=1 \
go test -json -count=1 -timeout=8m -run '^TestIssue134MirrorReplay20K$' ./internal/runtime/incrementalexec
```

Evidence files use exclusive creation. Select a new output directory for reruns;
do not truncate a prior failed trace. The shell runner continues other modes
after a failed test and returns nonzero if any failed. CI matrices use separate
machines/databases; the same-host serial run is the comparison experiment.

## Real source boundary

The Owner explicitly authorized an OpenList account and choosing a populated
directory. Authentication and bounded directory exploration succeeded. The
account's topology endpoint returned API403; that is retained as unavailable
proof. No stronger credential, false topology or deletion authorization is
invented. The optional real test permits at most3 POST `/api/fs/list` calls,
Reader1/1s spacing, on the selected source path, with mirror_presence=false.
It checks repeated identity and Q5/Q8 ordering in its disposable database.
It does not inject provider files or certify F1/F2 freshness/scale.
Tokens, source paths/names and provider bodies stay in a private host directory
and are excluded from the repository and public evidence.

## Initial same-host serial results (2026-11-10, retained history)

These initial traces predate the replay-clock correction: the start timestamp
included queue preparation, so a slow host could cross the+5s source-release
threshold before Runtime.Run. They remain diagnostic history, not final proof
of a mid-scan injection. The corrected harness starts its clock after startup
preparation; final-clock reruns and per-head receipts are linked from PR147.
The exact-head CI replay matrix also enables Race for the telemetry itself.

The requested host ran a separate PG18 container, CPU0.5/memory384MiB,
GOMAXPROCS1/GOMEMLIMIT256MiB. These are deliberately disclosed resource ceilings,
not a claim about production PostgreSQL capacity. Each mode ran480 real seconds;
setup/compilation is excluded. All production Go code is byte-identical to main.

| Measured metric | baseline | F1 only | F2 only |
| --- | ---: | ---: | ---: |
| First new directories claimed / visible |0/12 /0/12|12/12 /12/12|0/12 /0/12|
| First discovery -> claim P50/P95/P99 seconds |NOT_TRACKED|59.85/110.58/110.58|NOT_TRACKED|
| First discovery -> Q5/Q8 P50/P95/P99 seconds |NOT_TRACKED|62.61/113.49/113.49|NOT_TRACKED|
| NORMAL claim wait P50/P95/max seconds |243.59/461.45/478.70|269.53/461.11/479.50|242.26/460.15/479.48|
| NORMAL executed pending epochs |326|243|359|
| Actual HTTP / directory lists |1700/340|1405/281|1872/374|
| Repeated scope executions / ratio |13/3.82%|13/4.63%|14/3.74%|
| Max receiver request starts / anchored second |5|5|5|
| Peak source HTTP concurrency |1|1|1|
| Actual receiver HTTP429 /5xx |0/0|0/0|0/0|
| Unhinted late file visible by window end |false|false|false|
| Watch first -> last |1->1|1->1|1->414|
| Final due Watches / debt-overdue-seconds |1/4.19|0/0|322/72800.28|
| PG sampled peak CPU percent |49.77|51.89|43.72|
| PG sampled peak Docker memory MiB |51.18|54.78|54.50|
| PG logical block-hit increase |697767|6049578|722025|
| Max sampled lock waiters |0|0|0|

Zero completed new-directory samples are right-censored failures, not zero
latency. Their P50/P95/P99 cannot be computed. Timeout counts, transient lock-wait
duration, exact client wire-start timestamps and per-admission critical-path
attribution are NOT_TRACKED. CPU/memory are96 samples per mode at about5s intervals;
they are not continuous profiles. Logical hit counters are per-database cumulative
counters differenced between first/last samples; the1s observer is included.

F1 proves real priority effect and all24 fresh/nested files reached Q5/Q8, but
its first-claim P95 exceeds the Issue13460s engineering target, and the old late
file missed the480s window. It is FIX, not overall PASS. NORMAL work progressed;
finite progress does not certify absence of starvation under indefinite arrivals.
The F1 run has about8.67x the baseline logical block-hit increase and17.35% fewer
completed lists. This supports investigating selector/PG overhead, not attributing
all of the slowdown to a particular query without a plan. The500-directory SQL
probe has12 real eligible fresh candidates; the20K probe and500 probe emit separate
actual EXPLAIN/BUFFERS and client timing evidence after their timed workloads.

F2 suppresses some requeue, but cannot make the existing401-work backlog disappear.
PENDING312 is not freshness: new files were still unobserved,322 Watches were due,
and debt reached72800 overdue-seconds. No automatic Watch retirement exists.

## Independent CI repetition and 20K rejection

On code head `d592785ed9175e4d486d624a1332b42e22e59566`, replay matrix
[42805036688](https://github.com/nathanxiangang-web/index-core/actions/runs/42805036688)
completed SUCCESS. Those jobs have separate hosted machines, not the constrained
same-host comparison above. Baseline/F1/F2 first-claim P95 was446.80/63.35/433.79s;
Q5/Q8 P95 was448.56/64.49/435.47s. Actual HTTP2294/2295/2300, concurrency1 and
all12 fresh files visible. These results independently reproduce F1's benefit,
but do not replace the retained host failures or certify the60s target.

The same CI F2-only20K experiment performed60 actual lists /300 actual HTTP in
130.15s. Watch20001->20001, retired0, debt32048.63->2627484.56 overdue-seconds.
Capacity disposition: **NO-GO**. This is a finite due-storm rejection with20K
SQL-seeded directories, not a full20K source traversal or a steady-state soak.
F1+F2 combined validation remains NOT_RUN because the independent gates did not
pass. Shortening all directory cadences is not a remedy.

## Evidence, verification and next boundary

- `evidence/issue134-replay-20261110.ndjson.gz` contains original public trace
  records wrapped as `{run,record}`, including all three host runs, the four CI
  runs, receiver telemetry, resource samples and redacted source exploration.
- `evidence/issue134-replay-manifest.json` records per-stream SHA256/byte counts,
  exact initial-recorder hash and errata. Initial recorder's timeout0 is corrected
  to NOT_TRACKED in analysis; original bytes are preserved. Server arrival jitter
  was initially mistaken for exact budget reservation time; the receiver rate
  envelope is now checked while strict sourcebudget clock tests remain required.
- `evidence/issue134-replay-summary.ndjson` is the compact result index. Initial
  failed host/CI outputs remain available in the task evidence bundle and CI.
- Final per-head full PG18, Race, vet/build, additional SQL plans, real-source
  opt-in and CI receipts are posted in [PR147](https://github.com/nathanxiangang-web/index-core/pull/147).
  Read that receipt for completed checks; this measurement document does not
  convert a pending check or a performance failure into PASS.

At200ms, five real source calls per mirror observation imply an ideal ceiling
near60 observations/minute and at least~400s of request-start budget for400
directories, before SQL/admission/runtime costs. Increasing readers cannot remove
that request budget. F1's one-fast-slot-per-Root/cycle also bounds burst service.
F2 needs separately reviewed capacity-aware rolling cohorts/backpressure and an
identity/epoch-fenced Watch lifecycle. Investigate the measured SQL read burden
with preserved identity, Snapshot, parent/root, Journal and deletion fences.
No production F1/F2/reader switch, second writer or unrelated PR integration is
authorized or included. Issues134/138 remain OPEN.
