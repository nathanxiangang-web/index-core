# Issue138 prerequisite: Hint lifecycle test readiness

Scope: test-fixture reliability and evidence only. No production Go source,
configuration, schema, Collector, admission, Canonical, Journal or runtime
scheduling change. This is NOT the concurrent-fetch implementation PR.

## Baseline and failure evidence

Baseline main: `c5bb6a68c5f4b8b98d28d348a604f4b2f82c361a`.
Task: [Issue138](https://github.com/nathanxiangang-web/index-core/issues/138),
reprioritized by [Issue134 comment6381017272](https://github.com/nathanxiangang-web/index-core/issues/134#issuecomment-6381017272).

[Main CI40070457955 attempt1](https://github.com/nathanxiangang-web/index-core/actions/runs/40070457955/attempts/1)
failed in job120802569611, Targeted race suite (hybrid runtime + app lifecycle):

```text
go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
--- FAIL: TestP9ShutdownHoldsWriterLockUntilHandlersFinish (3.08s)
    serve_hint_test.go:286: hint listener 127.0.0.1:37751 never became available
```

The full PostgreSQL18 suite, vet and build had passed. The failing step did
not report a Go race-detector finding. This does not establish that no other
races exist. The original fixture discarded the runServe logger, did not
observe its exit channel while waiting, and reserved then released a TCP port
before runServe bound it. Thus the historical failure cannot distinguish early
startup failure (including address reuse) from delayed startup. **Historical
root cause remains UNKNOWN.** Do not call it a proven runtime defect, a proven
port collision, or a proven harmless flake.

## Modules and minimal repair

- Test-only `p9ObservedHint` decorates the existing transport seam and reports
  the listener address actually owned by runServe. The two affected P9 success
  fixtures request `127.0.0.1:0`; they never release a chosen Hint port and
  attempt to reacquire it later.
- Test-only readiness waits for the bind or the actual runServe exit, preserving
  the underlying error and rejecting a clean early exit. If both signals are
  already ready, process exit wins. The readiness deadline remains3s; shutdown
  join remains5s. This is not a larger timeout or a retry.
- Cleanup cancels and joins runServe before restoring the global test seam or
  closing its pool. The blocking handler is released even on assertion failure.
- Existing real HTTP ingestion assertions and writer-lock-held-until-handler-
  finish/released-after-join assertions remain in place. No auth or ownership
  check is weakened. The new negative fixture holds an occupied port to prove
  exact startup-error propagation and ownership cleanup, not to retroactively
  claim the original CI cause.

Dependencies: these helpers live exclusively in `*_test.go`, wrap the existing
app transport interface and real loopback listener, and use the existing owned
PG fixture. No production API or dependency is added. Tests are sequential as
before; the package-global injection is restored only after actor join. Any
future parallel test conversion requires separate per-test dependency injection,
not simply adding `t.Parallel`.

## Isolated checks on ssh dsh (.50)

Dedicated `index138-ci-pg18`, ownership label `codex-task=index138`, PG18-alpine,
loopback dynamic port, restart=no, CPU1, memory1GiB, pids128,512MiB tmpfs. Go1.27.1,
GOMAXPROCS2/GOMEMLIMIT512MiB. Separate purpose-marked Issue130 test database for
the inherited suite. No running service, real OpenList/115 account or source
was used. GOMEMLIMIT is a Go soft target, not an OS memory ceiling.

Unmodified exact-main evidence:

- Failing P9 method repeated40 times with `-race`: PASS,13.032s.
- Original two-package race command repeated5 times: PASS,
  incrementalruntime63.215s / app67.637s. No reproduction in this sample.
- One controlled failed-job rerun of the SAME main SHA was requested. Attempt1
  remains retained in Actions. Attempt2 status is recorded separately in the PR
  receipt; never relabel attempt1 SUCCESS or infer cause from a green rerun.

Commands (set INDEXCORE_TEST_DATABASE_URL to the owned PG18 fixture first):

```sh
# Unmodified archived c5bb6a68 tree, not the candidate files:
go test -race -p 1 -count=40 -timeout=10m \
  -run '^TestP9ShutdownHoldsWriterLockUntilHandlersFinish$' ./internal/runtime/app
go test -race -count=5 -timeout=5m \
  ./internal/runtime/incrementalruntime ./internal/runtime/app

# Candidate, same PG18 limits:
go test -race -p 1 -count=20 -timeout=5m \
  -run '^TestP9ShutdownHoldsWriterLockUntilHandlersFinish$|^TestP9HintIngestPersistsWithoutExecution$|^TestIssue138' \
  ./internal/runtime/app
go vet ./...
go build ./...
go test -p 1 -count=1 ./...
go test -race -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
```

Candidate checks and exact-head CI are recorded in the PR receipt after execution.
The four new methods cover actual ephemeral listener + HTTP auth failure,
occupied-port error + writer-lock release, simultaneous bind/exit signals, and
clean early exit. Repetition evidence is not proof against every scheduling
interleaving.

## Remaining architecture work and boundaries

Issue138 still needs a separate FetchScope / immutable observation /
CommitFetchedScope extraction and measured isolated proof, BEFORE any default-OFF
bounded runtime pool. Do not invoke the current ScanScope from parallel goroutines:
it also persists Snapshots, allocates admission sequence and executes Kernel.
Adapter/config epoch, root generation, parent identity, claim lineage and source
observed_at must be revalidated at the ordered commit boundary; a prefetch result
is not fresh merely because its eventual commit happens later. Concurrent reads
do not fix F4 freshness debt or authorize Watch retirement.

This prerequisite PR is not architecture acceptance, production enablement or a
throughput/SLA claim. Keep Issues134/138 OPEN and F1/F2 production flags unchanged.
F4-A/F4-B remain separate. No real provider calls, production mutation, queue
reset, unrelated PR126/#133 merge, or source refresh is part of this work.
