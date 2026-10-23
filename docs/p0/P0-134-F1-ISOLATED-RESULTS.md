# Issue134 F1: bounded first-discovery candidate

Date: 2026-10-23 UTC. Base:84096becb373f46161b00d91170b503e611bfaa1.
Status: implementation / isolated validation; NOT Architect accepted, NOT deployed.
Refs #134. F2 duplicate requeue suppression and F3 capacity remain separate gates.

## Isolation and module boundary

Validation runs on the requested ssh dsh host192.168.178.50, Go1.27.1,
GOMAXPROCS2 / GOMEMLIMIT512MiB. A new purpose-labelled PostgreSQL18-alpine
container is owned only by Issue134: CPU1, memory1GiB, pids128, restart=no,
loopback-only dynamic port and512MiB tmpfs database storage. No shared local
database, real OpenList, real 115, CloudSite or production service is modified.
The source is an httptest OpenList-compatible fixture, not upstream provider data.
PostgreSQL and the scoped scanner/Kernel/Journal are real for the backlog case.

Database resets/termination are destructive to this disposable test database
only. The backend-crash test additionally requires explicit opt-in, loopback,
the exact purpose-named database, database comment/owner and PostgreSQL18 before
any schema reset. It kills only the session blocked at the tagged child-insert
barrier, never a production or shared test backend. CI separately provisions
this database and runs the crash test, rather than counting its default skip.
An independent purpose-marked database runs existing Issue130 crash tests in
the same owned container. Tmpfs database contents are ephemeral, not artifacts.

Two setup failures are retained, not counted as successful verification:
the Windows working-tree tar retained CRLF in an existing Shell test; a fresh
Git-tree archive with standard LF fixed packaging without changing that script.
The initial long-lived test container then exhausted its512MiB tmpfs with the
default PostgreSQL WAL budget. It was not OOM. A new owned container retained
the SAME CPU/memory/tmpfs limits and enabled max_wal_size64MB, min_wal_size32MB,
checkpoint_timeout30s. Full regression and lifecycle race then passed, ending
with215.4MiB of512MiB used. No consistency assertions, fsync or safety guards
were disabled. Failed-run logs and container metadata remain preserved.

See [ADR007 proposal](../decisions/ADR-007-BOUNDED-FIRST-DISCOVERY.md) for
module dependencies, eligibility, deterministic quotas, opt-in and rollback.
No migrations or collector/Kernel/Canonical/Journal write rules are changed.

## Repeated backlog experiment

Command: focused race suite, package-serial, count3. The fixture starts with401
previously verified NORMAL directories waiting for another scan, then atomically
publishes a new ordinary /fresh child under a verified parent. It is outside any
explicit HIGH subtree and contains a nested directory and one new file.
The same fixture is recreated for legacy and candidate service order each run.

| Measurement | Legacy, flag off | Candidate, flag on |
| --- | --- | --- |
| Fresh directory claim/scan position, all3 runs | 402 | 2 |
| Target file's producing scan position, all3 runs | 403 | 7 |
| Model: seconds waiting before fresh scan at1s/scan | 401 | 1 |
| Model: seconds through target scan at1s/scan | 403 | 7 |
| Measured target-directory claim wall ms, runs1/2/3 | 6750 /6912 /6912 | 27 /28 /28 |
| Claim wall P50 / P95 ms, nearest rank, N3 | 6912 /6912 | 28 /28 |
| Measured fixture end-of-cycle wall ms, runs1/2/3 | 6811 /6973 /6974 | 208 /214 /205 |
| Fixture end-of-cycle P50 / P95 ms, N3 | 6973 /6974 | 208 /214 |

The1s clock is an explicit deterministic model, NOT sleeping HTTP requests and
NOT measured production seconds. Measured wall times exclude fixture setup and
include race instrumentation. Claim is recorded at scanner entry immediately
after committed ClaimWork. Fixture end-of-cycle includes remaining cycle slots
and final assertions, NOT an exact canonical commit timestamp. N3 quantiles are
descriptive only, not a statistically established SLA. Production source-to-Q5
or website P95 with F1 enabled remains UNMEASURED; no <30s production claim.

Each run asserts target Canonical PRESENT, exactly one target resource-added,
no resource-removed, unchanged old-directory removal evidence, producing Snapshot
identity, and exactly one forced source refresh per actual scan. Baseline and
candidate both preserve the same canonical result; only service order changes.

## Fairness and safety matrix

- Real PostgreSQL + initial real HTTP/Kernel directory publication:110 fresh
  folders, a sibling root with40 aged NORMAL items, six new HIGH signals per
  cycle for20 cycles. Every cycle processes a sibling aged item and one fresh
  item;20 sibling items progress, and no fresh item is durably relabelled HIGH.
  The execution scanner in this fairness stress is a NOOP test double: this
  proves operational service order, not110 full canonical/provider scans.
- Planning port:100 cycles, each one aged + at most two distinct-root fresh
  credits + two legacy FIFO slots. Empty fresh cohort probed once per cycle;
  errors stop without legacy fallback. Disabled/direct one-shot behavior retained;
  opted-in cycles smaller than three are rejected.
- Real PostgreSQL proof guards: wrong namespace, removed/non-directory/uncertain
  child, unverified parent, inactive root, ambiguous parent or child, future
  not-before, previously attempted or HIGH work all excluded from the fresh lane.
- New nested child is eligible; already-verified rediscovery is not. A concurrent
  signal invalidates selected version; two concurrent claimers yield one winner.
- Existing atomic child publication forced-error and stale-claim regression.
- Real backend termination AFTER child INSERT and BEFORE parent completion:
  uncommitted child invisible, completion fails, child count remains zero and
  parent retains its original IN_FLIGHT claim. No half-published discovery.

The ten new top-level methods plus the existing atomic-completion method all
passed three consecutive race runs, with no skips in that dedicated matrix.
The eight-level /1000-child limits and accepted explicit DiscoveryScope HIGH
semantics remain in the existing discovery implementation and regression suite.

## Reproduction and remaining gates

Provision an owned loopback PostgreSQL18 database named index134_test, owner
matching the test user, comment indexcore-issue134-owned-disposable. Configure
INDEXCORE_TEST_DATABASE_URL explicitly; never reuse an inherited/shared DSN.

```
INDEXCORE_ISSUE134_DISPOSABLE=owned-disposable-only \
go test -race -p 1 -count=3 \
  -run '^TestIssue134|^TestDiscoveryCompletionAtomicRollbackAndStaleClaim$' \
  ./internal/runtime/config ./internal/store/postgres ./internal/runtime/incrementalexec
```

Dedicated GitHub CI repeats this matrix against its own marked database. Full
test/vet/build and runtime lifecycle race results must be checked at PR HEAD.

Local final Git-tree verification on.50: gofmt, go vet ./..., go build ./...,
go test -p1 -count1 -v ./..., and targeted incrementalruntime/app race all pass.
The full suite reports461 passing top-level methods,16 explicit optional skips
(real Provider/rclone/live fixtures and opt-in scale), zero failures. The ten
new F1 methods, actual backend crash and the separate Issue130 tests did run;
none is hidden among those16 skips. GitHub exact-head CI remains a separate
required gate, not inferred from the local result.

Retained operator logs under /home/nathan/index134-f1-20261023/logs:

- f1-race-repeat3.log SHA256
  e834bf0aa927b218802e2ff5f2acfbb461a128fa4d35f32eeed3a927da4e249b.
- full-final-owned.log SHA256
  cd9455915b99298e8903b91aad1afedce1ca9947d4f585a224f7cc58cadc7cf4.
- vet-final.log, build-final.log, runtime-race.log; initial failed full/PG logs.

F2 repeated-merge suppression: NOT_RUN. F3 2k/20k/50k, source rate limits and
capacity/queue-slope matrix: NOT_RUN. Separate source/ingest/DB timing and
production performance/soak: NOT_RUN. No provider download, production SQL,
root reset, source parallelism or cleanup deletion. Issue134 stays OPEN.
