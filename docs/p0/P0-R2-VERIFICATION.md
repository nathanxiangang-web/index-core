# P0-R2: isolated correctness and recovery evidence

Execution Issue: #121. Parent #120 stays OPEN. R1 bounded acceptance:
[#120 comment6281974853](https://github.com/nathanxiangang-web/index-core/issues/120#issuecomment-6281974853).
This is test/evidence work, not P0 reliability acceptance or deployment approval.

## Identity and environment

- Baseline: `a9e2f9875fb86c33c54a525b924ddfad12a878d7` (merged PR119).
- Initial matrix: `887a081f203b843e502c428c5d8c624ae1c9b7d9`.
- Stricter Q8 identity-transition replay and CI race step:
  `845fe3edf2a0d845fd33d8b087d08a5ddd29d368`.
- PR: [#122](https://github.com/nathanxiangang-web/index-core/pull/122), DO NOT MERGE.
- Initial implementation CI: [39361476542](https://github.com/nathanxiangang-web/index-core/actions/runs/39361476542),
  exact head887a081, completed SUCCESS. Stricter implementation-head
  [39361865998](https://github.com/nathanxiangang-web/index-core/actions/runs/39361865998)
  at845fe3e also completed SUCCESS, including the new PG18 R2 race step. Final
  report-head CI is checked separately in the PR review packet; never substitute
  an older-head green run for final exact-head verification.
- Operator requested compilation move to192.168.178.50. Go1.27.1 linux/amd64,
  disposable PostgreSQL18, Docker internal network, no host database ports.
  Test runner has2 CPUs/2GiB; PG has0.5 CPU/256MiB. No production database,
  credential, source directory or product-volume mounts.
- OpenList is an in-memory **HTTP fixture**, not a stock115/OpenList deployment.
  Public dependency download/image preparation precedes internally networked tests.
  Native115 mutation is impossible from fixture configuration. Credentials and
  paths in tests are synthetic; no real names, identities or secrets are exported.
- A preliminary public-host compile attempt had a test enum-name error and ran
  no new scenario successfully. On operator instruction those owned test
  containers were stopped, logs/data retained; no further builds occur there.

## Modules and oracle

`internal/runtime/scan/p0_r2_matrix_test.go` reuses existing scoped-root/PG
helpers, ScanScope, Coordinator and read-only QueryReader. The independent
source map is created before Canonical, and explicitly mutated only inside
the disposable fixture. It is never reconstructed from Canonical or Journal.

Every checkpoint compares the complete expected PRESENT path set to Q6,
validates Q4 direct-child hierarchy and Q5 unique path/ID/PRESENT, and replays
all Q8 resource presence transitions against Q3 including historical tombstones.
It checks event ordering, per-generation ordering, uniqueness, complete record
coverage and root generation. Preserved sibling IDs are compared explicitly.
After mutation/recovery, three subsequent observations must keep exact IDs,
generation and Q8 unchanged. Failure scenarios make four observations while
the source is untrusted, then restore the source and check three more cycles.

Trust matters: a confirmed direct-child absence may match the physical fixture;
an untrusted omission must preserve prior Canonical truth, even though the
fixture source is temporarily smaller. Positive facts may still be added.
No tests derive destructive authority from a consumer-side list or fixed274.

`internal/store/postgres/p0_r2_retry_test.go` reuses existing real-PG work/watch
APIs. Four eligibility reads preserve exact work rows; permitted CAS transitions
retain attempts, failures and signal sequence and reject stale second recovery.
No canonical resources or resource Journal events may be invented by retry logic.

Scan checkpoints report work/watch row counts0 because no scheduler is installed
in that fixture. Repeated explicit scans are **not** claimed as a real daemon
watch/claim integration test. The separate17-case Store matrix exercises persisted
watch/work policy; existing runtime lifecycle/race suites remain required.

## Focused results:49 scenarios PASS

Final implementation845fe3e run on.50:24.469s, Go test JSON log SHA256:
`df02e18f03e3decf5d869037050a23974d192f239d12da5ad0dae5b2b5b5fe56`.
Each scan fixture begins with13 PRESENT records,13 ADD events, generation7.
Counts below are observed fixture counts, not production invariants.
`+A/-R` is the Q8 delta from baseline; historical rows remain in Q3.

| Source change | PRESENT after | Q8 delta | Generation | Next3 cycles | Result |
| --- | ---: | --- | --- | --- | --- |
| Child file deleted |12|+0/-1|8|Exact identity/generation/Q8 stable|PASS|
| Root file deleted |12|+0/-1|8|Stable|PASS|
| Directory plus descendants deleted |9|+0/-4|8|Stable; prefix sibling intact|PASS|
| Empty directory deleted |12|+0/-1|8|Stable|PASS|
| Delete, then recreate same path |12 then13|+0/-1 then+1/-0|8 then9|Stable at both stages; new ID, old tombstone retained|PASS|
| Hash-only same-parent rename |13|+1/-1|8|Stable; no invented identity continuity|PASS|
| Cross-scope move with equal-content copy |13|+1/-1|9|Stable; existing copies/siblings retain IDs|PASS|
| Additional equal-content copy |14|+1/-0|8|Stable; distinct canonical ID|PASS|

ADR-004 does **not** authorize moving a live hash-only candidate. Thus these
rename/move fixtures correctly converge by ADD+REMOVE, not a guessed MOVED event.
Stable provider-ID recognition and unique MISSING-within-horizon semantics are
covered separately by existing pure identity tests, not claimed for this driver.

| Untrusted observation | Canonical/Q8 during four fault cycles | Recovery+next3 | Result |
| --- | --- | --- | --- |
| Virtual-only success masks provider absence |All13 prior rows retained; one positive virtual child added, zero removals|Restore13; only virtual child removed once, then stable|PASS|
| Hidden mount |13 retained; no new events|Stable baseline IDs|PASS|
| Nested mount |13 retained; no new events|Stable baseline IDs|PASS|
| Alias covering storage |13 retained; no new events|Stable baseline IDs|PASS|
| Unknown covering driver |13 retained; no new events|Stable baseline IDs|PASS|
| Failed covering storage |13 retained; no new events|Stable baseline IDs|PASS|
| Topology token expired/401 |13 retained; no new events|Stable baseline IDs|PASS|
| Topology GET503 |13 retained; no new events|Stable baseline IDs|PASS|
| Topology GET redirect |13 retained; no new events|Stable baseline IDs|PASS|
| Truncated topology total |13 retained; no new events|Stable baseline IDs|PASS|
| Topology changes between checks |13 retained; no new events|Stable baseline IDs|PASS|
| Referenced environment variable empty |13 retained; no new events|Stable baseline IDs|PASS|
| Wrong topology credential |13 retained; no new events|Stable baseline IDs|PASS|
| Credential rotates, then client binding updated |13 retained while old token denied; no new events|New token restores trusted observations, stable IDs|PASS|
| Source HTTP503 |Error; old13 retained; no new events|Stable baseline IDs|PASS|
| Source API403 |Error; old13 retained; no new events|Stable baseline IDs|PASS|
| Source list/total mismatch |Error; old13 retained; no new events|Stable baseline IDs|PASS|
| Source base namespace drift |Error; old13 retained; no new events|Stable baseline IDs|PASS|
| Request-context cancellation |Error; old13 retained; no new events|Stable baseline IDs|PASS|

| Ordering / transaction case | Observation and after | Next3 | Result |
| --- | --- | --- | --- |
| Root coverage versus deep child |Four root scans retain unobserved descendant; direct child empty scan removes exactly1,13->12/gen8|Root+child replay stable|PASS|
| Old empty response after newer positive generation |New positive14/gen8; old response STALE_INPUT, no removal/event|Stable|PASS|
| Old adapter digest |Old empty response STALE_INPUT;13/gen7 unchanged|Stable|PASS|
| Old observation time |Old empty response STALE_INPUT;13/gen7 unchanged|Stable|PASS|
| Journal insert fails; reopen Store/client and recover |Rollback keeps13/gen7/Q8 unchanged; durable head recovery yields12/gen8/exactly1 REMOVE|Stable|PASS|

Fresh Store/client recovery closes and reopens the PG pool and reconstructs the
runtime service. It is **not** a subprocess SIGKILL, DB restart or production
rollback drill. Delayed-input tests retain a real draft before admitting newer
input/config; they do not simulate all overlapping HTTP-completion schedules.

### Retry/watch matrix:17 scenarios PASS

Four selector observations are read-only and retain exact work/history; only
eligible rows are then advanced by one guarded CAS. Canonical count/resource
Q8 count remain0. Inactive-root lifecycle itself emits its ordinary root event.

| Policy / state | Eligible | Result |
| --- | --- | --- |
| HOT operator mirror root, due, failure1 |Yes|PASS|
| WARM operator mirror root, due, failure1 |Yes|PASS|
| Configured credential env names only |Yes, scheduling only; NOT deletion authority|PASS|
| No watch (manual-origin work) |No|PASS|
| Disabled watch |No|PASS|
| Cold watch |No|PASS|
| Adaptive-only, no operator policy |No|PASS|
| Wrong cadence |No|PASS|
| Child scope, not root |No|PASS|
| Mirror off |No|PASS|
| Missing topology reference |No|PASS|
| Partial credential reference pair |No|PASS|
| Wrong collector |No|PASS|
| Retry not yet due |No|PASS|
| Inactive root |No|PASS|
| Second consecutive failure |Yes; history retained, stale CAS rejected|PASS|
| Third consecutive failure |No; failures remain3|PASS|

## Existing regression coverage reused

The full suite retains, without modification, pure directory/identity tests,
live-copy/horizon/tombstone tests, virtual-overlay regressions, credential renewal
and redirect isolation, coverage fences, generic P10 allowlist and runtime
lifecycle/race tests. The new matrix does not replace those proofs.
Existing `TestP3BlockedKeepsPostClaimBarrier` and admission-recovery tests cover
blocked work and retained sequence barriers; the49 new scenarios do not claim
to independently repeat every existing lifecycle transition.

The first.50 run failed two **new test assumptions**: hash-only rename was
incorrectly expected to keep identity, and a nonexistent Watch was incorrectly
given a POLL_SCHEDULE claim. ADR-004 and existing ClaimWork/watch requirements
justify correcting those fixtures. Original logs are retained; no production
implementation or existing acceptance assertion was weakened. No new confirmed
core defect was established, so no speculative defect Issue was opened.

## Reproduction and final validation

Use a disposable PostgreSQL18 database: existing testutil.ResetSchema deliberately
resets its schema. NEVER point INDEXCORE_TEST_DATABASE_URL at a real installation.
Use one package at a time (`-p1`) because helpers share that disposable schema.

```sh
go version #1.27.1
gofmt -l . #must be empty
go vet ./...
go build ./...
go test -p 1 -count=1 ./...
go test -p 1 -count=1 -run '^TestP0R2' ./internal/runtime/scan ./internal/store/postgres
go test -race -p 1 -count=1 ./internal/runtime/incrementalruntime ./internal/runtime/app
go test -race -p 1 -count=1 -run '^TestP0R2' ./internal/runtime/scan ./internal/store/postgres
```

Verified checkpoints (final report-head CI receipt remains a review gate):

| Execution | Exact source | Observed result |
| --- | --- | --- |
| Focused49 on.50 |845fe3e|PASS24.469s, checksum above|
| Local full pipeline on.50 |887a081|PASS214.468s: format, vet, build, full enabled PG18 tests, runtime/app race, and new49-scenario race|
| Initial GitHub full CI39361476542 |887a081|SUCCESS|
| GitHub full CI39361865998 |845fe3e|SUCCESS, including separate PG18 R2 matrix race|

The first local full pipeline failed because the test image lacked `bash` for
the existing P12 mode guard. Adding bash to the disposable test image fixed the
environment; no repository assertion or implementation was changed. A subsequent
implementation-head local full repeat is reported with its checksum in the PR
review packet, not presumed from the earlier run. No new49-scenario test skipped.

The enabled full suite has16 **pre-existing explicit skips**, not hidden passes:

- Real-source URL/credential flags not provided (6): `TestRealAListInstance`,
  `TestAlphaRuntimeWithRealAListSource`, both `TestLiveP0PhaseA/B` probes
  (`TestLiveP0PhaseABaseline`, `TestLiveP0PhaseBScopedIncrement`), and
  `TestLiveP1HotScopesPhaseABaseline` / `TestLiveP1HotScopesPhaseBCycle`.
- No rclone binary in the isolated image (8): `TestAdapterRealProcessLocalBackend`,
  `TestConfiguredConfigPathIsUsed`, `TestAlphaRuntimeOverHTTP`, `TestAlphaEndToEnd`,
  `TestWorkerRecoversUnadmittedSubmittedSnapshot`, `TestScanRerunDrainsOlderPending`,
  `TestScanReturnsErrorOnRcloneFailure`, `TestScanEndToEndWithRealRclone`.
- Opt-in harnesses disabled (2): `TestGate4ReferenceConsumerFixture`
  (`INDEXCORE_GATE4_FIXTURE`) and `TestScalePopulationAndDelta`
  (`INDEXCORE_SCALE_TEST`). This is not a post-fix20k/50k result.

Success of `go test ./...` means every enabled test passed, not that these skipped
provider/scale probes ran. In particular, the granted read-only real account was
not injected into live mutation/scan probe flags just to remove skips.

## Separate authorized real-source read and limitations

The operator separately provided an OpenList account for readonly source access.
The product host's OpenList-settings route returned404; the verified OpenList
host's public settings/login/me/list returned success. One cached, non-refreshing
root page returned4 directories. No filenames/base path/token were exported;
password/token stayed in process memory only. This is access confirmation, NOT
independent production deletion/reappearance testing or fresh absence evidence.

No real source deletion/move/rename, production scan, mirror toggle, mount edit,
provider download, credential rotation, live deployment, schema or runtime code
change occurred under R2. No CloudSite source/runtime changes. Synthetic matrices
do not certify all stock-provider pagination/cache behavior or an exact latency
SLA. New process/DB crash drills, post-fix20k/50k capacity and long-period soak
remain R4/R5; R3 diagnostics are unstarted. Architect acceptance is still required.
