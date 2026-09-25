# Index Core — Next Actions

## Current state

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
```

P10 implementation PR #96 merged at `f655f04`; closeout PR #97 merged at
`eddf95d`. P10 exit decision is:

`AUTHORIZE_PRODUCTION_HYBRID_RUNTIME_HARDENING`.

The governing P11 plan is:

`docs/architecture/INCREMENTAL-P11-PRODUCTION-HYBRID-RUNTIME-HARDENING.md`

## Current bounded objective

P11 hardening is complete and ARCHITECT_ACCEPTED.

The next bounded step is deployment soak planning/validation of the current accepted runtime.

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

No deployment-soak implementation has started yet. The Architect must define its bounded scope before any new execution work begins.

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
6. the P11 result/closeout while deployment soak is the next authorized step.

Chat history is cache; Git is project memory.
