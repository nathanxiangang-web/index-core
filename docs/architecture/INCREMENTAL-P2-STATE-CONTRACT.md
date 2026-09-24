# Incremental P2 — Durable State Contract (`ScopeWatchState` + `DirtyScopeWork`)

> Status: **PROPOSED_FOR_ARCH_REVIEW — P2 DESIGN ONLY**
>
> Parent: #57 · Executing issue: #69 · Plan: `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`
>
> **PRODUCTION SCHEDULER: NOT AUTHORIZED** · **STORE MIGRATION / DIRTY TABLES: NOT AUTHORIZED**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

This document covers Issue #69 §A (`ScopeWatchState`), §B (`DirtyScopeWork`), §D (lost-wakeup /
coalescing proof), §E (trigger contract), §F (failure/defer matrix), §H (crash/restart matrix) and
§I (interaction matrix). State machines: `INCREMENTAL-P2-STATE-MACHINES.md`; schema sketch:
`INCREMENTAL-P2-SCHEMA-SKETCH.md`; future tests: `INCREMENTAL-P2-TEST-PLAN.md`.

This is a **logical contract**, not SQL. No migration, Store code, scheduler, or executor is
authorized by this document.

## 1. Scope identity and coverage (foundation for A and B)

Logical key: **`(root_id, scope_key)`**.

- `root_id`: the existing IndexCore root identifier (opaque to this model).
- `scope_key`: a **normalized, root-relative directory path**.
  - `"/"` is valid and denotes the root's own direct children.
  - Normalization: collapse nothing semantically; reject `..`, absolute escape, duplicate-slash
    ambiguity, empty interior segments, and provider-opaque identifiers. The Kernel-facing key must
    be provider-neutral.
  - Two keys are equal iff their normalized forms are byte-equal.

Coverage contract (frozen at P0):

```text
coverage_class = EXACT_DIRECT_CHILDREN
recursive      = false
```

Therefore `/a` does **not** cover `/a/b`. **No parent/child collapse** is permitted unless a future
verifier carries an explicit coverage contract proving subsumption. Overlapping paths never merge
watch or work rows.

## 2. `ScopeWatchState` — recurring observation policy (§A)

Purpose: persist **recurring policy and next-due state**. It is not a queue and never triggers a
Canonical write directly; it only produces triggers.

### 2.1 Fields (conceptual)

| Field | Conceptual type | Semantics |
| --- | --- | --- |
| `root_id` | id | Owning root; part of the logical key. |
| `scope_key` | normalized path | Part of the logical key (§1). |
| `watch_state` | enum | `HOT` \| `WARM` \| `COLD` \| `DISABLED` (§2.2). |
| `cadence_class` | enum | Policy class selecting an interval (e.g. `HOT_120`, `WARM_600`, `COLD_OFF`). |
| `effective_interval` | duration | Resolved interval for this row; `NULL` iff `COLD`/`DISABLED`. |
| `source_set` | set<source> | Provenance of the current watch policy (§2.4). |
| `priority_class` | enum | `URGENT`\|`HIGH`\|`NORMAL`\|`LOW` used for tie-break/fairness only. |
| `last_due_at` | instant, nullable | When the next-due transition fired (the schedule point). |
| `last_attempt_started_at` | instant, nullable | Start of the most recent scheduled attempt started against this watch. |
| `last_attempt_finished_at` | instant, nullable | End of that attempt (success or failure). |
| `last_success_at` | instant, nullable | Most recent successful coverage of this scope (any trigger source). |
| `next_due_at` | instant, nullable | Next instant the watch is due; `NULL` iff not scheduled. |
| `consecutive_failures` | int | Consecutive **provider** failures attributable to the watch schedule; budget defer does not increment (§6). |
| `last_error_class` | enum, nullable | Provider-neutral failure class of the most recent failed attempt (§6). |
| `deferred_until` | instant, nullable | If set, the watch must not be selected before this instant (scheduling pressure). |
| `created_at` / `updated_at` | instant | Bookkeeping. |
| `version` | int | Optimistic-concurrency / CAS token (§2.8). |

### 2.2 `watch_state`

- `HOT` / `WARM` / `COLD`: schedule classes; only these produce `POLL_SCHEDULE` triggers.
- `DISABLED`: no scheduling. Retained for audit; selection ignores it entirely.
- `DISABLED` is **not** deletion. Watch demotion (HOT→WARM→COLD) must **never** discard pending
  `DirtyScopeWork` (§15 of the plan; §8 here).

### 2.3 `cadence_class` and `effective_interval`

- `cadence_class` is policy; `effective_interval` is the resolved value used for `next_due_at`.
- P1's `120 s` is **feasibility evidence, not a frozen SLA**. No threshold is frozen by P2.
- `effective_interval` changes require a policy transition (operator or adaptive policy), not a
  silent mutation.

### 2.4 `source_set` (provenance)

- **Source ≠ reason** (§5). `source_set` records where the watch policy came from:
  `OPERATOR_POLICY`, `ADAPTIVE_POLICY`, `MIGRATED`, `BACKSTOP_ENROLL`.
- New provenance merges into the set; it does **not** overwrite earlier provenance.
- A `MUTATION_HINT` **must not** be recorded as permanent watch policy and must not by itself create
  a `HOT` watch (§5.3).

### 2.5 `priority_class`

- Advisory ordering hint only. It never overrides same-root FIFO (§8) and never changes coverage.

### 2.6 Time fields and due derivation

- `next_due_at` is authoritative; `last_due_at` records the schedule instant that produced it.
- A watch is **due** iff `watch_state ∈ {HOT,WARM,COLD}` **and** `next_due_at <= now` **and**
  (`deferred_until IS NULL OR deferred_until <= now`).
- Scheduling is derived from **persisted state**, never from an in-memory timer event.

### 2.7 Failure fields

- `consecutive_failures` and `last_error_class` describe the **watch schedule**, not the work item.
- Budget defer (§6) is **not** a failure: it must not increment `consecutive_failures` or set
  `last_error_class`.

### 2.8 `version` / CAS semantics

- Every mutation of a watch row is a compare-and-swap on `version`:
  `UPDATE ... SET version = version + 1 WHERE root_id=? AND scope_key=? AND version = expected`.
- A failed CAS means a concurrent (or recovery) writer changed the row; the caller must re-read and
  re-derive, never blind-write.
- The row is claimed by at most one single-writer process (no distributed lease; §7).

### 2.9 Restart behavior for overdue watches

- On single-writer startup, **no in-memory timer state is trusted**. Due watches are recomputed from
  persisted rows.
- If `next_due_at <= now` at startup, the watch is due immediately; it produces a
  `POLL_SCHEDULE` trigger on the first scheduling pass.
- Restart must lose **neither** due watches **nor** pending dirty work (§7).
- A stale `last_attempt_started_at` without a matching finished value does not suppress due-ness.

### 2.10 Watch invariants

1. `watch_state = DISABLED` ⇒ `next_due_at IS NULL`.
2. `watch_state ∈ {COLD}` ⇒ no schedule trigger even if `next_due_at` is set historically; selection
   uses `watch_state`, not merely the timestamp.
3. `consecutive_failures` counts only provider-class failures; restart does not reset it, but a
   successful coverage resets it to `0`.
4. Changing `watch_state`/`cadence_class`/`effective_interval` is CAS-guarded and must not touch any
   `DirtyScopeWork` row.

## 3. `DirtyScopeWork` — coalesced verification intent (§B)

Purpose: represent **at most one** outstanding requirement to run one safe verification pass for an
exact scope, through the accepted P0 `scan.Service.ScanScope` path.

### 3.1 Fields (conceptual)

| Field | Conceptual type | Semantics |
| --- | --- | --- |
| `root_id` | id | Part of the logical key. |
| `scope_key` | normalized path | Part of the logical key. |
| `work_state` | enum | `PENDING`\|`IN_FLIGHT`\|`VERIFIED`\|`RETRY_WAIT`\|`BLOCKED`\|`SUSPENDED` (see `STATE-MACHINES.md`). |
| `signal_seq` | bigint, monotonic | Durable count of merged triggers for this work item; incremented on every merge (§4). |
| `claimed_signal_seq` | bigint, nullable | Value of `signal_seq` captured when an attempt claimed the item (null when not IN_FLIGHT). |
| `reason_set` | set<reason> | Why verification is wanted (§5). Union-merged, never overwritten. |
| `source_set` | set<source> | Where the triggers came from (§5). Union-merged, never overwritten. |
| `priority_class` | enum | `URGENT`\|`HIGH`\|`NORMAL`\|`LOW`. |
| `first_seen_at` | instant | First merge time (stable; used for aging/fairness). |
| `last_seen_at` | instant | Latest merge time. |
| `not_before` | instant, nullable | Earliest instant this work may be attempted (retry backoff / defer). |
| `attempt_count` | int | Total attempts started (diagnostic; survives retries). |
| `consecutive_failures` | int | Consecutive provider-class failures for this work item. |
| `last_attempt_started_at` | instant, nullable | Start of most recent attempt. |
| `last_attempt_finished_at` | instant, nullable | End of most recent attempt. |
| `last_error_class` | enum, nullable | Provider-neutral class of the most recent failure. |
| `last_verified_at` | instant, nullable | Time of the most recent successful verification. |
| `last_verified_signal_seq` | bigint, nullable | `signal_seq` that was satisfied by that verification. |
| `created_at` / `updated_at` | instant | Bookkeeping. |
| `version` | int | CAS token (§3.7). |

### 3.2 `work_state`

- Exactly one **active logical** item exists per `(root_id, scope_key)` (§4).
- `VERIFIED` is a terminal-for-this-signal state; a **newer signal** re-opens the item (§4).
- `SUSPENDED` is policy/lifecycle-driven (e.g. root non-ACTIVE), not a failure.

### 3.3 `signal_seq` and `claimed_signal_seq`

- `signal_seq` increments on **every** merged trigger (poll due, hint, manual, recovery, backstop).
- It is **durable and monotonic**; it must not be reset by state transitions other than a fresh
  item creation after removal/compaction.
- `claimed_signal_seq` is set atomically at claim time and is the basis of the completion CAS (§4).

### 3.4 `reason_set` and `source_set`

- Both are **sets**, union-merged on every trigger; existing members are never removed by a newer
  trigger, and a newer trigger's provenance never replaces older provenance.
- `reason_set` answers "why"; `source_set` answers "who asked". They evolve independently.

### 3.5 Attempt/verification fields

- `last_verified_at` / `last_verified_signal_seq` are written **only** on successful coverage that
  satisfies the claimed signal (§4).
- `last_attempt_finished_at` is written for both success and failure.
- Success is defined by **fresh successful scope coverage + successful admission/application**, not
  by `Mutated=true` (§5 of the plan; §5 here).

### 3.6 `not_before`, `attempt_count`, `consecutive_failures`

- `not_before` is scheduling eligibility (backoff/defer); it is independent of `priority_class`.
- `consecutive_failures` increments only on provider-class failures; budget defer does not touch it.
- `attempt_count` is diagnostic and never used to gate correctness.

### 3.7 `version` / CAS semantics

- All mutations are CAS on `version` (same rule as §2.8).
- Claim, merge (`signal_seq++`), completion, and failure transitions each perform a single CAS. A
  merge that races a completion must be serialized by CAS so that the merge is never lost (§4).
- The item is owned by the single writer process; `IN_FLIGHT` is **not** a distributed lease.

### 3.8 Work invariants

1. At most one active logical item per `(root_id, scope_key)`.
2. `claimed_signal_seq IS NOT NULL` iff `work_state = IN_FLIGHT` (during an attempt).
3. `claimed_signal_seq <= signal_seq` always.
4. `last_verified_signal_seq <= signature` of the verification, and `<= signal_seq` at write time.
5. `signal_seq` never decreases for the lifetime of a row except on explicit compaction after
   `VERIFIED` with no newer signal.
6. Work state transitions never mutate Canonical Inventory or removal evidence (§8).

## 4. Lost-wakeup / coalescing proof (§D)

**Invariant C1** — there is at most one active logical `DirtyScopeWork` per `(root_id, scope_key)`.

**Invariant C2** — every new trigger **merges** into that item and **increments durable
`signal_seq`** in the same CAS-guarded transition.

**Invariant C3 (lost-wakeup safety)** — a successful attempt may transition to `VERIFIED` **only if
`current signal_seq == claimed_signal_seq`**. Otherwise a newer signal arrived during the attempt and
the item must remain/re-enter `PENDING` (never silently dropped).

### 4.1 Required CAS rule

```text
# merge (any trigger, including during IN_FLIGHT):
CAS(version=v -> v+1):
    signal_seq     = signal_seq + 1
    last_seen_at   = now
    last_due_at?   (poll only)
    reason_set     = reason_set ∪ new_reasons
    source_set     = source_set ∪ new_sources
    not_before     = min(not_before, trigger.not_before)   # never push later

# claim:
CAS(version=v -> v+1):  where work_state = PENDING and (not_before IS NULL or not_before <= now)
    work_state         = IN_FLIGHT
    claimed_signal_seq = signal_seq
    last_attempt_started_at = now
    attempt_count      = attempt_count + 1

# successful completion:
CAS(version=v -> v+1):  where work_state = IN_FLIGHT and claimed_signal_seq = k
    if signal_seq == k:
        work_state               = VERIFIED
        last_verified_at         = now
        last_verified_signal_seq = k
        claimed_signal_seq       = NULL
        consecutive_failures     = 0
    else:
        # newer signal arrived during the attempt -> do NOT clear it
        work_state               = PENDING
        claimed_signal_seq       = NULL
        # signal_seq unchanged (the newer signal is preserved)
```

The completion branch is the exact rule that prevents clearing signal `8` after attempt `7`.

### 4.2 Worked example (the required race)

```text
signal_seq = 7
attempt claims 7            -> claimed_signal_seq = 7, work_state = IN_FLIGHT
new trigger arrives         -> signal_seq = 8   (merge CAS; version bump)
attempt 7 succeeds          -> CAS sees signal_seq(8) != claimed(7)
                            -> work_state = PENDING, claimed = NULL
                            -> signal 8 remains pending and WILL be processed
```

Signal 8 is **never** cleared by attempt 7.

### 4.3 Prohibited shortcut

It is invalid to clear dirty work merely because *some* observation of the scope succeeded. Clearing
requires: successful fresh coverage **and** admission/application **and** `signal_seq ==
claimed_signal_seq`.

## 5. Trigger contract (§E)

All triggers converge on the **same** `DirtyScopeWork` merge path. Sources and reasons are separate
sets (§3.4).

### 5.1 Sources

| Source | Produced by | May set watch policy? |
| --- | --- | --- |
| `POLL_SCHEDULE` | due `ScopeWatchState` | No (watch is read-only input). |
| `MUTATION_HINT` | future hint ingress | **No** (§5.3). |
| `MANUAL_OPERATOR` | operator action | May, via explicit policy op only. |
| `PROVIDER_EVENT` | future provider event | No. |
| `RECOVERY` | single-writer startup / repair | No. |
| `FULL_VERIFY_BACKSTOP` | periodic/backstop policy | No. |

### 5.2 Reasons

`POSSIBLE_CHANGE`, `DELETE_HINT`, `MOVE_UNCERTAIN`, `METADATA_UNCERTAIN`, `MANUAL_VERIFY`,
`DRIFT_VERIFY`, `RETRY`. Reasons accumulate; they never overwrite.

### 5.3 Mutation Hint boundary

- A hint creates **immediate dirty work** for the exact scope; it **must not** automatically create or
  promote a permanent `HOT` watch policy.
- A hint must never directly produce a Canonical mutation or removal evidence; it only produces dirty
  work that runs the accepted P0 path.
- Hint ingress is **not implemented** in P2 (no endpoint/API).

### 5.4 Merge semantics

- Multiple triggers merge into the outstanding item; `signal_seq++`, `last_seen_at = now`,
  `reason_set`/`source_set` grow, `not_before` is never pushed later.
- If no item exists (e.g. previously `VERIFIED` and compacted, or never created), a **fresh** item is
  created with `signal_seq = 1`.

## 6. Failure / defer matrix (§F)

Provider-neutral failure classes: `TRANSIENT_PROVIDER`, `THROTTLED`, `AUTH_OR_PERMISSION`,
`SCOPE_TOO_LARGE`, `INVALID_SCOPE`, `ROOT_INACTIVE`, `CONFIG_INVALID`, `INTERNAL`, plus non-failure
**budget defer** and crash-while-`IN_FLIGHT`.

| Case | Next state | Retry eligible? | `consecutive_failures`++ ? | Watch policy change? | Operator action? |
| --- | --- | --- | --- | --- | --- |
| `TRANSIENT_PROVIDER` | `RETRY_WAIT` | Yes, bounded backoff via `not_before` | Yes | No | No |
| `THROTTLED` | `RETRY_WAIT` | Yes, with **larger** bounded backoff | Yes | No | If sustained, yes (surface) |
| `AUTH_OR_PERMISSION` | `BLOCKED` | **No tight automatic retry** | Yes (once per attempt) | No | **Yes** (repair credentials/permission) |
| `SCOPE_TOO_LARGE` | `BLOCKED` | No (not by raising `max_entries`) | Yes (once) | No | **Yes** (authorize fallback) |
| `INVALID_SCOPE` | `BLOCKED` | No | Yes (once) | Watch may become `DISABLED` by policy | **Yes** |
| `ROOT_INACTIVE` | `SUSPENDED` | No (until root ACTIVE) | No | No (watch retained) | No |
| `CONFIG_INVALID` | `BLOCKED` | No | Yes (once) | No | **Yes** |
| `INTERNAL` | `RETRY_WAIT` | Yes, bounded | Yes | No | If repeated, yes |
| **budget defer** | remain `PENDING` (`not_before` may be set) | Yes (next cycle) | **No** | No | No |
| crash while `IN_FLIGHT` | recovered to `PENDING`/`RETRY_WAIT` (§7) | Yes | No (not a provider failure) | No | No |

Notes:

- "Never silently raise `max_entries`": `SCOPE_TOO_LARGE` must not be worked around by widening the
  request.
- Budget defer records scheduling pressure (observability `budget_defer_count`); it is **not** a
  provider failure and must not advance failure counters or error class.
- A failed attempt never mutates Canonical Inventory or removal evidence.

## 7. Crash / restart matrix (§H)

Single active writer per DB. P2 does **not** authorize distributed leases, fencing tokens, or HA.

| Crash point | On restart |
| --- | --- |
| Before claim | Item remains `PENDING`; `not_before`/`signal_seq` unchanged. |
| After claim (IN_FLIGHT, no completion persisted) | Stale `IN_FLIGHT` recovered to `PENDING` (or `RETRY_WAIT` if policy says so); `claimed_signal_seq` cleared; `signal_seq` preserved. |
| After provider success but before work completion | No durable success recorded; item re-runs P0 safely (idempotent, IO3-protected); `signal_seq` still prevents losing a newer trigger. |
| After Canonical apply but before `VERIFIED` persisted | Canonical is already correct; the P0 re-run reconciles to the same/newer state safely (IO3 NOOP or normal reconcile); work then completes via the normal CAS. |
| Restart with stale `IN_FLIGHT` | Recovered as above; no blind re-claim without CAS; no duplicate Canonical write lane. |
| Restart with overdue watches | Recomputed from `next_due_at <= now`; due immediately (§2.9). |
| New signal during recovery | Merge CAS increments `signal_seq`; recovery must not clear it (same rule as §4). |

Recovery is **single-writer**, CAS-guarded, and must not invent distributed coordination.

## 8. Interaction matrix (§I)

| Interacts with | Contract |
| --- | --- |
| **P0 `ScanScope`** | The only verification actuator. Dirty work never calls a different provider path; it never widens `max_entries`; PARTIAL/additive-safe semantics unchanged. |
| **P1 hot polling** | Watch scheduling produces `POLL_SCHEDULE` triggers into `DirtyScopeWork`; P1's bounded cadence/temporal guard remain the executor-side policy. |
| **Future Mutation Hint** | Enters the **same** merge path; creates dirty work only; must not create permanent HOT policy; not implemented in P2. |
| **Full scan** | Does **not** auto-clear dirty work by wall-clock recency. It may satisfy work only if it proves equal-or-stronger coverage, freshness vs the signal, successful admission/application, and no newer signal. Root-level direct-child refresh does **not** verify nested scopes. |
| **Root lifecycle** | Routine scheduling only for `ACTIVE` roots; non-ACTIVE ⇒ work `SUSPENDED`, state retained (not deleted), no automatic `ScanScope`. Existing lifecycle semantics unchanged. |
| **Same-root FIFO** | Dirty verification enters the existing same-root serialized admission/application discipline. No executor may bypass earlier root work or become a second Canonical-write lane. |
| **Q1–Q9** | Unchanged. Operational scheduler state is **not** Canonical state and is not exposed through Q1–Q9. |
| **Canonical Journal** | Dirty work never writes Journal events itself; only the accepted Kernel/reconcile path does. No new Journal event type. |
| **No-removal semantics** | Dirty signals never create removal evidence directly; absence in a scoped observation remains non-removal. |

## 9. Invariant index (Issue #69 §Mandatory invariants, 1–13)

1. Exact `(root_id, scope_key)` with normalized root-relative path — §1.
2. Coverage `EXACT_DIRECT_CHILDREN`; `/a` ≠ `/a/b` — §1.
3. No parent/child collapse without proven subsumption — §1.
4. One active logical `DirtyScopeWork` per key — §3.8, §4.
5. Every trigger merges and increments durable `signal_seq` — §4.1.
6. Claim captures `claimed_signal_seq`; success clears only if no newer signal — §4.1.
7. Success = fresh scope coverage, not `Mutated=true` — §3.5, §5.
8. Budget defer is scheduling pressure, not provider failure — §6.
9. Restart loses neither due watches nor pending work — §2.9, §7.
10. Single-writer recovery; no distributed leases/HA — §7.
11. Same-root admission/application ordering frozen — §8.
12. Dirty work never directly mutates Canonical Inventory or removal evidence — §3.8, §8.
13. Q1–Q9 unchanged — §8.