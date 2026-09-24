# Incremental P2 — Future Deterministic Test Matrix

> Status: **PROPOSED_FOR_ARCH_REVIEW — P2 DESIGN ONLY**
>
> Parent: #57 · Executing issue: #69 · Plan: `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`
>
> **PRODUCTION SCHEDULER: NOT AUTHORIZED** · These tests are for a **later authorized implementation
> phase**; P2 ships no code, no SQL, and no tests.
>
> `FROZEN_CONTRACT_CHANGES: NONE`

Covers Issue #69 §J. Each test must be **deterministic** (fake clock / injected dependencies where
needed) and must assert the contract in `INCREMENTAL-P2-STATE-CONTRACT.md` and the transitions in
`INCREMENTAL-P2-STATE-MACHINES.md`. Postgres-backed tests run on real PostgreSQL; no new Canonical
path may be introduced.

## 1. Watch scheduling and recovery

| ID | Test | Asserts |
| --- | --- | --- |
| T-W1 | due-watch recovery after restart | for a `HOT`/`WARM` watch with `next_due_at <= now` and no active defer, startup recomputes due state from persistence and emits `POLL_SCHEDULE`; `COLD`/`DISABLED` never become due from a stale timestamp; no reliance on in-memory timers. |
| T-W2 | overdue watch produces exactly one trigger | one due evaluation ⇒ one `DirtyScopeWork` merge (`signal_seq` +1), not N. |
| T-W3 | `COLD`/`DISABLED` never trigger | stale `next_due_at` on `COLD`/`DISABLED` yields **no** trigger. |
| T-W4 | watch change does not drop work | HOT→WARM→COLD/DISABLED leaves pending `DirtyScopeWork` intact. |

## 2. Coalescing and lost-wakeup

| ID | Test | Asserts |
| --- | --- | --- |
| T-C1 | duplicate trigger coalescing | repeated triggers for the same key produce **one** active item with growing `signal_seq`; no duplicate rows. |
| T-C2 | **signal 7 / signal 8 lost-wakeup race** | claim(7) → trigger(8) → success(7) results in `work_state = PENDING` with `signal_seq = 8` (never `VERIFIED`); 8 is subsequently processed. |
| T-C3 | success clears only the claimed signal | success with `signal_seq == claimed` ⇒ `VERIFIED` and `last_verified_signal_seq == claimed`. |
| T-C4 | merge during `IN_FLIGHT` preserves provenance | `reason_set`/`source_set` union-merge; `not_before` never pushed later. |

## 3. Verification semantics

| ID | Test | Asserts |
| --- | --- | --- |
| T-V1 | successful `Mutated=false` verification | a fresh P0 `ScanScope` that applies with `Mutated=false` **satisfies** the work item (`VERIFIED`) when `signal_seq == claimed`. |
| T-V2 | success requires fresh coverage + admission | no `VERIFIED` without successful fresh coverage and successful admission/application. |
| T-V3 | work never mutates Canonical directly | the executor only calls P0 `ScanScope`; no direct Canonical/removal writes. |

## 4. Failure / defer

| ID | Test | Asserts |
| --- | --- | --- |
| T-F1 | transient → `RETRY_WAIT` | `TRANSIENT_PROVIDER` sets `RETRY_WAIT` + bounded `not_before`, `consecutive_failures++`. |
| T-F2 | throttled → `RETRY_WAIT` with larger backoff | `THROTTLED` is retried with a larger bounded backoff; no tight loop. |
| T-F3 | auth → `BLOCKED` | `AUTH_OR_PERMISSION` ⇒ `BLOCKED`, **no tight automatic retry**, operator action required. |
| T-F4 | oversized scope → `BLOCKED` | `SCOPE_TOO_LARGE` ⇒ `BLOCKED`; `max_entries` is **not** silently raised. |
| T-F5 | invalid scope / config → `BLOCKED` | `INVALID_SCOPE`/`CONFIG_INVALID` ⇒ `BLOCKED`. |
| T-F6 | **budget defer not counted as failure** | when a cycle cannot select eligible work due to budget, the item stays `PENDING`, `consecutive_failures` and `last_error_class` are unchanged; `budget_defer_count` may increment (observability only). |
| T-F7 | root inactive suspension | non-ACTIVE root ⇒ `SUSPENDED`, state retained; no automatic `ScanScope`. |

## 5. Crash / restart

| ID | Test | Asserts |
| --- | --- | --- |
| T-R1 | crash before claim | `PENDING` unchanged. |
| T-R2 | **stale `IN_FLIGHT` recovery** | startup recovers stale `IN_FLIGHT` to `PENDING`/`RETRY_WAIT`, clears `claimed_signal_seq`, preserves `signal_seq`; no duplicate Canonical lane. |
| T-R3 | crash after provider success, before completion | re-run through P0 is safe (IO3/protected); `signal_seq` still guards newer triggers. |
| T-R4 | crash after Canonical apply, before `VERIFIED` | re-run reconciles safely; work completes via normal CAS; no double removal/evidence. |
| T-R5 | new signal during recovery | merge CAS increments `signal_seq`; recovery must not clear it. |

## 6. Coverage and ordering invariants

| ID | Test | Asserts |
| --- | --- | --- |
| T-I1 | **direct-child parent/child non-collapse** | `/a` and `/a/b` remain separate watch/work rows; a change in `/a/b` is not covered by polling `/a`. |
| T-I2 | no unsafe parent collapse | overlapping paths never merge rows without an explicit coverage-subsumption contract. |
| T-I3 | **same-root serialization** | dirty verification enters the existing same-root admission/application order; an executor cannot bypass earlier root work or become a second Canonical-write lane. |
| T-I4 | full scan does not auto-clear work | a later full scan does not clear dirty work by wall-clock recency; it may satisfy only with equal-or-stronger coverage + freshness + successful admission + no newer signal. |
| T-I5 | `MUTATION_HINT` creates work only | a hint dirties the exact scope; it does **not** create/promote permanent `HOT` policy, and does not touch Canonical/removal directly. |

## 7. Frozen-contract guards

| ID | Test | Asserts |
| --- | --- | --- |
| T-G1 | **no Q1–Q9 changes** | existing Query Contract behavior/tests remain unchanged; operational scheduler state is not exposed through Q1–Q9. |
| T-G2 | no new Canonical-write path | grep/structural checks + integration: dirty executor has no direct Canonical/removal authority. |
| T-G3 | P0 unchanged | `scan.Service.ScanScope` behavior/semantics unchanged; PARTIAL/additive-safe preserved. |
| T-G4 | no Journal event type added | dirty work never writes Journal events itself. |
| T-G5 | migration-free P2 | this design phase ships no SQL/migration and no production Store code. |

## 8. Test-harness requirements (for the later phase)

- **Deterministic clock** and injected selector/executor seams so due/backoff/aging can be tested
  without wall-clock sleeps.
- **Real PostgreSQL** for CAS/unique-key/state persistence; **in-memory** fakes are not sufficient for
  the CAS and recovery tests (T-C*, T-R*).
- **Single-writer** harness only; no distributed/HA simulation.
- **Fake P0 actuator** for failure-injection (T-F*) plus a real mock-provider path for integration
  (as used in P0/P1), so no live 115 calls are needed for determinism.
- Every test asserts an explicit invariant ID from §9.

## 9. Invariant coverage map

| Issue #69 invariant | Tests |
| --- | --- |
| 1 exact `(root_id, scope_key)` | T-C1, T-I1 |
| 2 `EXACT_DIRECT_CHILDREN` | T-I1, T-I2 |
| 3 no parent/child collapse | T-I1, T-I2 |
| 4 one active work per key | T-C1 |
| 5 merge + `signal_seq++` | T-C1, T-C2, T-C4 |
| 6 claim + safe clear | T-C2, T-C3 |
| 7 success ≠ `Mutated=true` | T-V1, T-V2 |
| 8 budget defer ≠ failure | T-F6 |
| 9 restart-safe | T-W1, T-R1..T-R5 |
| 10 single-writer only | T-R2, §8 harness |
| 11 same-root ordering | T-I3 |
| 12 no direct Canonical/removal mutation | T-V3, T-G2 |
| 13 Q1–Q9 unchanged | T-G1 |
## 10. Round-1 review additions (contract sharpening)

These tests lock the semantics added in the Round-1 review of PR #70.

| ID | Test | Asserts |
| --- | --- | --- |
| T-W5 | `COLD` never schedules | a `COLD` watch with a stale `next_due_at` produces **no** `POLL_SCHEDULE`; only `HOT`/`WARM` are periodically polled. |
| T-W6 | poll-due atomicity | due re-check + schedule advance + `DirtyScopeWork` merge commit in **one** transaction; a crash mid-way leaves **neither** advance **nor** merge visible (no duplicated poll signal, no skipped due watch). |
| T-C5 | merge while in each `work_state` | merge is defined and never ignored for `PENDING`/`IN_FLIGHT`/`VERIFIED`/`RETRY_WAIT`/`BLOCKED`/`SUSPENDED`; `BLOCKED`/`SUSPENDED`/`RETRY_WAIT` are **not** auto-cleared by a merge. |
| T-C6 | epoch provenance reset | after `VERIFIED`, a new trigger **resets** `reason_set`/`source_set` (no inheritance from the previous epoch) while `signal_seq` keeps increasing. |
| T-C7 | CAS-conflict completion | a merge during `IN_FLIGHT` bumps `version`; the completion CAS fails → re-read → success recorded **only if** `signal_seq == claimed_signal_seq`; otherwise `PENDING` (signal preserved); no tight retry loop. |
| T-C8 | claim cleared on every exit | every `IN_FLIGHT → non-IN_FLIGHT` transition (success/newer-signal/crash/`RETRY_WAIT`/`BLOCKED`/`SUSPENDED`) sets `claimed_signal_seq = NULL`. |
| T-F8 | counter ownership | a merged `POLL_SCHEDULE` + `MUTATION_HINT` attempt that fails updates **Work** counters; **Watch** counters/attempt fields update **only** when `POLL_SCHEDULE ∈ DirtyScopeWork.current_epoch.source_set`; budget defer updates neither. |
| T-C9 | `not_before` handled per state | a merge into `PENDING` may make it eligible sooner; a merge into `RETRY_WAIT` leaves `not_before` **unchanged** (backoff not cancelled); a new epoch (`VERIFIED`) rebuilds `priority_class`/`first_seen_at`/`last_seen_at`/`not_before`; same-epoch `priority_class` = **max** of outstanding signals. |
| T-C10 | failure/abort CAS re-read | a merge during `IN_FLIGHT` makes the failure/abort CAS conflict → re-read → apply failure/abort **only if** the claim is still valid; otherwise stop (no stale counter update); no tight loop. |