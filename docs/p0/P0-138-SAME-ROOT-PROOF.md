# Issue138 PR-A: same-root frozen-read lineage candidate

This is the module contract, reproducibility guide and evidence interpretation,
NOT Architect acceptance, runtime enablement or a production speedup claim.
Per-HEAD CI, raw timing receipts and final execution status are recorded in the
associated PR Worker receipt. Do not infer them from an earlier green branch.
Proposal: [ADR011](../decisions/ADR-011-SAME-ROOT-READ-LINEAGE.md).

## Baseline and independent prerequisite

PR140 is accepted and merged main562a852; merge CI40081586002 SUCCESS verified.
Its eight-Root proof did not solve same-root read serialization. This candidate
is stacked on independent PR141/a49bbfec, not a claim that PR141 is accepted.
PR141 fixes a reproduced legacy retry-maintenance shutdown misclassification;
its exact-head CI40085684901 SUCCESS and original failures remain available.
Review/merge that small prerequisite separately, then retarget/sync this PR to
the resulting main and validate that HEAD. No self-acceptance or auto-deployment.

## Modules and immutable boundaries

1. Existing Store ClaimWork owns each durable attempt. Eligibility is1..8 unique
   non-root sibling scopes, same ACTIVE Root, unique PRESENT directory identities.
   Runtime and Store both enforce claim-start/scope order, not HTTP arrival order.
2. Unwired SameRootPrototype overlaps only source reads through shared readScope.
   Result retention<=8, entries<=1000 each, scope depth<=8; contexts are finite.
   FetchBatch includes a subsequent persistence phase: unlike the old FetchScope,
   it is NOT a wholly read-only method. No canonical/admission write occurs here.
3. Store FreezeScopedReadBatch atomically persists ALL actual DRAFT facts and
   their identical ordered Snapshot-ID roster, original generation/frontier/time,
   exact claim/parent/config, policy revision and dependency digest. There is no
   empty placeholder Snapshot, new table, migration, queue or provider write.
4. Existing admission and Coordinator own serial commits. Every Stage1 API and
   final Stage2 enforce the roster prefix and exact terminal predecessor receipts.
   APPLIED without mutation and NOOP do not invent generation increments.
5. Source generation/time remain unchanged. New fence version2 and coverage
   version3 explicitly require Store-validated lineage; legacy fence1/coverage2
   retain exact-generation semantics. No missing/virtual/partial input is promoted
   into trusted absence merely because a sibling committed.
6. Work completion remains caller-owned. Checkpoint resume reuses persisted facts;
   an unacknowledged terminal slot replays its original receipt, acknowledged
   slots may be skipped. Cancel/discard never marks a claim VERIFIED.

All provider I/O ends before transactional admission. No app/CLI/config creates
this prototype. Normal ScanScope is unchanged. Source facts, lineage verification
and Canonical truth remain distinct modules; no provider logic enters the Kernel.

## Conflict set and measured query correction

Path-disjoint siblings may still share identity dependencies. Capture/check the
scoped directory/subtree/ancestors, global equal hash/size/latest provider IDs,
and ALL non-NONE removal evidence including tombstones. Policy/config ABA,
parent replacement, claim epoch changes, unrelated admissions and expired source
facts reject the old handle. Refetch needs existing recovery and a new attempt.
No raw SQL repairs or in-place generation/observed_at edits are recovery APIs.

The first implementation repeatedly loaded the FULL Root prior before hashing
that set. Its50k/four-reader run failed the unchanged1.5x gate:1.1796x. The failure
is retained in logs/capacity-r5-50000-16.log on the owned test host; correctness
assertions held, but performance was NO-GO. An earlier64-read pilot was aborted
for baseline fault isolation, not counted as a completed matrix.

The candidate now pushes the EXACT predicate into PostgreSQL BEFORE transfer,
then retains the same Go reference predicate/hash. A common decoder/projection
still reads the latest authoritative identity observation, not its mutable cache.
Normal and forced-generic plans are compared against the full-root reference on
48 combinations of nulls, hashes, sizes, old/latest IDs, wildcard/Unicode/quoted
paths, missing/removed rows and a separate Root. No dependency is dropped for speed.
For ordinary OpenList facts with no provider object ID, a separate static SQL
shape avoids an unnecessary global ID branch in generic plans. Values stay bound.

Kernel reconciliation STILL loads the complete prior. SQL may still scan the
Root, and a large matching/missing/tombstone set may still be expensive. Neither
this filter nor Stage1 is an O(1) scalability claim. No work_mem/resource increase,
new index, identity/deletion relaxation or concurrent Canonical writer is used.
EXPLAIN(ANALYZE,BUFFERS)50k projection evidence is emitted by the owned plan test.

## Controlled performance method

Owned PostgreSQL18 + fake OpenList, ssh dsh/.50, Go1.27.1, CPU1/memory1GiB/pids128/
512MiB tmpfs/loopback-only/restart=no, GOMAXPROCS2/GOMEMLIMIT512MiB soft target.
Purpose guard requires exact loopback index138_test and its owner/PG18/database
marker BEFORE reset. No real OpenList/115, production DB, source file or task call.

ONE actual Root,512 aged durable queued scopes, SQL-preseeded2k/20k/50k directories.
The delayed source offers a late file in each of512 old directories. Only the
requested16/64/400 directories are actually fetched; the rest are UNOBSERVED.
Do not call preseeded directories source traversals or claim the whole Root fresh.

Compare N1/2/4/8 and1250ms real fake-HTTP latency. N1 uses the ALREADY ACCEPTED
version1 single-observation FetchPrototype with one claim/read/commit at a time;
it is not the new lineage implementation padded by its extra single-thread cost.
N2/4/8 use eight-result barriers and the new ordered lineage proof. Record these
different modes/claim caps explicitly. N1 is a fenced serial prototype baseline,
not a claimed measurement of the production executable's unfenced ScanScope.
The finite driver explicitly claims selected scopes; it is not runtime scheduler
or fairness proof. Every case asserts exact request counts, no duplicate list,
peak concurrency<=N, claimed/verified/pending totals, exact Canonical/Q5 identities,
producing Snapshot, generation/admission/Q8 ordering and no extra removal events.
The four-reader >1.5x gate is an assertion, never weakened after a failure.

Receipts include ALL HTTP calls (topology calls as well as forced lists), actual
returned fraction, wall time, queue/read/commit/source-to-Q5Q8 p50/p95/p99 and
post-source wait. Timings include database/client work; they are not pure SQL
lock durations. Quantiles are finite correlated observations, NOT a user SLA.
The physical source change time is the controlled fixture release; the test does
not measure CloudSite projection/browser rendering or a real115 download.
PG temp counters are cumulative checkpoints and may include a prior case;
resource samples/time high-water are not continuous lock or per-result memory proof.

```sh
export INDEXCORE_ISSUE138_DISPOSABLE=owned-disposable-only
# DSN must target the already purpose-marked owned index138_test, never shared DB.
go test -race -p 1 -count=3 -timeout=3m -run '^TestIssue138SameRoot|^TestIssue138DependencyPushdown' ./internal/runtime/scan ./internal/store/postgres
INDEXCORE_ISSUE138_DEPENDENCY_PLAN=50000 go test -race -count=1 -run '^TestIssue138DependencyQueryPlan$' ./internal/store/postgres -v
INDEXCORE_ISSUE138_SAME_ROOT_POPULATION=2000 INDEXCORE_ISSUE138_SAME_ROOT_FETCHES=16 go test -race -count=1 -timeout=25m -run '^TestIssue138SameRootCapacity$' ./internal/runtime/scan -v
# Repeat2000:64,2000:400,20000:16,50000:16 under the SAME caps and assertions.
```

## Adversarial proof and open gates

Tests cover reversed source replies, invalid/ancestor batches, direct admission
API bypass, malformed roster/coverage, same-claim competing batches, config/policy
ABA, replacement parent, deprecated Root, claim recovery/new epoch, source TTL,
late Hint, peer429/5xx/decode/timeout/cancel and virtual-empty vs real empty removal.
Backend termination proves atomic freeze rollback and Stage2 rollback followed
by frozen-fact recovery. Reopened Store/instance is NOT an OS process crash test.
The actual Runtime is not wired here; this is not a failover/rollout certificate.

NOT_RUN / NOT_IMPLEMENTED:30..60m sustained churn soak, mixed-depth production
capacity, large tombstone/missing populations, Go OS kill, actual upstream limits,
global/per-root/per-provider rate budgets, adaptive429/serial fallback, runtime
in-flight claim limits/fairness and production canary. Those require PR-B and
later explicit acceptance. No throughput measured here grants a real115 rate.
Only sibling cohorts qualify; difficult identity cases conservatively refetch.
Old-binary downgrade with persisted lineage is not certified. Keep134/138 OPEN;
F4 budget/debt and Watch lifecycle remain separate and necessary.
