# Issue134: production-policy scheduling replay

State: IN_PROGRESS / independent Architect review required. No self-merge.

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

## Results and decision

Execution is underway. Measured tables, raw-event hashes, local verification,
exact-head CI and explicit PASS/FIX/NO-GO dispositions will be added after the
recorded runs. No performance acceptance is claimed by this initial document.
