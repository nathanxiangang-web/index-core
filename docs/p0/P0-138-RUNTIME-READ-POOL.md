# Issue138 PR-B: runtime read pool candidate

Baseline: PR141 merged896c180, merged-main CI40179782676 SUCCESS. PR142 exact
reviewed headf1de4f4 accepted for prototype integration by6395936509, merged
6e0a128, merged-main CI40181118395 SUCCESS. Those verified gates preceded PR-B.
Architecture proposal: [ADR012](../decisions/ADR-012-RUNTIME-READ-POOL.md).
This document is NOT PR-B acceptance, deployment or a provider-rate approval.

## Module contract and dependencies

`app/config -> sourcebudget + scan + incrementalexec -> incrementalorch -> P10`.
Collector parses facts; Store owns claims/fences; Kernel owns canonical results.
No new queue, migration, source write or parallel canonical commit is added.

- Default reader1 retains serial work execution and five-item cycle ceiling.
  Explicit2/4/8 enables the bounded pool and eight-item ceiling. Watch-attempt
  cap5,cycle wall60s,one event loop and the existing writer lock remain intact.
- A source-budget manager is shared by all incremental readers, including serial
  fallback. All HTTP calls consume global/Root/source-group no-burst spacing,
  including auth,identity and topology. Credentials/bodies are never inspected.
- Source-group defaults to normalized origin. Optional adapter JSON field
  `request_budget_group` binds independent Root clients/endpoints to one operator
  group. It is not an inferred115 account or distributed, multi-process quota.
- Cooldowns are checked before claim. Up to8 read-only Root probes and a rotating
  cursor skip cooled-down Roots without burning attempts; one explicit fair Root
  slot per pool cycle prevents a large older Root monopolizing every slot.
- Same-parent contiguous siblings use the accepted frozen roster. Other cohorts,
  reader1 and fallback use accepted fence-v1 reads/commits through ClaimedScanner.
  The ordinary/manual ScanScope contract is unchanged. Common claim/completion
  logic is shared, including atomic child publication and pending-Hint retention.
- Failed source slots retain typed classes. Successful/canceled/unread peers are
  transient cohort aborts, not mislabeled AUTH/INVALID failures. Failure does not
  retry that source/claim inline. Other independent Roots can still progress.
- Cohort errors/stale fences choose serial fallback for30s; cancellation joins
  readers before reporting all interrupted Roots. Existing recovery/Kernel stale-
  input semantics drain old admitted facts, preserve audit DRAFTs and allow fresh
  claim epochs. DB/unknown completion errors remain fatal/fail-closed.

## Configuration and honest rate limits

|Environment / flag|Default|Bound|
|---|---|---|
|INDEXCORE_INCREMENTAL_READ_WORKERS / --incremental-read-workers|1|1,2,4,8|
|INDEXCORE_SOURCE_GLOBAL_INTERVAL / --source-global-interval|100ms|1ms..1m|
|INDEXCORE_SOURCE_ROOT_INTERVAL / --source-root-interval|100ms|1ms..1m|
|INDEXCORE_SOURCE_PROVIDER_INTERVAL / --source-provider-interval|100ms|1ms..1m|

Backoff starts45s, doubles to15m, never shortens a valid longer Retry-After.
Success after cooldown resets escalation; overlapping success cannot clear a
newer cooldown. State maps are bounded4096 roots/groups; capacity fails closed,
never discards a live limit. Redirects cannot escape the guarded origin.
Pacing/cooldown state is process-local. Durable Work retry floors survive, but
this is NOT a durable cross-process HTTP reservation ledger or a guarantee that
unknown groups retain a cooldown across process loss. Real upstream account
budgets and restart/canary policy still require independent operator proof.

IMPORTANT:100ms global spacing is at most10 HTTP starts/s, not10 directories/s.
A mirror observation can cost5 HTTP calls.400 such observations therefore have
a request-budget lower bound near200s, before other costs, regardless of readers.
Default policy is NOT a promise of two-minute freshness or an approved115 quota.
The isolated latency fixture explicitly uses the SAME1ms lab-only budgets for
every N; those fake-source budgets are never copied into production implicitly.

## Verification and reproduction

All destructive fixtures require the exact loopback purpose-marked PG18
index138_test via testutil.Issue138Pool before schema/reset/source seeding.
Tests are serial; fake HTTP is the only provider. Same owned .50 container caps:
CPU1,memory1GiB,pids128,512MiB tmpfs,loopback-only,restart=no;
GOMAXPROCS2/GOMEMLIMIT512MiB soft target, no shared service restart.

```sh
# Explicit owned-DB DSN/marker required, NEVER a production database.
go test -race -p 1 -count=3 -timeout=5m -run '^TestIssue138Pool|^TestIssue138Budget' ./internal/sourcebudget ./internal/runtime/config ./internal/runtime/app
INDEXCORE_ISSUE138_RUNTIME_FETCHES=16 go test -race -p 1 -count=1 -timeout=5m -run '^TestIssue138PoolRuntimeLatency$' ./internal/runtime/app -v
# Same assertion/config/resource matrix, longer actual work, not SQL seed only:
INDEXCORE_ISSUE138_RUNTIME_FETCHES=400 go test -race -p 1 -count=1 -timeout=25m -run '^TestIssue138PoolRuntimeLatency$' ./internal/runtime/app -v
```

Latency proof uses actual app composition plus the P10 event loop, real writer
ownership and startup recovery, burst cooldown/wake behavior, normal materializer,
claims, source budgets, commits and cancellation. One Root,2k SQL-seeded directory
inventory,actual16/400 1250ms-delayed lists,N1/2/4/8. Source release is an explicit
atomic flag before Runtime.Run.50ms cursor observation checks exact Q5 identity,
Q8 order/producing Snapshot and immutable fence base. Final Work completion and
HTTP counts/duplicates/concurrency are checked after join. Observer timings are
upper observations, not exact database commit timestamps or browser rendering.
Reader1 and pool use cycle ceilings5 vs8; receipts explicitly include both caps.
Four-reader >1.5x is an unchanged assertion, not a retrospective chosen target.

Real OS-crash test forks this test binary under the same owned-DB guard, holds a
DB Stage2 transaction barrier, kills the Go child process, joins it and verifies
writer ownership is recoverable. Startup recovery invalidates the old claim;
the existing Kernel records STALE_INPUT with zero partial Journal, then fresh
epochs converge exactly once. Fresh read-only refetch after recovery is disclosed;
no uncertain source write or old claim is retried. Child helper's default SKIP is
intentional; parent invokes it explicitly. This finite crash proof is NOT soak.

Per-HEAD results, original failed compile/fixture runs, resource samples and raw
latency receipts belong to the associated PR receipt. Failed cases are retained;
do not treat an earlier green working tree as exact-head success.
NOT_RUN unless explicitly evidenced:30..60m sustained churn, runtime20k/50k
throughput,large tombstone populations,real provider/canary/production rollout.
F4 budget/debt and Watch lifecycle remain separate; keep134/138 OPEN.
