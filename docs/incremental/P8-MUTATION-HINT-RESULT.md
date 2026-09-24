# Incremental P8 — Mutation Hint Ingestion Prototype — Result

> Status: **IMPLEMENTED — REAL-POSTGRESQL VERIFIED — ARCHITECT REVIEW PENDING**
>
> Executing issue: #88 · Parent: #57 · Plan: `docs/architecture/INCREMENTAL-P8-MUTATION-HINT-PROTOTYPE.md`
>
> Planning PR: #87 · Predecessor: P7 manual incremental command — Issue #85 / PR #86 — ARCHITECT_ACCEPTED (merge `8ae9b01`)
>
> **TRUSTED IN-PROCESS ONLY · ONE HINT -> ONE MERGESIGNAL · SOURCE=MUTATION_HINT · NO PROVIDER I/O AT INGRESS · NO EXECUTION FROM P8 · NO CLI/HTTP TRANSPORT · NO MIGRATION · NO CANONICAL WRITE · DELETE_HINT IS NON-DESTRUCTIVE**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. API

Package `internal/runtime/incrementalhint` (`service.go`):

```go
type Store interface {
    MergeSignal(ctx context.Context, sig state.DirtySignal) (state.DirtyScopeWork, error)
}

type Request struct {
    RootID   string
    ScopeKey string
    Reason   state.TriggerReason
}

type Service struct { store Store; now func() time.Time }

func New(store Store, now func() time.Time) (*Service, error)
func (s *Service) IngestOne(ctx context.Context, req Request) (state.DirtyScopeWork, error)
```

The service returns the existing `state.DirtyScopeWork` directly. No new
persistent Hint/history/receipt table exists. `*postgres.Store` satisfies
`Store`.

## 2. Trust boundary

P8 is an **in-process primitive** callable only from trusted code already running
inside the active single-writer runtime boundary. It does not acquire the global
writer advisory lock itself and adds no second writer. It is not wired into
public HTTP, browser requests, localhost HTTP, message-queue consumers, webhook
handlers, or a concurrent-writer CLI; those need a separate transport/trust
decision not part of P8.

## 3. Mapping semantics

For every accepted call:

```go
acceptedAt := now()
sig := state.DirtySignal{
    RootID:    req.RootID,
    ScopeKey:  req.ScopeKey,
    Source:    state.SourceMutationHint,
    Reason:    normalizedReason,
    Priority:  state.PriorityHigh,
    SeenAt:    acceptedAt,
    NotBefore: &acceptedAt,
}
store.MergeSignal(ctx, sig)   // exactly once
```

Source, priority, seen time, and not-before are P8-owned; the caller cannot set
them. `MergeSignal` logic is not copied. `SeenAt = NotBefore = accepted_at` makes
new/VERIFIED/ordinary-PENDING hints immediately eligible while the existing
state-specific merges still preserve `RETRY_WAIT`/`BLOCKED`/`SUSPENDED` barriers.

## 4. Allowed reasons

Allowed: `POSSIBLE_CHANGE`, `DELETE_HINT`, `MOVE_UNCERTAIN`, `METADATA_UNCERTAIN`.
An empty reason defaults to `POSSIBLE_CHANGE`. `MANUAL_VERIFY`, `DRIFT_VERIFY`,
`RETRY`, and any unknown value are rejected before the Store call. Reason is
provenance only: `DELETE_HINT` never authorizes removal.

## 5. Scope validation

`ScopeKey` is the exact canonical root-absolute directory scope validated with the
existing `state.ValidateScopeKey`. `""`, relative paths, trailing slash, duplicate
slash, `.`/`..` components, and backslashes are rejected before any Store call. No
`path.Clean` and no silent reinterpretation; a file path is not converted to its
parent directory.

## 6. Duplicate semantics

P8 is **row-coalescing, not event-id idempotent**. Repeated identical hints update
the same `DirtyScopeWork` row, increment `signal_seq` once per accepted call, and
keep source/reason sets de-duplicated. `signal_seq` is explicitly documented to
change on replay.

## 7. Work-state evidence (real PostgreSQL)

- **Active first hint** (`TestP8ActiveRootFirstHint`): one row, `PENDING`,
  `signal_seq=1`, `pending_source_set={MUTATION_HINT}`,
  `pending_reason_set={POSSIBLE_CHANGE}` (the empty/default-reason path persists
  its reason provenance), `pending_priority=HIGH`,
  `pending_first_seen_at = pending_not_before = last_seen_at = accepted_at`.
- **Duplicate/replay** (`TestP8DuplicateReplayCoalesces`): still one row,
  `signal_seq=2`, de-duplicated sets, earliest `first_seen`, latest `last_seen`.
- **VERIFIED reopen** (`TestP8VerifiedReopensNewEpoch`): `PENDING` new epoch,
  `signal_seq=2`, epoch provenance only the new hint, `first_seen`/`last_seen`
  reset to the new accepted time, `last_verified_signal_seq=1`.
- **IN_FLIGHT lost wakeup** (`TestP8InFlightLostWakeup`): `claimed_*` unchanged,
  `signal_seq=2`, hint only in `pending_*`; the older `CompleteSuccess` leaves the
  work `PENDING` with the hint surviving.
- **RETRY_WAIT** (`TestP8RetryWaitBarrierPreserved`): state stays `RETRY_WAIT`,
  `signal_seq++`, hint merges, `pending_not_before` (future barrier) unchanged, no
  auto `RetryReady`.
- **BLOCKED** (`TestP8BlockedPreserved`): stays `BLOCKED`, `signal_seq=2`, hint
  provenance retained, no repair.
- **SUSPENDED / inactive root** (`TestP8InactiveRootSuspended`): first hint creates
  `SUSPENDED`, subsequent hint stays `SUSPENDED`, root lifecycle unchanged.

## 8. Watch attribution

`TestP8WatchPolicyUntouched` proves hint-only ingestion leaves the full watch row
(state/cadence/next_due/health/version) unchanged.

`TestP8HintOnlyExecutionIsNotPollSuccess` runs the real P5/P4 + real
`ScanService` path with a blocking scanner: the in-flight claim provenance
contains `MUTATION_HINT` and **not** `POLL_SCHEDULE`, the watch row is unchanged
both during the claim and after `VERIFIED`, and exactly one provider refresh is
issued — hint-only execution does not count as poll-schedule health.

`TestP8CoalescedPollAndHintAttribution` coalesces `POLL_SCHEDULE` + `MUTATION_HINT`
in one pending epoch. Using the same blocking-scanner pattern as the hint-only
test, it reads the Work row **while the attempt is in flight** and asserts
`IN_FLIGHT` with `claimed_source_set` containing **both** `POLL_SCHEDULE` and
`MUTATION_HINT` — direct proof the hint was not dropped during coalesced claim
attribution. After release, one execution satisfies the epoch (no extra signal),
the work reaches `VERIFIED`, and watch attribution occurs because
`POLL_SCHEDULE` was actually claimed.

## 9. No provider I/O at ingress

`TestP8ProductionPackageHasNoProviderDependencies` asserts `service.go` imports
none of `internal/collector`, `internal/runtime/scan`,
`internal/runtime/incrementalexec`, `internal/runtime/incrementalorch`,
`internal/transport`. `TestP8InvalidProviderScopeDeferredToExecutor` shows a
syntactically valid but provider-nonexistent scope (`/missing`) is accepted at
ingress with **zero** provider requests, and the accepted executor later
classifies it as `INVALID_SCOPE` → `BLOCKED` without any new P8 error class.

## 10. Real end-to-end

`TestP8RealEndToEnd` — real PostgreSQL + real P5/P4 + real `scan.Service` +
httptest AList:

```text
P8 hint -> DirtyScopeWork PENDING
  -> P5 -> P4 -> ScanScope -> exactly one refresh=true
  -> PARTIAL Snapshot -> Canonical /a.txt PRESENT
  -> Work VERIFIED
```

## 11. DELETE_HINT non-destructive

`TestP8DeleteHintDoesNotRemove`: canonical `/old.txt` is established through an
accepted hint-only ingestion, then a `DELETE_HINT` on `/` is executed while the
scoped refresh omits `/old.txt`. Afterwards `/old.txt` remains PRESENT with
`removal_evidence_state=NONE`, `missing_since=NULL`, and an unchanged
complete-missing count — PARTIAL scoped absence is not removal authority.

## 12. Unit evidence

`service_test.go` proves malformed scopes and non-hint reasons are rejected before
the Store call, canonical scopes (`/`, `/a`, `/a/b`) are accepted, the empty reason
defaults to `POSSIBLE_CHANGE`, and exactly one `MergeSignal` is called with
`MUTATION_HINT`/`HIGH`/`accepted_at`/`accepted_at`.

## 13. Changed files

```text
internal/runtime/incrementalhint/service.go           (new)
internal/runtime/incrementalhint/service_test.go      (new: 9 tests)
internal/runtime/incrementalhint/integration_test.go  (new: 13 tests)
docs/incremental/P8-MUTATION-HINT-RESULT.md           (new)
```

No `internal/store/postgres/**`, no `internal/incremental/state/**`, no
`internal/runtime/incrementalexec/**`, no `internal/runtime/incrementalorch/**`,
no `internal/runtime/scan/**`, no `internal/runtime/app/**`, no `cmd/**`, no
`internal/transport/**`, no `internal/kernel/**`, no `internal/query/**`, no
`internal/domain/**`, no `internal/collector/**`, no migration, and no
`go.mod`/`go.sum` change. No new TriggerSource, no new state enum, no schema
change.

## 14. Regression results

```text
gofmt -l <Go files>             clean
go vet ./...                    clean
go test -p 1 -count=1 ./...     all packages ok (real PostgreSQL 18)
```

P8 tests = **22** (unit 9 + integration 13). Existing P3/P4/P5/P6/P7 tests remain
green.

## 15. Boundary statement

P8 adds one in-process ingestion primitive only. It adds no standalone hint
writer process, `indexcore incremental hint` CLI, public/local HTTP write
endpoint, webhook/queue consumer/sidecar, provider traversal at ingress,
ScanScope/P4/P5/P6 execution from the P8 service, new state enum, migration, Hint
history/idempotency table, Canonical/Journal/removal write, production polling
scheduler, native delta/provider cursor, direct 115 integration, destructive
removal, or Gate 5. P8 performs validation + one `MergeSignal` + return; a trusted
caller must still run the accepted executor elsewhere to actually verify the hint.

`FROZEN_CONTRACT_CHANGES: NONE`
## 16. Round 1 evidence closeout (Issue #88 review)

The P8 Round 1 review found no production-code blocker and required two
test-only evidence details, both now landed inside
`internal/runtime/incrementalhint/**` (no production change):

1. **Persisted first-hint reason provenance.** `TestP8ActiveRootFirstHint` now
   asserts `pending_reason_set == {POSSIBLE_CHANGE}` for the empty/default-reason
   path, closing the real-PostgreSQL persistence evidence end-to-end.
2. **Direct coalesced claimed-source proof.**
   `TestP8CoalescedPollAndHintAttribution` now uses the blocking-scanner pattern to
   read the in-flight Work row and assert `IN_FLIGHT` with `claimed_source_set`
   containing both `POLL_SCHEDULE` and `MUTATION_HINT`, then releases the scanner
   and keeps the one-execution / `VERIFIED` / watch-attribution assertions.

P8 tests remain 22 (unit 9 + integration 13).

`FROZEN_CONTRACT_CHANGES: NONE`