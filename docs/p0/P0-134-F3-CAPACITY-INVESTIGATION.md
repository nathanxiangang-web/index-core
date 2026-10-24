# Issue134 F3: capacity investigation, not production acceptance

Date:2026-10-24 UTC. Base:db8f30155fb17b239143cd958ac8bd64e1fd9529.
F2 integration PR136/Architect6380413345 and merged-main CI40066617849 SUCCESS
verified before branching. Task packet: Issue134 comment6380420697.

Verdict: bounded investigation/correctness samples PASS; fixed120s all-directory
capacity gate NOT SATISFIED. Adaptive runtime/automatic Watch retirement/full
20k or50k source traversal/long soak/production canary are NOT IMPLEMENTED or
NOT_RUN. Do not equate green tests with a sustainable production schedule.

## Reproducibility, modules and caps

[ADR009 proposal](../decisions/ADR-009-CAPACITY-BUDGET-INVESTIGATION.md) defines
dependencies, budget/lifecycle interfaces required by future implementation,
extension constraints and the separate concurrency decision gate. This PR changes
only tests, CI and documents. Existing scheduler/store/scanner/Kernel behavior
and F1/F2 default-OFF settings are unchanged. No migration or production binary.

Host:ssh dsh/192.168.178.50, Go1.27.1, GOMAXPROCS2, GOMEMLIMIT512MiB (a soft Go
memory target, NOT a physical process limit). Owned container index134-f3-pg18,
label codex-task=index134-f3: hard CPU1, memory1GiB, pids128, restart=no,512MiB
tmpfs, loopback port. WAL64/32MB/checkpoint30s, fsync ON. No resource cap was
raised. Owned DB target/owner/comment/PG18 are checked before fixture resets.
The same purpose-marked test DB is reused sequentially; no shared DB/service,
real OpenList/115, source deletion or download submission was accessed.

Commands after provisioning the same guarded Issue134 fixture DB:

```bash
# Set only the owned loopback index134_test DSN and purpose marker described by testutil/issue134.go.
export INDEXCORE_ISSUE134_DISPOSABLE=owned-disposable-only
for n in 2000 20000 50000; do
  INDEXCORE_ISSUE134_F3_SCALE=$n GOMAXPROCS=2 GOMEMLIMIT=512MiB \
    go test -race -p 1 -count=1 -timeout=10m -run '^TestIssue134F3Capacity$' ./internal/runtime/incrementalexec -v
done
go test -race -p 1 -count=3 -run '^TestIssue134F3BudgetModel$|^TestIssue134F3WatchLifecycleEvidence$' ./internal/runtime/incrementalexec -v
```

No default DSN fallback is allowed for the heavy fixture. CI separately enables
the2k experiment;20k/50k remain explicitly bounded manual experiments, not skipped
cases silently marked accepted. Bootstrap SQL quoting and an initial test-field
compile typo were corrected before successful runs; first.log retains the failed
compile. No failure was suppressed or consistency assertion weakened.

## Population versus observation

Each N means exactly N directory rows: N/100 groups with99 direct child
directories each, depth2, maximum500 children in the root and100 in the changed
group. Bulk SQL preloads Canonical/verified Work/managed Watch metadata solely
for a DB population fixture; it is NOT N completed source scans, trusted initial
Snapshot histories or a full traversal benchmark. Normal identity metadata,
unique paths, size/mtime and parent IDs are consistent for repeat observations.

Both F1/F2 are opted in only in the fixture.120s managed watches and one30s HOT
root are made synchronously due by an explicit logical+121s eligibility offset.
No actual120s initial wait is claimed. Subsequent HTTP/services/latencies are
real elapsed time, with a real1250ms delay on each forced-list request. The mock
has no production failure/rate-limit distribution. Topology is intentionally
virtual/additive-only in the scale runs; no absence authorization is fabricated.

Two no-change cycles precede a1% late-file change set (one old directory per
group) plus one new nested subtree. The bounded change window contains seven
trusted Hints, one deliberately unhinted old-directory change, new nested
discovery, an aged NORMAL intent and repeated HIGH root activity. Four further
cycles preserve five-poll/five-execution caps. Actual Kernel/Q5/Q8 and producing
Snapshot anchors are checked for nine target files; histories are not replaced
by row-count-only assertions. All scopes remain serial, with no prefetch.

## Measured queue and service evidence

| Measurement |2k|20k|50k|
| --- | --- | --- | --- |
| Actual forced scoped scans, including no-change stage |31|31|31|
| Scanner service seconds |40.327|48.878|60.155|
| Actual serial service scopes/min |46.123|38.054|30.920|
| Injected HTTP delay seconds, summed |38.988|39.003|38.961|
| Scanner remainder seconds, summed |1.339|9.875|21.194|
| Final active watches |2003|20003|50003|
| Final overdue watches |1972|19972|49972|
| Final oldest overdue seconds, logical initial offset plus real elapsed |42.058|52.099|65.770|
| Final PENDING / oldest pending seconds |12 /19.649|12 /23.829|12 /29.280|
| Source changed files / actually observed |20 /8|200 /8|500 /8|
| Changed files still unobserved, not called successful |12|192|492|

Overdue age increases throughout the trace even while PENDING stays near12.
P6 lazy materialization masks offered demand: a small Work queue is NOT proof
of no backlog. This synchronized finite storm alone is not a long steady-state
measurement. Unhinted directories also need rolling service after verification.
No steady-state queue stability at future scale has been demonstrated.

Naive due demand N/2 per minute versus measured fixture service is1000/46.123,
10000/38.054 and25000/30.920, before roots/retries/foreground work. Coalescing
means this is unmet offered inspection demand, NOT an equal number of new Work
rows/minute. One Watch coalescing many missed intervals still has freshness debt.

| Target timing ms, nearest rank P50/P95/P99, N9 per scale |2k|20k|50k|
| --- | --- | --- | --- |
| Source change to committed claim |9414 /15964 /15964|11590 /19397 /19397|15364 /22856 /22856|
| Source change to producing scan committed return |10707 /17266 /17266|13142 /20931 /20931|17226 /24716 /24716|
| Fresh directory discovery-return to claim, N1 |1503|2352|3763|
| Aged NORMAL claim after injection, N1 |1590|2408|3906|

Quantiles are one batch with N9 correlated targets, P95/P99=max; not a production
SLA or statistical tail estimate. Changed-set timings include profiling queries
performed before draining. Fresh discovery-return is a post-commit upper bound,
not the exact instant the source first created the directory. Canonical-return
is anchored to the actual file-producing Snapshot, not browser/projection time.
Seven hints plus one pre-existing queued/due unhinted change passed; the rest of
the1% source set is explicitly unobserved in the finite trace.

## SQL plans, memory, CPU and missing decomposition

Actual public Store calls are traced in a dedicated single-connection test pool;
the exact emitted SQL/args are read-only EXPLAIN ANALYZE/BUFFERS, not a copied
alternative selector. Client times include Go decoding/transport and race
instrumentation. Selector N10 and whole-root Canonical N3 are single-fixture
samples, in whole milliseconds (zero means <1ms after truncation).

| Client P50/P95/P99 ms |2k|20k|50k|
| --- | --- | --- | --- |
| Legacy due selector |0 /18 /18|4 /22 /22|10 /27 /27|
| F2 fair due selector |1 /4 /4|3 /5 /5|7 /9 /9|
| PENDING selector |0 /1 /1|0 /1 /1|0 /1 /1|
| First-discovery selector |3 /5 /5|2 /5 /5|3 /5 /5|
| Full-root Canonical client read |25 /27 /27|198 /229 /229|525 /538 /538|
| Full-root Canonical server EXPLAIN execution ms, N1 |3.371|21.082|43.673|

Canonical reads materialize2001/20001/50001 rows (fresh directory already added),
sorting in memory at2k and external-merge on disk at20k/50k. Full-root materialization
and Go decoding are significant, not a slow due selector alone. The fair query
returns six rows but the ordinary cohort can scan/filter the full Watch table;
at20k it filtered20002 rows, with a separate bounded index scan for the managed
cohort. Output bounds do not mean bounded physical reads. Do not add an index or
narrow canonical truth without a separately reviewed identity/coverage proof.

| Observed resource sample |2k|20k|50k|
| --- | --- | --- | --- |
| PG CPU maximum percent of a core, sampled |34.58|76.70|80.31|
| PG container memory maximum MiB, sampled |134.8|328.6|416.3|
| Final database logical bytes |11747855|28079807|53860031|
| DB temp_bytes cumulative since container start |0|19398656|67862528|
| Blocked DB sessions at each reported queue sample |0|0|0|

CPU/memory are docker stats samples, not guaranteed process peaks. temp_bytes
is cumulative across sequential fixture resets, not a per-query allocation or
per-scale independent disk maximum. The owned512MiB tmpfs was145.9MiB used after
the50k run, OOM=false/restarts0; all work remained within the PG hard caps.
Go heap snapshots are in raw receipts; GOMEMLIMIT is not claimed as a hard RSS cap.

HTTP delay accounts for96.7%/79.8%/64.8% of measured scanner service. The remaining
time is NOT an exact serialized admission critical path: it includes collection,
SQL, allocation/decoding and Kernel/store work. An exact source/identity/admission
critical-path decomposition and real upstream rate limits remain UNMEASURED.
Parallel prefetch is therefore NOT JUSTIFIED by this fixture. Even eliminating
some HTTP wait is not a plan to serve tens of thousands of scans/minute. Budget
and lifecycle are prerequisites; keep the Kernel writer serial.

## Budget and lifecycle decisions for subsequent review

The test-only conservative model uses older historical48.267/min and at most50%
for background after reservations. Minimum whole-round times are17.610min for425,
82.872min for2k,828.724min for20k and2071.809min for50k. Current F2 max1h cannot
represent even the2k50%-budget round. Do not silently stretch it or reinterpret
it as a guaranteed freshness SLA. A later runtime design needs an explicit
bounded rolling cursor/cohort, measured pessimistic service estimate, preserved
Hint/HIGH/aged reservations and reported unsatisfied freshness/overload.

The120-minute synthetic budget workload (trusted12/min, service48->24->48,
conservative EWMA/lower-bound background allocation) has modeled debt0. Trusted
12/min with service8/min accumulates40 intents over ten minutes. These are pure
MODELS, not runtime adaptation, proof of bounded queues under arbitrary arrivals,
or real long-period source coverage. No policy is activated by these tests.

Lifecycle evidence uses a separate real PG+HTTP/Kernel fixture, not seeded removal
SQL: trusted physical-view disappearance commits a genuine tombstone and one Q8
removal, while its managed Watch remains WARM/due. A test-only exact manual
CAS disables schedule without changing Work/history, stale CAS is refused, and
same-path reappearance gets a NEW canonical ID while the old disabled Watch
does not automatically rebind. Virtual-only empty success does not delete live
resources. BLOCKED intent/failure history survives due polling and a new pool;
inactive Root filters due selection but retains Watch rows.

This proves lifecycle gaps, not an automatic retirement fix. Proposed future
transaction must fence Root/adapter/canonical resource ID+epoch/Watch version and
trusted removal anchor, refuse path ABA/concurrent Hint/IN_FLIGHT, preserve all
histories and never take ownership of operator/adaptive watches. Existing
CASUpdateWatch alone cannot supply that cross-record atomic guarantee.

## Validation and raw receipts

Isolated gofmt/vet/build passed. Full regression passed476 top-level methods,
19 optional skips, zero failed (the prior18 optional cases plus the explicit
F3 capacity opt-in, separately run at all three sizes). Lifecycle/model race
passed six method executions across count3, no skips or failures. Runtime/app
lifecycle race also passed. Exact-head CI is a separate gate; its checked result
will be attached to the PR. No CI success is assumed in this pre-push document.
F1/F2 late-file, virtual, identity, Q8, Hint and crash
regressions remain in the existing mandatory CI. New CI enables the2k F3 test.

Committed [bounded raw receipts](evidence/issue134-f3-receipts.ndjson) contain
36 records: nine actual query plans and27 unedited queue/service/result receipt
strings. Full logs/CPU samples are retained in
/home/nathan/index134-f3-20261024/logs on.50:

| File | SHA-256 |
| --- | --- |
|capacity-2000.log|52a43392165d280273cc3d5d232935a387043683295e43767e278f71529162f8|
|capacity-20000.log|192883a0c7763e7da71bd14fcd3a8d6f8d9560be019d571178cb4a85cd35c94d|
|capacity-50000.log|297d49e70f93a4ca72be1f34bf0c3cce36c2c78e82b5b7e1c70574e640f3d29b|
|metrics-2000.log|306e7d927ab2d6a617216eaf0dadf65398b2b49f54b3069354afea3017d2273d|
|metrics-20000.log|f34d4d240c9a6bf5de31f441424a0999be628034386d9ec8d78a432cd0b8ea5a|
|metrics-50000.log|c4696688d5fe9fd52e85b9f052aaa4ee1ae0eb08b940cfdd3d639faf66029be7|
|full-final.log|8955b567a2d73b8c33ef58d1d7e83dfda016adbb8350f2fd3fbb011709c02f88|
|focused-final.log|e2dec79774f04b2a49aff9e9cdb2698ec43486bf53c8ac84cb1ef6ba944f9e57|
|runtime-race-final.log|48aacf16565715f7d27b164704fda87571c988ade671ac13c18cbbe9a29fd2c3|

Long-term steady-state, full20k/50k traversal, all1% change convergence, real
upstream effects, production website freshness and concurrency benefit remain
NOT_RUN/UNMEASURED. Keep Issue134 OPEN; no production rollout or R4 PR126/#133 work.
