# Index Core — Next Actions

## Current state

**Gate 4 — Reference Consumer Integration — CLOSED**

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex Executor / Worker only for the Architect-authorized P6 scheduler-orchestration issue**

Status:

**P0 TARGETED SCOPED REFRESH — ARCHITECT_ACCEPTED / P1 HOT-SCOPE POLLING FEASIBILITY — ARCHITECT_ACCEPTED / P2 DIRTY-SCOPE STATE DESIGN — ARCHITECT_ACCEPTED / P3 STATE PERSISTENCE — ARCHITECT_ACCEPTED / P4 ONE-SHOT DIRTY EXECUTOR — ARCHITECT_ACCEPTED / P5 BOUNDED EXECUTOR LOOP — ARCHITECT_ACCEPTED / P6 SCHEDULER ORCHESTRATION — AUTHORIZED AFTER PLAN MERGE / PRODUCTION SCHEDULER NOT AUTHORIZED**

Current architecture planning:

- Issue #57 — incremental architecture umbrella (OPEN)
- Issue #59 — Change Discovery capability research (COMPLETE)
- Issue #62 — P0 Targeted Scoped Refresh Prototype (**COMPLETE / ARCHITECT_ACCEPTED**)
- PR #64 — P0 implementation + real 115 Open evidence (**MERGED** at `a4b6ec83e637c25f26585f56c3fb906410fa4b9a`)
- Blueprint: `docs/architecture/POST-MVP-INCREMENTAL-BLUEPRINT.md`
- P0 plan: `docs/architecture/INCREMENTAL-P0-SCOPED-REFRESH-PROTOTYPE.md`
- P1 plan: `docs/architecture/INCREMENTAL-P1-HOT-SCOPE-POLLING-PROTOTYPE.md`
- Issue #66 — P1 Adaptive Hot-Scope Polling Feasibility (**COMPLETE / ARCHITECT_ACCEPTED**)
- PR #67 — P1 implementation + real 115 Open cadence evidence (**MERGED** at `41c4a26de85532a1d43da073e4256b7befb39d36`)
- P2 plan: `docs/architecture/INCREMENTAL-P2-DIRTY-SCOPE-STATE-DESIGN.md`
- Issue #69 / PR #70 — P2 durable state design (**COMPLETE / ARCHITECT_ACCEPTED**, merged at `e1f9c734e30502a133e5e7df8f54b2d1e3e1338a`)
- P3 plan: `docs/architecture/INCREMENTAL-P3-STATE-PERSISTENCE-PROTOTYPE.md`
- Issue #72 / PR #73 — P3 state persistence (**COMPLETE / ARCHITECT_ACCEPTED**, merged at `c7c7091412a92c66e35d4d70f65a326ddc82a132`)
- P4 plan: `docs/architecture/INCREMENTAL-P4-ONE-SHOT-DIRTY-EXECUTOR-PROTOTYPE.md`
- Issue #75 / PR #76 — P4 one-shot dirty executor (**COMPLETE / ARCHITECT_ACCEPTED**, merged at `10668d6203ad86cad3eee074df19a1ee62dea7f7`)
- P5 plan: `docs/architecture/INCREMENTAL-P5-BOUNDED-EXECUTOR-LOOP-PROTOTYPE.md`
- Issue #79 / PR #80 — P5 bounded executor loop (**COMPLETE / ARCHITECT_ACCEPTED**, merged at `88e3e8ffa9c791f0ae6b04529cf82a65a6431d1e`)
- P6 plan: `docs/architecture/INCREMENTAL-P6-SCHEDULER-ORCHESTRATION-PROTOTYPE.md`
- Accepted D0 report: `docs/research/INCREMENTAL-CHANGE-DISCOVERY-REPORT.md`

Research and P0–P5 are complete. The next bounded step is **P6 Scheduler Orchestration Prototype**: in one manually-invoked finite cycle, materialize at most 5 persisted due watches through the accepted atomic `EmitDuePoll`, then invoke one accepted P5 bounded executor cycle, all under a total wall-time cap of 60 seconds. Production scheduling/cadence/continuous execution remains **NOT AUTHORIZED**.

Accepted Gate-4 merge commits:

- IndexCore verification fixture PR #53:
  `9bc98fb7759f16d5c6b772cf4442dea133e2012b`
- Reference Web PR #2:
  `8f7062216dc9924f64d9ae0367e504c279704857`
- IndexCore authoritative findings PR #52:
  `9d23b24f0ed715fce6128c031da99f6e111257ed`

Authoritative Gate-4 report:

`docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`

Final executable evidence:

`PASS=52 FAIL=0`

`INDEXCORE_FROZEN_CONTRACT_CHANGES: NONE`

## Current usage documentation

For running or integrating the accepted IndexCore Alpha, use:

- `README.md`
- `docs/README.md`
- `docs/QUICKSTART.md`
- `docs/CLI.md`
- `docs/HTTP-API.md`
- `docs/COLLECTORS.md`
- `docs/INTEGRATION.md`
- `docs/OPERATIONS.md`

Core development for the accepted Alpha scope is complete. Operational maintenance,
bug fixes, security hardening, and separately Architect-approved IndexCore changes
may continue without turning deferred product features into Kernel responsibilities.

## Gate 4 conclusion

The public read-only `/v1` Query Contract is sufficient for a clean new
server-side Consumer.

The accepted Reference Web proves:

- Q1–Q9 consumption without direct Store access;
- hierarchy and generation-bound pagination;
- explicit path ambiguity;
- active and removed views;
- per-root Journal semantics;
- stale-cursor handling;
- retained DEPRECATED/DELETED audit navigation;
- IndexCore unavailable/restart behavior;
- zero PostgreSQL / IndexCore-internal / provider / CloudSite coupling.

The Reference Web remains a disposable validation artifact. It is not the formal
successor product.

CloudSite 1.0 remains **Legacy / Frozen Product**.

## IndexCore Post-MVP Incremental planning

This is a separate IndexCore infrastructure extension and does **not** consume or authorize Gate 5.

Capability discovery and P0–P5 are complete. The current task is **P6 scheduler-orchestration prototype**.

Compare:

```text
Mutation Hint
Native Delta / Provider Cursor
Scoped Refresh through OpenList/AList
Adaptive Polling
Hybrid
Full Scan fallback
```

Primary question:

> Can IndexCore discover real cloud-drive changes materially earlier than normal
> AList/OpenList cache refresh, with small controlled provider requests, without
> rebuilding mature provider drivers?

The accepted D0 report establishes the evidence baseline for 115/OpenList/AList/Xiaoya/rclone, request amplification, cache behavior, rate-limit/account risk, large-directory behavior, and remaining live-test UNKNOWNs.

The Architect accepted P0 scoped refresh, P1 bounded hot-scope polling feasibility, P2 durable state design, P3 persistence, P4 one-shot execution, and P5 bounded draining. P5 exit decision is **AUTHORIZE_SCHEDULER_ORCHESTRATION_PROTOTYPE**.

P6 may add only a finite manually-invoked orchestration runner that snapshots due watches once, attempts at most 5 atomic `EmitDuePoll` materializations, then invokes P5 exactly once with at most 5 execution items under a total wall budget <=60s. It must not add ticker/cadence, sleep-until-due, continuous daemon execution, automatic RetryReady/Resume/Repair/Recovery, Mutation Hint API, native delta/provider cursor, production `sync`, or destructive behavior.

## Next product blueprint phase

**Gate 5 — Future Product Architecture**

Current status:

**NOT AUTHORIZED**

The next action belongs to the Architect, not the Worker.

Before any new repository/product implementation begins, Gate 5 planning must
define at least:

- formal successor-product responsibility boundary;
- Web/UI scope;
- auth / user / admin ownership;
- search / catalog responsibility;
- share / favorite / history / playback responsibility;
- preview / download / 302 behavior;
- IndexCore client boundary and deployment topology;
- data ownership outside IndexCore;
- migration/cutover policy, if any;
- explicit relationship to CloudSite Legacy/Frozen.

## Not authorized yet

Do not begin:

- a formal CloudSite successor;
- CloudSite migration;
- auth/user/admin;
- search/catalog;
- favorites/history/playback;
- share subsystem;
- preview/player/Office;
- download gateway / 302 product behavior;
- 115 downloader;
- AI;
- CMS;
- direct IndexCore write APIs;
- direct browser-to-IndexCore public exposure;
- changes to frozen Gate 1B/1C semantics;
- Scanner Resume / multi-daemon HA unless separately planned.
- native delta implementation unless a future provider capability review explicitly authorizes it;
- production adaptive polling / scheduler or continuous dirty executor;
- orchestration behavior outside the bounded P6 manually-invoked prototype;
- Mutation Hint production integration until separately authorized.

## Recovery rule

If a future session asks to “continue”, do **not** start implementation from chat history alone.

First read:

- `PROJECT-STATE.md`;
- this file;
- `docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`;
- the blueprint Gate-5 section.

Then follow the active Architect-authorized phase. For incremental work, P6 is the bounded manually-invoked scheduler-orchestration prototype; for product work, Gate 5 still requires a separate architecture plan.
