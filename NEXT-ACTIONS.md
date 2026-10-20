# Index Core — Next Actions

## Current P0 Reliability execution (2026-10-20)

Issue120 tracks a new production-reliability program, NOT the earlier accepted
P0 Scoped Refresh prototype. R1 has bounded Architect acceptance at Issue120
comment6281974853. R2 is BOUNDED ARCHITECT_ACCEPTED: PR122 squash54b15e988aee,
main CI39366941874 SUCCESS; Issue121 closed, receipt Issue120 comment6283052837.
R3 is BOUNDED ARCHITECT_ACCEPTED: reviewed HEAD da8af1e51b22, PR124 merge
f5df97e3f6ec5d54389da39debf0c5937a3d3d9b, main CI39389632052 SUCCESS;
Issue123 closed, receipt Issue120 comment6286259503.
Current packet is Issue125 / R4: isolated post-fix nested capacity, actual owned
subprocess SIGKILL and dedicated PG18 stop/restart. One branch / one unmerged PR
for Architect review. Compile/test on192.168.178.50 only; no public runtime,
provider credentials/writes, migrations, repair semantics or product changes.
See docs/p0/P0-R4-CAPACITY-FAULT-VERIFICATION.md for PASS/FAIL/BLOCKED/NOT_RUN,
resource limits, guard and reproducible commands. Review actual measured evidence
including the50k matrix time-budget BLOCKED/PARTIAL (do not infer PASS from cold
ingestion or normal CI).2k/20k and actual process/DB crash probes passed.
Review5583387609/comment6288120356 now authorize one follow-up50k attempt:
fresh attested lab,2k/20k prerequisites, capacity50-only120min hard maximum
(<=5min cleanup), unchanged resources/isolation/assertions and other budgets.
Fresh2k/20k passed; the user stopped the50k follow-up at877.184s to prioritize
reviewed-main deployment. Follow-up status USER_INTERRUPTED_NOT_PASS/raw exit143;
remaining50k scenarios NOT_RUN. Owned test runner/DB stopped, artifacts retained.
Record new exact-head CI on the same Draft PR126; no further capacity retry,
merge, self-acceptance or R5 is inferred. Require exact-head CI before any
merge; merged-main CI and explicit acceptance are
separate gates. Do not self-merge or close Issue125/120. R5 is unstarted and
whole-P0 reliability is NOT accepted. Prior records below remain history, not
this program's current status.

## Historical incremental foundation

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex Executor / Worker only for the current Architect-authorized Issue**

Current main baseline before this planning branch:

`eddf95d232b261bac24f019621d1e3285ffb3c8b`

Current incremental state:

```text
D0 change-discovery research             ARCHITECT_ACCEPTED
P0 scoped refresh                        ARCHITECT_ACCEPTED
P1 hot-scope polling feasibility         ARCHITECT_ACCEPTED
P2 durable scope-state design            ARCHITECT_ACCEPTED
P3 state persistence                     ARCHITECT_ACCEPTED
P4 one-shot dirty executor               ARCHITECT_ACCEPTED
P5 bounded executor loop                 ARCHITECT_ACCEPTED
P6 scheduler orchestration               ARCHITECT_ACCEPTED
P7 manual incremental command            ARCHITECT_ACCEPTED
P8 mutation hint ingestion               ARCHITECT_ACCEPTED
P9 trusted hint transport                ARCHITECT_ACCEPTED
P10 hybrid runtime                       ARCHITECT_ACCEPTED
P11 production hybrid runtime hardening  ARCHITECT_ACCEPTED
P12 shortened deployment soak + Reference Test Web  ARCHITECT_ACCEPTED
```

P10 implementation PR #96 merged at `f655f04`; closeout PR #97 merged at
`eddf95d`. P10 exit decision is:

`AUTHORIZE_PRODUCTION_HYBRID_RUNTIME_HARDENING`.

The governing P11 plan is:

`docs/architecture/INCREMENTAL-P11-PRODUCTION-HYBRID-RUNTIME-HARDENING.md`

## Current bounded objective

P11 hardening is complete and ARCHITECT_ACCEPTED.

The current bounded validation step is **complete**: P12 shortened deployment soak with the Reference Test Web.

Canonical plan:

`docs/architecture/INCREMENTAL-P12-DEPLOYMENT-SOAK.md`

Reference consumer baseline:

`nathanxiangang-web/indexcore-reference-web` (**Reference Test Web / validation-only**) 

The incremental runtime remains **default disabled**.

## Still not authorized

Do not begin:

- incremental runtime default-on;
- native delta/provider cursor;
- network-reachable/public Mutation Hint transport;
- second writer / multi-daemon HA;
- parallel P6 cycles;
- automatic INTERNAL retry;
- automatic BLOCKED/SUSPENDED repair;
- destructive delta/removal;
- migration/schema expansion;
- direct 115 integration;
- formal successor product / Gate 5;
- auth/user/admin/search/catalog/preview/download/AI product work.

`FROZEN_CONTRACT_CHANGES: NONE`

## Execution protocol

P11 implementation PR #100 is Architect-accepted and merged as `ec980a5`; Issue #99 is complete.

P11 exit decision: `AUTHORIZE_DEPLOYMENT_SOAK`.

P12 shortened cross-repository validation is complete.

Accepted implementation merges:

```text
IndexCore Phase B         94eab92
Reference Test Web        169535d
```

The original long-duration acceptance profile remains available as
`P12_MODE=acceptance` but was **not run** and is **not claimed**.

After plan merge, the Architect opens one bounded execution Issue. Worker must:

1. branch from current `main`;
2. implement only the Issue/plan scope;
3. add required tests and result report;
4. submit one PR;
5. **do not merge**;
6. wait for Architect review.

## Current project boundary

Gate 1–4 remain CLOSED / ARCHITECT_ACCEPTED.

Gate 5 remains **NOT AUTHORIZED**.

CloudSite 1.0 remains Legacy / Frozen and is not the active IndexCore validation
target.

The public IndexCore Query contract remains read-only Q1–Q9.

## Recovery rule

A future Architect session should verify remote `main`, then read:

1. `PROJECT-CONTEXT.md`;
2. `PROJECT-STATE.md`;
3. `ARCHITECTURE-INVARIANTS.md`;
4. this file;
5. the current authorized Issue/PR;
6. the P12 result/closeout while no next feature phase is authorized.

Chat history is cache; Git is project memory.


## No automatic next phase

The shortened P12 test validation does not automatically authorize:

- incremental runtime default-on;
- native delta/provider cursor;
- network/public Hint;
- second writer / HA;
- destructive scoped removal;
- migration/schema expansion;
- direct 115;
- Gate 5.

A new explicit Architect/user decision is required before any such work.
