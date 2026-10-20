# P0-R4 isolated capacity and real crash verification (Issue125)

Status: execution in progress; NOT Architect acceptance or permission to merge.
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
READ ONLY REPEATABLE READ. Prepare requires >=10GiB disk and >=4GiB available
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
| Purpose DB / nonce / physical instance / OID guard | Pending final receipt |
| Nested2k /20k /50k | Running sequentially; no final result presumed |
| Legacy flat20k baseline | NOT_RUN yet |
| True deletion, fresh-ID reappearance, equal-content copy, siblings, three cycles | Part of each nested run |
| PARTIAL omissions / trusted root direct-child boundary | Part of each nested run |
| Real process SIGKILL at7 fences | PASS initial run; final receipt below |
| Owned PG18 stop/restart during Journal transaction | PASS initial run; final receipt below |
| R2 HTTP virtual/masked/topology/timeout, delayed generation/config/time, rollback | Pending full regression |
| Scheduler/discovery/CAS/retry/watch | Reused existing bounded regression, not50k scheduling certification |
| R3 cap at scale | Part of each nested run; no false OK for capped inventory |
| Real115 mutation / real OpenList failures / production rollback / soak | NOT_RUN, excluded |

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

Pending final runs and exact-head CI. This section is evidence, not acceptance.
