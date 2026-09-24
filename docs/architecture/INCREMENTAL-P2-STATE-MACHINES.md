# Incremental P2 — State Machines (`ScopeWatchState` / `DirtyScopeWork`)

> Status: **PROPOSED_FOR_ARCH_REVIEW — P2 DESIGN ONLY**
>
> Parent: #57 · Executing issue: #69 · Plan: `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`
>
> **PRODUCTION SCHEDULER: NOT AUTHORIZED** · `FROZEN_CONTRACT_CHANGES: NONE`

Covers Issue #69 §C. Field semantics live in `INCREMENTAL-P2-STATE-CONTRACT.md`.

## 1. Ownership model

- Exactly one **single-writer** process owns all transitions for a DB. No distributed leases, no HA
  fences, no cross-process hand-off (P2 does not authorize them).
- Every transition is a CAS on the row `version` (§2.8 / §3.7 of the contract). A failed CAS means a
  concurrent/recovery writer moved the row: the actor must re-read, never blind-write.
- Three actor roles may drive transitions:
  - **SCHEDULER** — selects due watches and eligible work (read + eligibility transitions).
  - **EXECUTOR** — claims work and runs P0 `ScanScope`, then records success/failure.
  - **RECOVERY / POLICY** — single-writer startup recovery; operator/policy watch changes.

## 2. `ScopeWatchState` machine

States: `HOT`, `WARM`, `COLD`, `DISABLED`.

```text
            (policy/adaptive)              (policy/adaptive)
   COLD ───────────────────────▶ WARM ───────────────────────▶ HOT
     ▲                              ▲                            │
     │  (long no-change)            │ (sustained successful       │ (observed change)
     └──────────────────────────────┴─ no-change) ◀──────────────┘

   any ────────────(operator)────────────▶ DISABLED
```

### 2.1 Transitions

| From | To | Trigger | Guard | Side effects |
| --- | --- | --- | --- | --- |
| `COLD` | `HOT` | `OPERATOR_POLICY` / adaptive | valid policy op | set `cadence_class`/`effective_interval`, recompute `next_due_at` |
| `WARM` | `HOT` | observed change (successful coverage w/ mutation) / operator | — | recompute `next_due_at` |
| `HOT` | `WARM` | policy/adaptive: sustained successful no-change | policy threshold (unfrozen) | recompute `next_due_at` |
| `WARM` | `COLD` | policy/adaptive: long no-change | policy threshold (unfrozen) | `next_due_at = NULL` |
| `any` | `DISABLED` | operator | — | `next_due_at = NULL`; **pending work untouched** |
| `DISABLED` | `HOT`/`WARM` | operator re-enable | — | recompute `next_due_at` |

### 2.2 Invalid transitions

- **No** watch transition may read/clear a `DirtyScopeWork` row (§15 plan, §8 contract).
- **No** automatic transition may be triggered by `MUTATION_HINT` (§5.3 contract).
- **No** transition may set `effective_interval` implicitly while changing `watch_state` without
  recomputing `next_due_at`.
- `HOT`/`WARM` are the **only** periodically scheduled states. `COLD` (retained state only) and
  `DISABLED` must not produce `POLL_SCHEDULE` triggers and are never polled periodically, regardless of
  a stale `next_due_at`.

### 2.3 Scheduling (read-only) transitions

- **Due evaluation** is not a state change: `due = watch_state ∈ {HOT,WARM} AND next_due_at <= now
  AND (deferred_until IS NULL OR deferred_until <= now)`. `COLD`/`DISABLED` are never due.
- Producing a `POLL_SCHEDULE` trigger writes a **DirtyScopeWork** merge, never a Canonical write.
- After producing a trigger, the scheduler advances the watch schedule (`last_due_at = now`,
  `next_due_at = now + effective_interval`) via CAS.
- **Atomicity (v1)**: the due re-check + schedule advance + `DirtyScopeWork` merge commit in **one
  Store transaction per scope** (contract §5.5).

## 3. `DirtyScopeWork` machine

States: `PENDING`, `IN_FLIGHT`, `VERIFIED`, `RETRY_WAIT`, `BLOCKED`, `SUSPENDED`.

```text
                 claim (CAS)                    success: signal_seq == claimed
   PENDING ───────────────────▶ IN_FLIGHT ─────────────────────────────▶ VERIFIED
      ▲   ▲                          │
      │   │ failure: transient/      │ failure: auth/scope/config/internal?  ─▶ BLOCKED
      │   │ throttled/internal       │
      │   └────────────── RETRY_WAIT │ failure: root inactive ─▶ SUSPENDED
      │            (not_before)      │ newer signal during attempt ─▶ PENDING
      │                              │
      └───────────── recovery/backoff / root reactivated / operator ─┘
```

### 3.1 Transitions

| From | To | Trigger | Guard | Side effects |
| --- | --- | --- | --- | --- |
| (none) | `PENDING` | first merge | row absent (new key) | create with `signal_seq = 1`, sets `first_seen_at`/`last_seen_at` |
| `PENDING` | `PENDING` | merge (any source) | item exists | `signal_seq++`, `last_seen_at=now`, **same-epoch** reason/source union, `not_before` not pushed later |
| `PENDING` | `IN_FLIGHT` | claim | `not_before <= now` | `claimed_signal_seq = signal_seq`, `attempt_count++`, `last_attempt_started_at=now` |
| `IN_FLIGHT` | `VERIFIED` | success | **`signal_seq == claimed_signal_seq`** | `last_verified_at`, `last_verified_signal_seq=claimed`, **clear claim**, `consecutive_failures=0` |
| `IN_FLIGHT` | `PENDING` | success but newer signal | `signal_seq != claimed_signal_seq` | **clear claim**; **preserve** `signal_seq` (newer signal pending) |
| `IN_FLIGHT` | `PENDING` | crash | restart recovery | **clear claim**; not a failure |
| `IN_FLIGHT` | `RETRY_WAIT` | `TRANSIENT_PROVIDER` / `THROTTLED` / `INTERNAL` | retry allowed | **clear claim**, `consecutive_failures++`, `last_error_class`, `last_attempt_finished_at=now`, `not_before = now + backoff(..)` |
| `IN_FLIGHT` | `BLOCKED` | `AUTH_OR_PERMISSION` / `SCOPE_TOO_LARGE` / `INVALID_SCOPE` / `CONFIG_INVALID` | — | **clear claim**, `consecutive_failures++`, `last_error_class`, `last_attempt_finished_at=now`; **no tight retry** |
| `IN_FLIGHT` | `SUSPENDED` | `ROOT_INACTIVE` | — | **clear claim**, `last_attempt_finished_at=now`; no failure counter; state retained |
| `RETRY_WAIT` | `PENDING` | `not_before <= now` | — | become eligible again |
| `RETRY_WAIT` | `RETRY_WAIT` | merge | — | `signal_seq++`, same-epoch union; does **not** cancel the existing backoff |
| `BLOCKED` | `PENDING` | operator/repair resolves | explicit | resume normal path |
| `BLOCKED` | `BLOCKED` | merge | — | `signal_seq++`, same-epoch union; stays blocked (no auto-clear) |
| `SUSPENDED` | `PENDING` | root becomes ACTIVE | — | resume; `signal_seq` preserved |
| `SUSPENDED` | `SUSPENDED` | merge | — | `signal_seq++`, same-epoch union; no attempt while root non-ACTIVE |
| `VERIFIED` | `PENDING` | merge | — | **new epoch**: `signal_seq++`, **reason/source reset** to the new trigger |

### 3.2 Invalid transitions

- `IN_FLIGHT → VERIFIED` when `signal_seq != claimed_signal_seq` (**forbidden**; must go to
  `PENDING`).
- Any `→ VERIFIED` without **fresh successful coverage + successful admission/application**
  (i.e. `Mutated=false` alone is **not** sufficient; success is coverage, not mutation — §3.5/§5
  contract).
- Any state `→ (deleted)` that would lose a pending/late signal (§4 lost-wakeup rule).
- Any transition that writes Canonical Inventory or removal evidence directly.
- `PENDING → IN_FLIGHT` while `not_before > now`.
- Budget defer must **not** produce `RETRY_WAIT`/failure transitions; the item stays `PENDING`
  (optionally with `not_before` set) and counters do not increment.
- **No merge may be ignored**: every `work_state` has a defined merge behavior (§3.1); a merge must
  never be dropped merely because the item is `IN_FLIGHT`/`RETRY_WAIT`/`BLOCKED`/`SUSPENDED`.
- A merge into `BLOCKED`/`SUSPENDED`/`RETRY_WAIT` must **not** auto-clear that state (no automatic
  un-block, no automatic un-suspend, no cancelling of backoff).
- **Every `IN_FLIGHT → non-IN_FLIGHT` transition must set `claimed_signal_seq = NULL`** (work invariant
  2, contract §3.8) — including crash recovery.
- A CAS conflict on completion must **re-read** and only record success if the claim is still valid
  (contract §4.3); no tight retry loop.

### 3.3 Recovery transitions (single-writer startup)

| Observed at startup | Action |
| --- | --- |
| `IN_FLIGHT` (no completion) | CAS to `PENDING` (or `RETRY_WAIT` per policy); clear `claimed_signal_seq`; preserve `signal_seq`. |
| `PENDING` with `not_before <= now` | Immediately eligible. |
| `PENDING` with `not_before > now` | Remains `RETRY_WAIT`-equivalent eligibility; no forced attempt. |
| `SUSPENDED` | Only resumed when root lifecycle allows. |
| `BLOCKED` | Reported; requires operator/repair; not auto-retried. |

## 4. Cross-machine rules

1. **Watch changes never drop work.** Any watch transition leaves all `DirtyScopeWork` rows intact.
2. **Work success may inform watch policy** (e.g. observed change → promote `WARM→HOT`, sustained
   no-change → demote), but promotion/demotion is a separate CAS-guarded watch transition — never a
   side effect that mutates watch state without going through §2.1.
3. **One actuator.** Both machines only ever converge on the accepted P0 `scan.Service.ScanScope`
   path; no other Canonical-write lane exists.
4. **Frozen ordering.** Executor claims are serialized under the existing same-root
   admission/application discipline; the state machines do not introduce a new ordering system.