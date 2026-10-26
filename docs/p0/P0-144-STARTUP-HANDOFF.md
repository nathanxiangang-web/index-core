# Issue #144: selected stale head consumption, not successful coverage

Base: reviewed/deployed `17b64ba1156291af043d51e6427bc21cd2bb8d6c`.
This change is a candidate hotfix, NOT production rollout or N2 acceptance.

## D0: live evidence and limits

Prospective observation: 2026-10-25 16:18:04.064454Z through 16:48:04.087143Z
(2026-10-26 00:18..00:48 Asia/Shanghai), 1800.022689s, 31 samples.
Actual native SHA256: `7f8ea469c45d440ef5ffb0a3b25c9c82cedf05f0820164b2c72ba049c8ddd7cb`.
Same PID 2225758, reader 1, all three pacing intervals 200ms. Restart counter 1
unchanged, fatal delta 0. This new window does not erase the first-start fatal.
Q4/Q5/Q8/Q9/readyz were sampled, existing Journal prefix and presence anchors
retained. No live queue, source, policy, process or schema was modified.

The [compact public D0 receipt](evidence/issue144-d0-readonly.json) retains all
31 timestamps, source binary, PID, pacing, queue ages, Journal/presence anchors,
writer count and read-only transport results. Timestamps preserve explicit
offsets even where a legacy producer names the field `utc`. The source bundle
SHA256 is recorded; raw scope identities and private credentials are excluded.

Read-only correlation found legacy admission 412523, observed at 14:13:17Z,
STALE_INPUT/REJECTED committed at 14:19:34.021425Z. It has no new fetch fence, and
retains original v2 coverage/generation 270. The first new committed Snapshot
appears only at 14:19:38.370481Z, after restart. Raw Root/scope and full tuple
remain private; public receipts use hashes. This identifies the old input, NOT
the exact failed new Work claim: historical logs have no claim fingerprint or
unwrapped sentinel, and current Work overwrites last-attempt information.
Therefore exact attribution of that single production fatal remains limited.

## Deterministic reproduction on unchanged main

The owned PG18/fake OpenList test uses genuine draft/admission APIs and an
immutable synthetic old observation, 6min old at creation. It is not a modified
production backup, not timestamp rebasing and not a fake Coordinator.

1. Legacy input remains SUBMITTED/PENDING with old IN_FLIGHT Work.
2. Actual new app composition, reader 1, writer ownership and startup recovery.
3. First real P6 wake reads source; a test-local pgx driver barrier pauses AFTER
   selecting the old FIFO head, BEFORE its Stage2 root lock.
4. A competing real Coordinator actor consumes that exact head as STALE_INPUT.
5. On unchanged `17b64ba1`, the original actor returns ErrNotHead; scanner drain
   wraps INTERNAL; P6 stops INTERNAL_ITEM_FAILURE; runtime is fatal.
6. With the fix, the old negative result remains negative, the same fresh claim
   receives a fresh successful observation, and no source fetch is retried inline.

The scanner-wins control passed before the fix; the worker-wins case failed
with the specific head sentinel. Original red log is retained. Three repeated
race-enabled green runs were recorded after the narrow fix. This proves a real
startup handoff defect consistent with the live evidence, not every hypothetical
executor failure or a retrospective recovery of missing production error text.

## Module contracts

`Coordinator.ProcessHead -> Store consumed-stale proof -> scanner/worker drain`.
`P6 result -> runtime finite diagnostic fields` is an independent read-only seam.

- Store owns exact(root,selected seq,Snapshot) validation under the Root lock.
  Require unique STALE_INPUT admission with NULL applied_generation, REJECTED
  Snapshot, exactly one STALE_INPUT result, NULL result generation, empty counts,
  no conflicts, exact superseded-input reason and no application/generation
  history. Inconsistent/missing/duplicate receipts and SQL errors fail closed.
- ProcessHead emits ErrStaleHeadConsumed ONLY after that proof. It is a progress
  signal, NOT an APPLIED/NOOP receipt. No original generation is invented:
  STALE_INPUT audit rows do not persist a generation to reconstruct.
- Scanner and admission worker can select the next head on this finite signal.
  The scanner still creates/validates its fresh input normally. Old stale input
  never verifies Work. ProcessSnapshot/AdmitOrResumeSnapshot refusal is retained.
- No Kernel identity, completeness/removal decision, schema, claim budget,
  ObservedAt, Work retry policy or provider quota is changed.
- Runtime diagnostic includes finite P6 stop reason, durable failure class,
  error family, optional five-character SQLSTATE and hashed correlation tokens.
  It never emits err.Error(), source paths, raw RootID, URLs, SQL or bodies.
  Actual fatal behavior remains fail-closed for INTERNAL/SYSTEMIC/unknown errors.

Extension constraint: never expand the progress signal to generic ErrNotHead,
FAILED/REJECTED or guessed terminal states. A new terminal class requires its
own exact evidence proof, negative matrix and independent architecture review.
Store proof stays replaceable/testable; consumers do not duplicate audit SQL.

## Separate tail findings; no performance-policy change here

Live deployed executable Defaults() has first-discovery=false and mirror-revisit=0.
Both environment variables are absent in the actual process; command is serve
without overriding flags. Hence F1/F2 code is compiled but these opt-ins are
not enabled. Do not describe them as production-effective merely because merged.

At 16:57Z all 232 PENDING items had prior claims (min 273), NORMAL priority; the
currently claimed epoch waited 850.66s since its pending-first-seen. One HOT
SOURCE_MIRROR root watch runs every 30s. In a separate existing 30min log window,
814 completions covered 474 unique scopes, 340 repeated completions; max repeats
for one scope 53. Whole-scope duration P50/P95/P99=1799/3263.10/3809.64ms includes
HTTP AND database work, not an isolated upstream latency measurement.

SourceRecovery/POSSIBLE_CHANGE publication without mirror-revisit policy matches
the existing unconditional child merge path. This supports a repeated-work/load
finding, not proof of starvation of a new directory that was never observed.
No natural APPLIED/new scope arrival was witnessed, and source starts/s or
structured upstream429/5xx are not available in existing telemetry.

The 31-sample queue is cyclic, not monotonically growing: PENDING 138..468,
P95 age oscillates as old cohorts drain and parents publish new work. Counts
alone and one tail endpoint cannot establish freshness improvement. Historical
first-ever-claim time is not reconstructable from current last-attempt fields.
Fixing F1/F2 deployment activation or F4 capacity policy belongs in a separate
bounded test/configuration or performance PR, not this correctness hotfix.

## Verification contract

- Reuse exact loopback owner-marked index138_test PG18 via Issue138Pool guard;
  never point these resettable fixtures at production or restored private data.
- Startup first wake: both actor orders, actual Claim/Recover/P6/HTTP; preserve
  old STALE_INPUT, unchanged Journal, current Work not verified by negative proof.
- Terminal evidence matrix: status/ID/result/history/counts/SQL-error negatives;
  STALLED is not an enum and must fail the database constraint, not be invented.
- Finite one-root 400 NORMAL scopes verifies once-only FIFO claims, exact Q5/Q8,
  and captures additional P95 claim delay. It is manual P6 with1ms fake-source
  pacing, NOT a P10/real-provider throughput proof or two-minute promise.
- Existing real OS crash/recovery, terminal handoff, changed/stable directory,
  cancellation and P0-R2 safety tests must be rerun, plus full suite/vet/build
  and exact-head GitHub CI. Results belong to the linked PR/Worker receipt.

Production remains `17b64ba1` / N1, N2 HOLD. No candidate deployment or old downgrade
is authorized by this document. Issues #132/#134/#138/#144 remain open for review.
