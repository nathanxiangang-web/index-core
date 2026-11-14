# Issue134 / PR148: registration repair and Whole-Root Prior review

## Scope and provenance

This independent follow-up is stacked on unmerged PR148, base
`9841cca43ecd10d675b0985449783b0ab9b3bbe8`. Remote main was verified as
`9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e`. No PR146/147 changes, production
configuration/data/provider traffic, second writer, reader increase or merge.
Development runs on the owned dsh PG18.6/Fake OpenList lab. Historical120s20K
results remain in the preceding report; no new20K or long synthetic replay.

The latest Owner instruction separates functional delivery from real deployment
capacity acceptance. Functional fixes and pre-deployment review are this stage;
real scale/throughput/resource/long stability are **NOT_RUN_POST_DEPLOYMENT**.

## Registration defect and repair

P6 set `rollingPrepared=true` after a one-time DISTINCT Root scan of managed
Watches. The rolling selector starts from `index_mirror_root_budget`. An empty
Root or a new Root after this scan could create a managed Watch through
`CreateWatch` without a budget row, making its due Watch invisible. The parent
completion path sometimes inserted a budget, masking the missing invariant.

Migration0007 registers all Roots transactionally at creation and backfills
existing Roots once. `PrepareMirrorRolling` checks only the singleton; no Watch
scan, retry loop or extra writer. Existing budgets/cursors/reservations survive.
Root metadata exists before the first Watch, regardless of F4 setting. A failed
Root registration rolls back Root creation; retry works in the existing actor.
See [ADR014](../decisions/ADR-014-MIRROR-ROOT-REGISTRATION.md) for the tradeoff.

The preflight also found an off/drain/on defect: successful completion returned
early with F4 disabled, retaining an old reservation forever. Completion now
releases only the verified covered reservation even while off. It does not
renew bindings or clear newer Hint work, and preserves global admission time.

Regression coverage includes empty startup, first later Watch, first accepted
child discovery, later Root, F4-disabled creation, eight concurrent Roots,
idempotent existing registration, actor reconstruction, retained reservation,
injected registration/migration failures and retry without restart. A reused
eight-read OS-child SIGKILL fixture also retains a real F4 reservation through
crash/claim recovery and releases it only after fresh-epoch success. The actual
app late-file test now retains **the same actor** from empty-Watch startup through
later Watch creation, real Fake HTTP, Kernel commit and consistent Q5/Q8.

## Whole-Root Prior call chain and SQL

`runtime/scan.readClaimedScope` uses the existing serial/pooled fetch path;
`scan.Service.ScanScope`/scoped observation persists and submits the Snapshot,
then `Coordinator.ProcessSnapshot` / `ProcessHead` calls `reconcileHeadSafe`.
Stage2 takes the Root row lock, validates absolute head/binding/scoped lineage,
loads `LoadPriorResources(ctx, tx, rootID)`, evaluates acceptance/identity and
reconciles using that **same prior slice** before committing generation,
Canonical/evidence, Journal and terminal receipt atomically.
`Store.ReconcileHead` in `tx.go` is a separate lower-level path, not a second call
inside `reconcileHeadSafe`. There is one prior load per evaluation in the safe
path, not an accidental double-load through both functions.

`prior.go` uses `priorQuery("c.root_id=$1::uuid")`; the full literal SQL and both
actual EXPLAIN plans are in the evidence JSON. It selects every Root Canonical
row, including removed resources, LEFT JOIN LATERAL its latest authoritative
observation (`ORDER BY observation_id DESC LIMIT 1`), then orders by resource ID.
It does not use `index_identity_evidence_current`. A small directory input limits
source observation, not the authoritative identity/deletion context required by
the current Kernel contract. Whole-Root context is loaded before IO3 no-op
evaluation, so repeated scans still incur prior costs even if they later no-op.

The earlier rolling run observed111 calls,2,200,948 returned rows and9.489s of
client query/row-consumption time, including an interrupted call. This does not
measure PostgreSQL CPU, and it does not isolate network, decoding or Kernel CPU.
No new whole-Root workload-scale speedup is claimed here.

| Option | Decision and safety constraint |
| --- | --- |
| Reuse within Stage2 | Already done: evaluation/reconcile share the loaded slice under the Root lock. |
| Reuse across admissions | Deferred: canonical generation, evidence-only changes, head ordering and transaction visibility must all be fenced. Generation-only cache is unsafe. |
| Batch admissions | Deferred: existing Root FIFO, per-Snapshot acceptance/identity, cancellation and Journal commit boundaries must remain intact. Reuse merged read workers only for frozen read lineage. |
| Projection/current cache | Rejected as authority; latest observations are the contract. |
| Ordered identity index | Implemented as0008; unchanged full SQL/cardinality and exact returned values. |

The bounded fixture has128 resources,96 with24 observations each (2,304 total),
32 without evidence, deliberately false current projections, and observed times
opposite insertion order. It compares complete prior values before/after index
creation, and verifies an evidence-only later insertion is visible without a
Canonical generation change. The measured initial probe:

| PG18 EXPLAIN observation | Before | After |
| --- | ---: | ---: |
| Returned Canonical rows |128|128|
| Server execution ms |3.208|0.284|
| Shared hit/read blocks |1768/0|342/13|
| Identity inner access |global observation PK backward + resource filter|resource/latest composite index|

This is one small planner probe, not a throughput benchmark or a guarantee of the
same factor on real data. Before, the global PK scan filtered an average612 rows
per inner loop; after, it used128 resource-specific index probes. The outer full
Root read, row transfer/decoding and resource ordering remain. The index has no
INCLUDE of potentially large provider strings, but adds write/storage/WAL cost.

## Reproduction and verification

Use a purpose-marked disposable PG18 database only. The actual app tests require
the existing Issue138 database guard. Never point these reset-schema tests at a
production database. Commands and raw NDJSON receipts are recorded in evidence.

```sh
go vet ./...
go build ./...
go test -json -p 1 -count=1 ./...
go test -race -json -p 1 -count=3 -run '^TestIssue134Rolling|^TestIssue134RuntimeQueryCosts' \
  ./internal/sourcebudget ./internal/runtime/config ./internal/store/postgres \
  ./internal/runtime/scan ./internal/runtime/incrementalorch ./internal/runtime/app
```

Normal PR CI retains complete PG18 tests, F4/related Race, crash recovery,
Kernel/identity/Journal and stale-head/FIFO checks. Historical capacity-only
experiments are retained as optional `workflow_dispatch(run_capacity=true)`;
assertions and evidence were not changed into weaker PASS criteria. Missing
real20K/soak/throughput/resource results remain NOT_RUN, not PASS.

The conditional off-mode completion adds one indexed conditional budget UPDATE
per successful Work completion, including a no-match probe when no reservation
exists. It creates no reservation or heap update in that case; measure its actual
Runtime cost during the authorized real-data stage. Root candidate selection
still scales with registered Root count; pre-registration includes idle Roots.
It does not add a wake-time Watch backfill, and does not certify arbitrary
high-Root-count capacity.

The evidence manifest records final checks and their exact source hashes; see the
PR exact-head CI receipt for final-head validation. Deployment decisions follow
the separate [deployment plan](P0-134-F4-DEPLOYMENT.md).

## Final local verification receipt

All six final gates passed on the same280 Go/SQL file hashes. Execution source
manifest: `b2d025bcb1c0dcffc51b3a8067941fb907c51896f1a746d934939e1f2abb7ee7`.
Later documentation/evidence additions are not mislabelled as already executed
source; final-head CI validates the committed tree.

| Gate | Result (test/subtest events, not unique methods) |
| --- | --- |
| Full PG18 |776 pass /126 disclosed skips /0 fail;160.73s|
| Runtime/sourcebudget Race |111 pass /50 skips /0 fail|
| F4 Race count3, purpose138 DB |111 pass /3 optional-replay skips /0 fail|
| Issue134 related Race, purpose134 DB |54 pass /14 purpose/optional skips /0 fail|
| Vet / Build |PASS / PASS|
| Schema5→8 and schema5 rollback restore |Original business fingerprints and one pending Work preserved; main rejects8 and accepts restored5|
| Schema8 backup/restore |All table fingerprints identical,8 Journal events retained|

The full suite's purpose-guarded app tests run in the dedicated F4 purpose138
Race job rather than bypassing the guard. Optional capacity/child-process helper
skips are disclosed; real OS-child kill/recovery is exercised by its parent test.
No DATA RACE was reported. On the unmodified PR148 parent, the new empty-startup
regression fails (first later Watch is not selected); the repaired version passes.
An early invalid Canonical test fixture was corrected; its failure log remains
in the external evidence archive, not silently treated as PASS.

[Raw checks and hashes](evidence/issue134-registration-manifest-20261114.json),
[exact authoritative SQL](evidence/issue134-prior-authoritative.sql),
[planner evidence](evidence/issue134-prior-plan.json.gz),
[schema5 upgrade/rollback receipt](evidence/issue134-registration-schema5-upgrade-rollback.json.gz).
