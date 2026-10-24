# Issue134 F2: mirror rediscovery reduction, isolated evidence

Date: 2026-10-24 UTC. Base: f9bbc4e5a98965e6c7773d2453823061bd4b1355.
Status: implementation candidate; NOT Architect accepted or production deployed.
Refs #134, specifically the F2 packet in comment6368575793. F1 was accepted in
PR135; its merged-main CI39979311690 was independently checked SUCCESS.

## Modules and authorization boundary

See [ADR008](../decisions/ADR-008-MIRROR-ROLLING-REVISIT.md) for the dependency
graph, exact coverage proof, opt-in, extension rules and rollback. The executor
only coalesces ordinary mirror rediscovery through an additive store port. The
store enrolls existing P3 watches and completes parent/children in the same
transaction; the orchestrator fairly selects bounded due cohorts. Existing
ClaimWork/EmitDuePoll CAS, serial source reads and Kernel-owned Q5/Q8 remain.
No migration, second queue, Canonical write path or deletion authority is added.

F2 defaults OFF. Valid positive revisit intervals are whole seconds30s..1h;
F1 remains independently default OFF. Existing explicit HIGH discovery subtrees
and operator/adaptive watches are not overridden. A directory mtime is NOT a
subtree revision. Positive canonical changes invalidate reuse; an unchanged
directory requires durable rolling revalidation even without a Hint.

Disabling F2 restores ordinary parent merges but does not delete persisted
watches. Exact owned watch policies can be retired with the existing CAS API,
not bulk SQL cleanup. Interval bounds eligibility, not completion under overload.
The opt-in due selector reserves one ordinary and one managed-watch candidate;
it returns at most six rows and still emits at most five polls per cycle. Its
cohort SQL has bounded output, not a proven bound on physical database reads.

## Owned environment and retained failures

Tests ran via ssh dsh on192.168.178.50, Go1.27.1, GOMAXPROCS2 and
GOMEMLIMIT512MiB. Container index134-f2-pg18 is labelled codex-task=index134-f2:
PostgreSQL18-alpine, CPU1, memory1GiB, pids128, restart=no, loopback-only dynamic
port,512MiB tmpfs. WAL budget64/32MB and checkpoint30s retained fsync ON. Final
observed tmpfs use during the matched latency run was155.9MiB/512MiB, OOM=false.
Only this disposable container was started; existing services were not changed.

Heavy/crash tests require explicit owned-disposable-only opt-in, exact database
index134_test, purpose comment/owner, loopback and PG18 BEFORE resetting schemas
or terminating the tagged blocked backend. Existing Issue130 tests use their
own separate purpose-marked database. Source HTTP is httptest OpenList-compatible
data; PostgreSQL, scanner, Kernel, QueryReader/Q5 and Journal/Q8 are real. No
production credentials, real OpenList,115 calls or downloads are involved.

Retained non-passing attempts are not counted as evidence of success:

- An initial mock omitted mirror expected_base_path. The test fixture, not the
  collector contract, was corrected to a matching /fixture base and identity.
- Windows working-tree packaging retained CRLF in an existing Shell fixture.
  Final broad validation used a normalized Git index-tree archive; that Shell
  script was not modified.
- The VM stopped/restarted during work (owned container exit255, not OOM).
  Its ephemeral database was freshly reprovisioned with purpose markers and the
  same resource caps before final runs; failed connectivity logs were retained.
- An exploratory latency run triggered baseline immediately but waited for the
  candidate's periodic deadline. It is retained as perf-final.log, NOT used as
  a matched latency comparison. The final test uses the same30s HOT root policy
  in both modes and real elapsed waits, not a simulated clock.

## Repeat-work measurement

The110/401 fixtures fully verify all children, then observe the identical parent
three times and drain between observations. Revisit interval1h deliberately
keeps periodic polls outside this short duplicate experiment. Source calls count
only actual forced refresh=true directory requests, not /api/me/topology calls.
Fanout signals are independently measured from durable sum(signal_seq), removing
the three manual parent seeds; they are not inferred from candidate-only stats.

| Children | Mode | Parent observations | Forced refreshes | Peak PENDING | Fanout signals | Eligible child injection / parent |
| --- | --- | --- | --- | --- | --- | --- |
|110|legacy|3|333|110|330|110|
|110|F2|3|3|0|0|0|
|401|legacy|3|1206|401|1203|401|
|401|F2|3|3|0|0|0|

All cases end with zero PENDING after draining. These are per-observation
injection counts, NOT a measured nonzero steady-state backlog slope (each batch
was drained). F2 reuses330/1203 verified child epochs without changing their
timestamps/CAS fields. Removing one repeat parent's N child scans does not remove
the need for a future periodic child scan.

The real2k nested fixture contains four groups with499 leaves each,2000 total
directories. Both modes perform2001 initial forced scans. Two rounds of root
plus all four group observations produce ten parent refreshes, without draining
leaves between rounds:

| Measurement | Legacy | F2 |
| --- | --- | --- |
| Repeated parent forced refreshes |10|10|
| Peak PENDING leaf work |1996|0|
| Measured fanout signals |4000|0|
| Verified child epochs reused |0|4000|

The final race-instrumented2k run passed in161.52s (legacy79.82s/F281.69s),
including fixture setup and assertions. It also adds a late nested file through
formal Hint and verifies actual Q5 PRESENT and exactly one Q8 addition, no
resource-removed events. This is2k functional/amplification evidence, NOT20k/50k
capacity, a production upstream latency model or F3 acceptance.

## Matched400-directory latency experiment

The fixture first verifies401 child directories. Both modes have the same HOT
root watch with30s cadence. A batch injects40 late files in evenly spaced old
directories; F2 also has30s rolling child watches. The test really waits for the
stored due deadline. Each mode uses the existing five due/five execution limits,
no source concurrency. Claim is recorded at ScanScope entry after ClaimWork
commits; scan return follows Kernel commit. Each timed file's producing Snapshot
is checked against its Q5 generation, with exactly one Q8 addition and no removes.

Final matched results (milliseconds):

| Measurement, P50/P95/P99 | Legacy | F2 |
| --- | --- | --- |
| Actual source-change to committed claim |26474 /29368 /29690|26961 /30390 /30762|
| Actual source-change to committed scan return |26490 /29384 /29705|26976 /30406 /30777|
| Scan service time |15 /16 /19|15 /18 /19|
| Forced refreshes through the last sampled target |395|395|

F2 does NOT show a latency improvement here: sampled P95 is about1.02s slower.
Both modes still inspect the old directories to detect their unhinted late files;
eliminating redundant parent intent is not eliminating rolling verification.
There is no hard timing assertion tied to a particular CPU; correctness and
bounded progress are mandatory, with the observed timings reported unfiltered.
Nearest-rank quantiles are N40 files in one batch per mode, not40 independent
workloads. This measures source-change-to-canonical, NOT the CloudSite browser
or projection latency. Mock source service is far faster than real115; no
production30s/two-minute SLA can be inferred. Ordinary periodic checks can cost
more than immediate parent notification; formal Hint keeps immediate eligibility.

## Safety and recovery evidence

- A logical0/30/60/90s HOT parent sequence adds zero duplicate child scans. A
  new process at the stored rolling deadline discovers late files in both an old
  child and an old nested child, despite unchanged parent metadata. Formal Hint
  discovers another file before its periodic deadline. Q5/Q8 verify all three.
- Continuous HOT root activity and managed due watches both progress within the
  existing five-slot budget. All ten late-file targets become Q5 PRESENT with
  single Q8 additions. Existing F1 first-discovery/fairness regression remains.
- Actual virtual mount topology plus an empty source listing does not grant
  absence authority. Existing resources/evidence remain unchanged; no deletion
  thresholds, grace periods or PARTIAL rules were relaxed.
- Store decision tests retain eligible PENDING bitwise, verified epoch bitwise,
  IN_FLIGHT post-claim signal, RETRY_WAIT/BLOCKED, explicit HIGH, unknown namespace,
  changed canonical row, disabled operator watch, overflow interval and stale CAS.
- Triggered transaction failure rolls back child publication, watch and parent
  completion. A real tagged PG backend termination repeats that proof; new-pool
  startup recovery, reclaim and replay enroll exactly one watch. No leaked child
  or watch survives the aborted transaction.
- Non-mirror sources retain the legacy completion path. Missing atomic mirror
  composition port fails before execution. F2 due fairness requires at least two
  attempts; legacy single-attempt configuration still works when F2 is OFF.

## Commands, logs and acceptance boundary

On the owned instance, package-serial full tests passed474 top-level methods,
18 optional methods skipped, zero failed. Skips include16 pre-existing optional
provider/rclone/live/scale cases and the two explicit F2 heavy opt-ins. Focused
F1/F2/atomic race passed24 top-level methods with just those two heavy skips.
The separate enabled2k and actual400 tests both ran and passed, not skip-counted.
Full vet/build/gofmt and runtime lifecycle race also passed. The final matched
latency and logical cadence fixture were rerun after test-only timing changes.

CI provisions the owned database, repeats the F1/F2/atomic race matrix count3,
then mandatorily enables both heavy fixtures. Exact-head GitHub CI is a separate
pending acceptance gate until the PR's actual run is checked. Do not substitute
this local receipt for green CI or Architect approval; do not merge/start F3.

Logs are retained under /home/nathan/index134-f2-20261023/logs on .50:

| File | SHA-256 |
| --- | --- |
|full-final.log|af53dd8b670f8754bc3da8240dcec8a45ce45f311eab19bcd646557dfa1cff79|
|focused-final.log|dc8488f722f2d80583c509c9ecd349e38e9318da95692498c4e4e47c29d0a3be|
|runtime-race-final.log|0265b07063b657c59a093233c77d8ec7e6024f7b8be036709bbf81b96dfc8a16|
|perf-final.log (2k; exploratory unmatched latency)|41a0bd4f1e2f8e299696247077f8f49bd11422a4840199c9354066d8ce93c868|
|perf-latency-matched-final.log|85af83de4cbfcd574bcf6f416e8e85c77571389236d13fbf3918d0da8a717942|

Production was not inspected for new measurements or reconfigured during F2.
Production redundant-parent ratio, network throughput, full website latency,
20k/50k and long-term revisit load remain UNMEASURED. Issue134 stays OPEN.
