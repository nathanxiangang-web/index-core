# P0-D1: exact committed Snapshot handoff (Issue130 candidate)

Development base `1a02cdc9238d147dd0fc11b98e6710821e180560`, incident129.
The deployed `fdeb6c8e910033f0d9e8437a4620347d9e8a6680` and base have identical
blobs for coordinator, scoped scan, executor/config and incremental Work paths
checked for this repair. R4 PR126 is a separate, untouched branch.

## Module and transaction contract

Only the PostgreSQL Store/Coordinator production boundary changes. The Collector,
Kernel identity/reconcile/removal rules, Query contracts and runtime fatal policy
are unchanged. A private `terminalSnapshotReceipt` helper reads persisted facts;
it is not a generic error catcher, admission API or second reconciler. Testutil's
owned-DB guard is test infrastructure and cannot be selected by production wiring.

Before: ScanScope submits/admit -> same-process Worker commits -> synchronous
ProcessSnapshot re-admits RECONCILED input -> ErrNotSubmitted -> Work INTERNAL ->
runtime cycle/executor fatal. Alternatively, Worker wins after admission lookup,
leaving the selected sequence terminal before synchronous stage2.

After: stage1 locks the existing Root, then either follows unchanged SUBMITTED
admission logic or discovers one exact terminal receipt. Stage2 reacquires that
same Root lock and validates the **already selected positive admission_seq** before
considering a different pending head. Worker ProcessHead uses this same stage2.
Both transactions are read-committed with the established per-root FOR UPDATE
serialization protocol; every legitimate reconcile writer holds that lock.
No uncommitted terminal result can pass through it.

Receipt acceptance requires all of:

- Exact Root + Snapshot binding; Snapshot RECONCILED, nonnull/nonfailed acceptance.
- Exactly one admission for this Snapshot, APPLIED or NOOP, matching authoritative
  sequence and root; nonnegative applied generation <= Root.current_generation,
  sequence <= Root.latest_admission_seq and existing generation history if >0.
- Exactly one successful result with matching root/sequence/generation, correct
  RECONCILED vs NOOP kind and no rejection. The schema has **no Snapshot column on
  index_reconcile_result**: its Snapshot binding is through the exact validated
  Admission triple, not inferred from generation or result recency.
- APPLIED has its exact application-history row; NOOP has no own application row
  and retains a previously committed same-generation IO3 anchor.
- Generation provenance, if owned by this input, matches both Snapshot and seq;
  it reconstructs original pre/post generation and mutation flag. Later Root
  generations or another pending Snapshot cannot replace the historical receipt.

The helper does not insert admissions, recompute identity, run Reconcile, update
Root/Canonical or append Journal. Public AdmitOrResumeSnapshot remains strictly
SUBMITTED-only. Inconsistent SUBMITTED/terminal admission does not allocate a new
sequence. Missing/duplicate/conflicting/rejected/stale/failed evidence and actual
DB/query/CAS faults remain errors. Existing binding error semantics are preserved.

No raw provider paths/errors, SQL or IDs are added to production diagnostics.
Unrelated INTERNAL failures remain fatal; the broader error classifier is not
rewritten. Future adapters must preserve this exact receipt/locking contract,
not emulate it by broadly swallowing ErrNotSubmitted or ErrNotHead.

## Deterministic proof

`TestIssue130RealScanWorkerHandoff` executes actual ScanScope, the real background
Worker.Run, P4/P5/P6 and incrementalruntime.Run with synthetic OpenList HTTP and
real disposable PostgreSQL18. A single acquired writer lock models serve ownership
of both in-process paths. PGX test-only commit tracers and channels force:

1. Worker commits before synchronous admission lookup.
2. Worker commits after synchronous admission lookup but before stage2.
3. Synchronous scanner commits before Worker observes the pending roots.

No production failpoint or scheduler-probability loop chooses the winner. Polling
only observes durable VERIFIED completion. A subsequent explicit signal/wake also
verifies, no new INTERNAL/RETRY_WAIT, survivor PRESENT, exactly3 HTTP observations
(seed + two refreshes). Journal count **and ordered full-row digest** stay unchanged.
Receipt replay checks Canonical full-row digest plus admission/generation/history
counts; no new identity evidence or Journal mutation occurs.

With only the old main coordinator overlaid, the same first interleaving fails:
`unexpected runtime fatal: incrementalruntime: fatal phase=cycle error_class=executor`.
Restoring the repair passes all three. The baseline harness initially had an
incorrect relative restore-archive path; restoration was corrected and verified
before final runs. This harness error affected only the disposable source tree.

Additional tests reject wrong Root/Snapshot/selected seq (including0/negative),
duplicate/missing admissions/results/history, wrong/unknown generations, failed
acceptance and rejected/stale/failed/PENDING terminal-looking records. FIFO tests
retain an older pending head and later input. Three fresh Coordinator instances
replay original APPLIED and NOOP, including after a newer input advances the Root.

A real transaction publishes fake terminal-looking generation99 **without commit**;
the reader is observed blocked on the Root lock. Terminating only that owned PG
backend rolls back the fake transaction; a fresh caller processes the real pending
input and subsequent receipt replay adds nothing. This is a **PostgreSQL backend
crash**, not an IndexCore OS-process kill, whole-server crash or production restart.
Query42P01, injected40001 update/CAS failure and closed pool stay errors/rollbacks.
The existing zero-row CAS-conflict regression also remains in the full suite.

## Execution evidence and isolation

Operator-designated .50, Go1.27.1, GOMAXPROCS2/GOMEMLIMIT512MiB. Newly owned
PostgreSQL18 container: CPU1, memory512MiB, PIDs128, loopback only. Dedicated
`indexcore_issue130_test`, explicit owned-disposable opt-in, purpose DB comment,
DB ownership and major18 verified **before destructive reset**. No default DSN
fallback. Inherited suites use a separate synthetic DB. No production DSN,
provider account, shared volume or source mutation. Packages reset schemas
serially with `-p 1`.

Final results (passing event counts include parent tests and subtests):

| Command scope | Result |
| --- | --- |
| go test -json -p 1 -count=1 ./... with PG18 | 683 pass,16 inherited optional skips |
| runtime/app lifecycle race | 80 pass |
| P0-R2 matrix race | 53 pass |
| P0-R3 diagnostics race | 70 pass |
| Issue130 real PG18 race | 50 pass,0 skip |
| go vet ./... / go build ./... | PASS |

The16 inherited skips require real provider/rclone/scale opt-ins; no R4 scale,
real115 mutation, live Root observation or production stability is claimed.
Initial full run caught a changed missing-Snapshot binding error (fixed, old
assertion preserved) and CRLF shell packaging. Original scripts were re-exported
with core.autocrlf=false; the existing shell test then passed without edits.
No test expectations about Kernel safety or fatal policy were relaxed.

CI explicitly provisions the separately marked purpose DB and runs the new race
suite in addition to inherited full/race/architecture coverage. Exact-head CI is
reported on the PR, not assumed from these local results.

## Release boundary

One unmerged PR for Architect review. No schema migration, production deployment,
Root repair, retry or rewrite of the123 INTERNAL/RETRY_WAIT records. D0 does not
prove that every recorded live fatal has this cause. D2 requires a reviewed merged
artifact, merged-main CI and a separately authorized backed-up rollout with real
continuous Q4/Q5/Q8/Q9, wake/retry and restart/fatal observation. CloudSite153
cannot be unblocked by this code/test candidate alone.
