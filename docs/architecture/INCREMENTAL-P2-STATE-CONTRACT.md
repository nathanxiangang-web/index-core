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
| `last_success_at` | instant, nullable | Most recent successful attempt **driven by this watch's own `POLL_SCHEDULE` trigger** — i.e. `POLL_SCHEDULE ∈ DirtyScopeWork.claimed_source_set` (the claim that attempt acquired), **not** this watch's own policy `source_set`. A success driven only by a hint/manual trigger does **not** update it (§6.1). |
| `next_due_at` | instant, nullable | Next instant the watch is due; `NULL` iff not scheduled. |
| `consecutive_failures` | int | Consecutive **provider** failures attributable to the watch schedule; budget defer does not increment (§6). |
| `last_error_class` | enum, nullable | Provider-neutral failure class of the most recent failed attempt (§6). |
| `deferred_until` | instant, nullable | If set, the watch must not be selected before this instant (scheduling pressure). |
| `created_at` / `updated_at` | instant | Bookkeeping. |
| `version` | int | Optimistic-concurrency / CAS token (§2.8). |

### 2.2 `watch_state`

- `HOT` / `WARM`: **scheduling classes**. Only these are eligible for periodic polling and produce
  `POLL_SCHEDULE` triggers.
- `COLD`: **retained state only — never periodically polled.** A `COLD` watch produces **no**
  `POLL_SCHEDULE` trigger, even if a stale `next_due_at` exists; it keeps its state (and any pending
  `DirtyScopeWork`) so it can be promoted later. `effective_interval IS NULL` for `COLD`.
- `DISABLED`: no scheduling at all. Retained for audit; selection ignores it entirely.
- `COLD`/`DISABLED` are **not** deletion. Watch demotion (HOT→WARM→COLD) must **never** discard
  pending `DirtyScopeWork` (§15 of the plan; §8 here).

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
- A watch is **due** iff `watch_state ∈ {HOT,WARM}` **and** `next_due_at <= now` **and**
  (`deferred_until IS NULL OR deferred_until <= now`). `COLD`/`DISABLED` are never due, regardless of
  `next_due_at`.
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
- After restart a watch is due **only if** it is `HOT`/`WARM`, `next_due_at <= now`, **and**
  (`deferred_until IS NULL OR deferred_until <= now`); it then produces a `POLL_SCHEDULE` trigger on
  the first scheduling pass. `COLD`/`DISABLED` are **never** due by a stale `next_due_at`
  (§2.2/§2.6/§2.10).
- Restart must lose **neither** due watches **nor** pending dirty work (§7).
- A stale `last_attempt_started_at` without a matching finished value does not suppress due-ness.

### 2.10 Watch invariants

1. `watch_state = DISABLED` ⇒ `next_due_at IS NULL`.
2. `watch_state ∈ {COLD,DISABLED}` ⇒ **no** schedule trigger and no periodic poll, even if
   `next_due_at` is set historically; selection uses `watch_state`, not merely the timestamp. Only
   `HOT`/`WARM` are periodically scheduled.
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
| `claimed_signal_seq` | bigint, nullable | `signal_seq` captured when an attempt claimed the item (non-null **iff** `IN_FLIGHT`). |
| `claimed_source_set` | set<source>, nullable | Sources of the signals **this attempt actually claimed** (snapshot of `pending_source_set` at claim). Non-null iff `IN_FLIGHT`. **Watch health counters use this** (§6.1). |
| `claimed_reason_set` | set<reason>, nullable | Reasons of the claimed signals (snapshot at claim). Non-null iff `IN_FLIGHT`. |
| `claimed_priority` | enum, nullable | Priority of the claimed signals (snapshot at claim). |
| `claimed_first_seen_at` | instant, nullable | First-seen time snapshotted into the claim; preserves the age of the claimed work across failure re-coalesce (fairness). Non-null iff `IN_FLIGHT`. |
| `pending_source_set` | set<source> | Sources of signals **not yet claimed by any attempt**. Empty means "no outstanding un-claimed signal". |
| `pending_reason_set` | set<reason> | Reasons of the un-claimed signals. |
| `pending_priority` | enum, nullable | Highest priority among the un-claimed signals. |
| `pending_first_seen_at` | instant, nullable | First un-claimed signal time (used for aging/fairness). |
| `pending_not_before` | instant, nullable | Earliest eligibility for the un-claimed signals. |
| `last_seen_at` | instant | Latest signal/merge time of the **current outstanding epoch**; reset to `now` when a new epoch opens after `VERIFIED` (bookkeeping). |
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
- It is **durable and strictly monotonic for the lifetime of the row**: it is **never reset** — not by
  completion, not across epochs, and not by `VERIFIED`. In v1 the row is never compacted/deleted
  (§G schema decision), so `signal_seq` is a single ever-increasing counter per `(root_id, scope_key)`.
- `claimed_signal_seq` is set atomically at claim time and is the basis of the completion CAS (§4).
- **Claim-scoped, not epoch-scoped.** An attempt is responsible only for the signals it **claimed**;
  signals that arrive after the claim live in `pending_*` and are attributed to the **next** attempt
  (§3.4). `claimed_*` fields are non-null **iff** `work_state = IN_FLIGHT` (work invariant 2, §3.8).

### 3.4 Claim-scoped provenance: `claimed_*` vs `pending_*`

Provenance is split into two independent groups so that **an attempt's attribution can never be
polluted by signals that arrived after it started**:

- **`pending_*`** — signals **not yet claimed by any attempt**. Merge fan-in writes **only** here.
- **`claimed_*`** — a **snapshot of `pending_*` taken at claim time**, describing exactly the signals
  this attempt is responsible for. It is **immutable for the duration of the attempt**.

Transitions:

- **claim**: `claimed_* := pending_*`, then `pending_*` is **cleared** (empty).
- **merge during `IN_FLIGHT`**: `signal_seq++` and the new provenance goes to **`pending_*` only**;
  `claimed_*` is untouched.
- **success, no newer signal**: item → `VERIFIED`; `claimed_*` released; `pending_*` is empty.
- **success, newer signal present**: item → `PENDING`; `claimed_*` is **released and discarded**;
  `pending_*` **keeps only the post-claim signals** (it never inherits `claimed_*`).
- **failure/abort**: the **un-satisfied `claimed_*`** is **re-coalesced back into `pending_*`** (union)
  together with any post-claim `pending_*`, then the item enters `RETRY_WAIT`/`BLOCKED`/`SUSPENDED`.

Epoch semantics: when a new signal arrives after `VERIFIED`, it opens a **new epoch** and **rebuilds**
`pending_*` from the new trigger (no inheritance from the previous epoch). `signal_seq` still does not
reset (§3.3).

Invariant: **the outstanding provenance must describe exactly the still-outstanding signals** —
`pending_*` = not-yet-claimed, `claimed_*` = currently being attempted. A pure `POLL_SCHEDULE` attempt
must never carry `MUTATION_HINT` provenance, and vice versa.

### 3.5 Attempt/verification fields

- `last_verified_at` / `last_verified_signal_seq` are written on successful coverage that satisfies the
  claimed signal (§4) — **including a partial success**: if the claimed signal `k` is verified but a
  newer signal arrived during the attempt, the item returns to `PENDING` **and still records
  `last_verified_at = now` / `last_verified_signal_seq = k`** (the claimed signal really was verified).
- `last_attempt_finished_at` is written for both success and failure.
- Success is defined by **fresh successful scope coverage + successful admission/application**, not
  by `Mutated=true` (§5 of the plan; §5 here).

### 3.6 `pending_not_before`, `attempt_count`, `consecutive_failures`

- `pending_not_before` is scheduling eligibility (backoff/defer) for the **un-claimed** signals; it is
  independent of `pending_priority`.
- A merge updates `pending_not_before` **per state** (§4.2): only `PENDING` may become eligible sooner;
  `RETRY_WAIT`/`BLOCKED`/`SUSPENDED` keep it (a new signal must **not** cancel backoff); a new epoch
  (`VERIFIED`) rebuilds it from the trigger.
- A failure sets `pending_not_before = max(pending_not_before, now + backoff(class))` when entering
  `RETRY_WAIT` (never earlier than the backoff).
- `consecutive_failures` increments only on provider-class failures; budget defer does not touch it.
- `attempt_count` is diagnostic and never used to gate correctness.

### 3.7 `version` / CAS semantics

- All mutations are CAS on `version` (same rule as §2.8).
- Claim, merge (`signal_seq++`), completion, and failure transitions each perform a single CAS. A
  merge that races a completion must be serialized by CAS so that the merge is never lost (§4).
- The item is owned by the single writer process; `IN_FLIGHT` is **not** a distributed lease.

### 3.8 Work invariants

1. At most one active logical item per `(root_id, scope_key)`.
2. `claimed_signal_seq`, `claimed_source_set`, `claimed_reason_set`, `claimed_priority` and
   `claimed_first_seen_at` are all `NULL` iff `work_state != IN_FLIGHT`; all five are set together at
   claim and released together on every exit from `IN_FLIGHT`.
3. `claimed_signal_seq <= signal_seq` always.
4. `last_verified_signal_seq` is always a previously-claimed `signal_seq` value and is `<= signal_seq`
   at write time. (There is no cross-scheme "signature" quantity to compare against.)
5. `signal_seq` never decreases for the lifetime of a row. In v1 rows are never compacted or deleted
   (`SCHEMA-SKETCH.md`), so it is strictly monotonic for all time.
6. **Claim isolation**: a merge never modifies `claimed_*`. A pure `POLL_SCHEDULE` attempt must never
   carry a hint/manual source that arrived after its claim was taken.
7. Work state transitions never mutate Canonical Inventory or removal evidence (§8).

## 4. Lost-wakeup / coalescing proof (§D)

**Invariant C1** — there is at most one active logical `DirtyScopeWork` per `(root_id, scope_key)`.

**Invariant C2** — every new trigger **merges** into that item and **increments durable
`signal_seq`** in the same CAS-guarded transition.

**Invariant C3 (lost-wakeup safety)** — a successful attempt may transition to `VERIFIED` **only if
`current signal_seq == claimed_signal_seq`**. Otherwise a newer signal arrived during the attempt and
the item must remain/re-enter `PENDING` (never silently dropped).

### 4.1 Required CAS rule

```text
# merge (any trigger, arbitrary work_state):   # provenance -> pending_* ONLY
CAS(version=v -> v+1):
    signal_seq   = signal_seq + 1
    last_seen_at = now
    if work_state == VERIFIED:            # NEW EPOCH -> rebuild the pending epoch
        claimed_signal_seq = NULL         # ensure any claim is released
        claimed_source_set = NULL
        claimed_reason_set = NULL
        claimed_priority   = NULL
        pending_source_set    = new_sources      # RESET (no inheritance)
        pending_reason_set    = new_reasons
        pending_priority      = trigger.priority_class
        pending_first_seen_at = now
        pending_not_before    = trigger.not_before
        work_state            = PENDING
    else:                                  # SAME epoch -> fan-in to pending_* (NEVER to claimed_*)
        pending_source_set = pending_source_set ∪ new_sources
        pending_reason_set = pending_reason_set ∪ new_reasons
        pending_priority   = max(pending_priority, trigger.priority_class)
        if pending_first_seen_at IS NULL: pending_first_seen_at = now
        # pending_not_before is handled PER STATE (NOT a blanket min()):
        # PENDING may become eligible sooner (min); every other state may only move LATER (max, never
        # earlier), so a post-claim signal's own eligibility is preserved and an existing backoff is
        # never cancelled.
        if work_state == PENDING:     pending_not_before = min(pending_not_before, trigger.not_before)
        if work_state == IN_FLIGHT:   pending_not_before = max(pending_not_before, trigger.not_before)  # preserve post-claim eligibility
        if work_state == RETRY_WAIT:  pending_not_before = max(pending_not_before, trigger.not_before)  # must NOT cancel backoff
        if work_state == BLOCKED:     pending_not_before = max(pending_not_before, trigger.not_before)
        if work_state == SUSPENDED:   pending_not_before = max(pending_not_before, trigger.not_before)

# claim (snapshot pending_* -> claimed_*, then CLEAR pending_*):
CAS(version=v -> v+1):  where work_state = PENDING and (pending_not_before IS NULL or pending_not_before <= now)
    work_state              = IN_FLIGHT
    claimed_signal_seq      = signal_seq
    claimed_source_set      = pending_source_set      # snapshot of what this attempt owns
    claimed_reason_set      = pending_reason_set
    claimed_priority        = pending_priority
    claimed_first_seen_at   = pending_first_seen_at   # snapshot age for fairness across failure re-coalesce
    pending_source_set      = ∅                        # cleared: nothing un-claimed yet
    pending_reason_set      = ∅
    pending_priority        = NULL
    pending_first_seen_at   = NULL
    pending_not_before      = NULL
    last_attempt_started_at = now
    attempt_count           = attempt_count + 1

# successful completion (CAS with re-read on conflict; see 4.3):
CAS(version=v -> v+1):  where work_state = IN_FLIGHT and claimed_signal_seq = k
    # Watch counters are updated from claimed_source_set HERE, BEFORE release (§6.1)
    if signal_seq == k:                   # no newer signal during the attempt
        work_state               = VERIFIED
        last_verified_at         = now
        last_verified_signal_seq = k
        consecutive_failures     = 0
    else:                                 # newer signal(s) arrived -> keep ONLY post-claim pending
        work_state               = PENDING
        # pending_* already holds ONLY the post-claim signals (never inherits claimed_*)
        # PARTIAL-SUCCESS watermark: the CLAIMED signal k WAS successfully verified, so record it
        # even though the item re-opens for the newer signal(s).
        last_verified_at         = now
        last_verified_signal_seq = k
        consecutive_failures     = 0
    # release the whole claim in BOTH branches:
    claimed_signal_seq    = NULL
    claimed_source_set    = NULL
    claimed_reason_set    = NULL
    claimed_priority      = NULL
    claimed_first_seen_at = NULL

# failure / abort (leaving IN_FLIGHT for ANY non-IN_FLIGHT state):
CAS(version=v -> v+1):  where work_state = IN_FLIGHT and claimed_signal_seq = k
    # Watch counters are updated from claimed_source_set HERE, BEFORE release (§6.1)
    # re-coalesce the un-satisfied claimed signals + any post-claim pending signals:
    pending_source_set    = claimed_source_set ∪ pending_source_set
    pending_reason_set    = claimed_reason_set ∪ pending_reason_set
    pending_priority      = max(claimed_priority, pending_priority)
    pending_first_seen_at = min(pending_first_seen_at, claimed_first_seen_at, last_attempt_started_at)  # preserve the OLDEST age (NULLs ignored)
    pending_not_before    = max(pending_not_before, now + backoff(class))   # RETRY_WAIT only; never earlier
    work_state               = <RETRY_WAIT | BLOCKED | SUSPENDED | PENDING>
    claimed_signal_seq    = NULL
    claimed_source_set    = NULL
    claimed_reason_set    = NULL
    claimed_priority      = NULL
    claimed_first_seen_at = NULL
    last_attempt_finished_at = now
    consecutive_failures     = consecutive_failures + 1   # provider classes only; NEVER budget defer
    last_error_class         = <class>                    # provider classes only
```

The completion branch is the exact rule that prevents clearing signal `8` after attempt `7`.
**Every `IN_FLIGHT -> non-IN_FLIGHT` transition (success, failure, block, suspend, crash recovery)
MUST release the whole `claimed_*` group to `NULL`** — work invariant 2 (§3.8).

### 4.2 Merge while in each `work_state`

A trigger may arrive in **any** work state; the merge CAS above always increments `signal_seq`, and one
transition is defined per state (no state silently ignores a merge). **In every case the new provenance
goes to `pending_*` only — a merge never modifies `claimed_*`.**

| current `work_state` | merge effect |
| --- | --- |
| `PENDING` | `signal_seq++`; `pending_*` union; `pending_not_before = min(...)` (may become eligible sooner). Stays `PENDING`. |
| `IN_FLIGHT` | `signal_seq++`; `pending_*` union; **`claimed_*` untouched**; **`pending_not_before = max(pending_not_before, trigger.not_before)`** (a post-claim signal's own eligibility is preserved — it never becomes eligible earlier than it asked). On completion the `signal_seq != claimed` branch returns the item to `PENDING` keeping **only** the post-claim `pending_*`, and records the **partial-success watermark**. |
| `VERIFIED` | **opens a NEW EPOCH**: `signal_seq++`; `pending_*` **rebuilt** from the new trigger (no inheritance); `work_state = PENDING`. |
| `RETRY_WAIT` | `signal_seq++`; `pending_*` union; **`pending_not_before = max(pending_not_before, trigger.not_before)`** — a new signal must **not** cancel or shorten the existing bounded backoff. Stays `RETRY_WAIT`. |
| `BLOCKED` | `signal_seq++`; `pending_*` union; `pending_not_before = max(pending_not_before, trigger.not_before)`. Stays `BLOCKED` — a new signal does **not** auto-clear a block; only operator/repair (or `INVALID_SCOPE` policy) returns it to `PENDING`. |
| `SUSPENDED` | `signal_seq++`; `pending_*` union; `pending_not_before = max(pending_not_before, trigger.not_before)`. Stays `SUSPENDED` — no attempt runs while the root is non-ACTIVE; resumes to `PENDING` when the root becomes ACTIVE. |

Notes:

- **`pending_priority`** is always the **maximum** of the currently **un-claimed** signals' priority;
  a new epoch sets it to the new trigger's priority.
- **`pending_first_seen_at`** is the first time of the **current un-claimed set**; a new epoch resets it
  to `now`. On failure re-coalesce it becomes the **oldest** of the post-claim pending and the claimed
  snapshot, so a long-waiting item is **not** reset to "just arrived" (fairness).
- **`pending_not_before`** may only move **later** (`max`) in every non-`PENDING` state, and may move
  earlier only in `PENDING` (`min`). This preserves a post-claim signal's eligibility and never cancels
  an existing backoff.
- **`claimed_*`** is a frozen snapshot for the attempt: it is **only** set at claim (including
  `claimed_first_seen_at`) and released on exit from `IN_FLIGHT`. A failure re-coalesces
  `claimed_* ∪ pending_*` back into `pending_*`.
- **Partial success**: if the claimed signal(s) were verified but newer signals are still pending, the
  item returns to `PENDING` **and** records `last_verified_at = now` /
  `last_verified_signal_seq = k` (the claimed signal really was verified).

### 4.3 CAS-conflict completion rule (explicit)

A merge during `IN_FLIGHT` bumps the row `version`, so attempt `k`'s completion CAS can fail **even
though `signal_seq` is still `k`**. The required behavior on any CAS conflict is:

1. **re-read** the row;
2. if `work_state == IN_FLIGHT` **and** `claimed_signal_seq == k` → the claim is still valid; retry the
   CAS (bounded, **no tight loop**);
3. otherwise the claim is no longer valid → **stop and do NOT write success**; the newer merge/state
   owns the item and it will be re-claimed later.

Equivalently: success is recorded **only** by a CAS that succeeds with
`signal_seq == claimed_signal_seq == k`.

**Failure/abort uses the same re-read discipline.** A failure/abort CAS (`IN_FLIGHT -> RETRY_WAIT /
BLOCKED / SUSPENDED / PENDING`) may also fail because a merge bumped `version`. On conflict:

1. **re-read** the row;
2. if `work_state == IN_FLIGHT` **and** `claimed_signal_seq == k` → the claim is still valid; retry the
   CAS (bounded, **no tight loop**);
3. otherwise the claim is no longer valid → **stop and do NOT write the failure/abort transition**; the
   newer merge/state owns the item.

A failure/abort transition must never overwrite a state a newer merge already advanced, and must never
re-apply `consecutive_failures` / `last_error_class` to a stale claim.

### 4.4 Epoch boundary

An epoch is the outstanding span between successful clears. `VERIFIED` closes the current epoch; the
next merge opens a new one and **rebuilds `pending_*`** (§3.4). `signal_seq` **does not**
reset across epochs (§3.3) — it remains the durable lost-wakeup guard.

### 4.5 Worked example (the required race)

```text
signal_seq = 7
attempt claims 7            -> claimed_signal_seq = 7, work_state = IN_FLIGHT
new trigger arrives         -> signal_seq = 8   (merge CAS; version bump)
attempt 7 succeeds          -> CAS sees signal_seq(8) != claimed(7)
                            -> work_state = PENDING
                            -> claimed_* released & DISCARDED (attempt 7's provenance)
                            -> pending_* keeps ONLY the post-claim signal 8 (MUTATION_HINT)
                            -> signal 8 remains pending and WILL be processed (as MUTATION_HINT)
```

Signal 8 is **never** cleared by attempt 7.

### 4.6 Prohibited shortcut

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
`DRIFT_VERIFY`, `RETRY`. Reasons follow the **claim/pending split** (§3.4): merges union into
`pending_reason_set`; a claim snapshots it into `claimed_reason_set`; a new epoch rebuilds
`pending_reason_set` from the new trigger. Reasons are never retained across an epoch, and a newer
reason never overwrites an existing **same-group** member.

### 5.3 Mutation Hint boundary

- A hint creates **immediate dirty work** for the exact scope; it **must not** automatically create or
  promote a permanent `HOT` watch policy.
- A hint must never directly produce a Canonical mutation or removal evidence; it only produces dirty
  work that runs the accepted P0 path.
- Hint ingress is **not implemented** in P2 (no endpoint/API).

### 5.4 Merge semantics

- Multiple triggers merge into the outstanding item; `signal_seq++`, `last_seen_at = now`, and the
  new provenance is added to **`pending_*` only** (never `claimed_*`), with `pending_not_before`
  handled **per state** (only `PENDING` may become eligible sooner; `RETRY_WAIT`/`BLOCKED`/`SUSPENDED`
  keep it unchanged) (§4.1/§4.2).
- **Cross-epoch**: a trigger arriving after `VERIFIED` opens a new epoch and **rebuilds** `pending_*`
  (`pending_source_set`/`pending_reason_set`/`pending_priority`/`pending_first_seen_at`/
  `pending_not_before`) from the new trigger (§3.4/§4.2/§4.4); `signal_seq` continues monotonically
  (never reset).
- In v1 the row is never compacted/deleted (`SCHEMA-SKETCH.md`), so the item always exists; a **fresh**
  item with `signal_seq = 1` is created only when the row is first inserted for a new
  `(root_id, scope_key)`.

### 5.5 Transaction boundary for poll-due triggering (v1 decision)

When a due `HOT`/`WARM` watch produces a `POLL_SCHEDULE` trigger, the due re-check **and** the schedule
advance **and** the `DirtyScopeWork` merge MUST be committed in **one Store transaction per scope**
(atomic). The transaction:

1. re-checks the watch is still due (`watch_state ∈ {HOT,WARM}`, `next_due_at <= now`,
   `deferred_until` not blocking) — CAS on the watch `version`;
2. advances the schedule (`last_due_at = now`, `next_due_at = now + effective_interval`) — same CAS;
3. merges the `DirtyScopeWork` for that scope (`signal_seq++`, or row insert) — CAS on the work `version`.

If the transaction does not commit, **neither** the schedule advance **nor** the work merge is visible,
so a crash cannot duplicate a poll signal and cannot silently skip a due watch. One poll cycle
processes **each due scope in its own transaction** (not one giant per-cycle transaction).

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
| **budget defer** | remain `PENDING` (`pending_not_before` may be set) | Yes (next cycle) | **No** | No | No |
| crash while `IN_FLIGHT` | recovered to `PENDING`/`RETRY_WAIT` (§7) | Yes | No (not a provider failure) | No | No |

Notes:

- "Never silently raise `max_entries`": `SCOPE_TOO_LARGE` must not be worked around by widening the
  request.
- Budget defer records scheduling pressure (observability `budget_defer_count`); it is **not** a
  provider failure and must not advance failure counters or error class.
- A failed attempt never mutates Canonical Inventory or removal evidence.

### 6.1 Counter ownership when one attempt serves merged signals

A single `ScanScope` attempt serves exactly the signals it **claimed**. Watch health counters are
attributed using the **claimed** provenance, never the whole outstanding epoch:

> **Watch attribution condition: `POLL_SCHEDULE ∈ DirtyScopeWork.claimed_source_set`.**

This is deliberately **not** the watch's own `ScopeWatchState.source_set` (policy provenance such as
`OPERATOR_POLICY`/`ADAPTIVE_POLICY`, which can never contain `POLL_SCHEDULE`) and **not** the current
`pending_source_set` (signals that arrived **after** the claim and are therefore not this attempt's
responsibility).

| Counter / field | On provider failure | On success | On budget defer |
| --- | --- | --- | --- |
| Work `consecutive_failures` / `last_error_class` / `last_attempt_finished_at` | **Yes** (provider classes) | reset `consecutive_failures = 0`; set `last_verified_at` / `last_verified_signal_seq` | **No** |
| Work `attempt_count` | Yes (already incremented at claim) | Yes | **No** (no claim) |
| Watch `consecutive_failures` / `last_error_class` | **Only if `POLL_SCHEDULE ∈ claimed_source_set`** | reset `consecutive_failures = 0`; set `last_success_at` **only if `POLL_SCHEDULE ∈ claimed_source_set`** | **No** |
| Watch `last_attempt_started_at` / `last_attempt_finished_at` | Same rule: **only if `POLL_SCHEDULE ∈ claimed_source_set`** | same | **No** |

Rules:

- Attribution uses `claimed_source_set` **as evaluated at the attempt's start**, before the claim is
  released.
- A failure caused **only** by a signal that arrived **after** the claim (i.e. still in `pending_*`,
  with `POLL_SCHEDULE ∉ claimed_source_set`) updates the **Work** counters but leaves the **Watch**
  counters/attempt fields unchanged — that watch poll did not fail, because the failure happened
  before that `POLL_SCHEDULE` signal existed.
- A failure caused **only** by `MUTATION_HINT`/`MANUAL_OPERATOR`/`PROVIDER_EVENT`
  (`POLL_SCHEDULE ∉ claimed_source_set`) likewise leaves the Watch counters unchanged.
- A **budget defer** is not an attempt: it updates **neither** Work nor Watch failure counters nor
  error class (it may increment an observability-only `budget_defer_count`).

## 7. Crash / restart matrix (§H)

Single active writer per DB. P2 does **not** authorize distributed leases, fencing tokens, or HA.

| Crash point | On restart |
| --- | --- |
| Before claim | Item remains `PENDING`; `pending_*`/`signal_seq` unchanged. |
| After claim (IN_FLIGHT, no completion persisted) | Stale `IN_FLIGHT` recovered to `PENDING` (or `RETRY_WAIT` per policy); the whole `claimed_*` group is **re-coalesced into `pending_*`** (`pending_* := claimed_* ∪ pending_*`) and released; `signal_seq` preserved. |
| After provider success but before work completion | No durable success recorded; item re-runs P0 safely (idempotent, IO3-protected); `signal_seq` still prevents losing a newer trigger. |
| After Canonical apply but before `VERIFIED` persisted | Canonical is already correct; the P0 re-run reconciles to the same/newer state safely (IO3 NOOP or normal reconcile); work then completes via the normal CAS. |
| Restart with stale `IN_FLIGHT` | Recovered as above; no blind re-claim without CAS; no duplicate Canonical write lane. |
| Restart with overdue watches | For `HOT`/`WARM` only: recomputed from `next_due_at <= now`, due immediately (§2.9). `COLD`/`DISABLED` never become due from a stale `next_due_at`. |
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