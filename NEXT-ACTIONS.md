# Index Core — Next Actions

## Current state

**Gate 4 — Reference Consumer Integration — CLOSED**

Architecture / acceptance owner: **ChatGPT Architect**

Execution owner: **Codex Executor / Worker only for the Architect-authorized P1 execution issue**

Status:

**P0 TARGETED SCOPED REFRESH — ARCHITECT_ACCEPTED / P1 HOT-SCOPE POLLING FEASIBILITY — AUTHORIZED / PRODUCTION IMPLEMENTATION NOT AUTHORIZED**

Current architecture planning:

- Issue #57 — incremental architecture umbrella (OPEN)
- Issue #59 — Change Discovery capability research (COMPLETE)
- Issue #62 — P0 Targeted Scoped Refresh Prototype (**COMPLETE / ARCHITECT_ACCEPTED**)
- PR #64 — P0 implementation + real 115 Open evidence (**MERGED** at `a4b6ec83e637c25f26585f56c3fb906410fa4b9a`)
- Blueprint: `docs/architecture/POST-MVP-INCREMENTAL-BLUEPRINT.md`
- P0 plan: `docs/architecture/INCREMENTAL-P0-SCOPED-REFRESH-PROTOTYPE.md`
- P1 plan: `docs/architecture/INCREMENTAL-P1-HOT-SCOPE-POLLING-PROTOTYPE.md`
- Accepted D0 report: `docs/research/INCREMENTAL-CHANGE-DISCOVERY-REPORT.md`

Research and P0 are complete. The next bounded step is P1 Adaptive Hot-Scope Polling **feasibility only**. Production incremental implementation remains **NOT AUTHORIZED**.

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

Capability discovery and P0 scoped-refresh validation are complete. The current task is **P1 hot-scope polling feasibility**, using the accepted P0 `ScanScope` path as the only refresh actuator.

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

The Architect selected **PROTOTYPE_SCOPED_REFRESH** as P0 and it is now **ARCHITECT_ACCEPTED**. The next bounded step is **P1 Adaptive Hot-Scope Polling Feasibility** to test detection of external writes when no Mutation Hint exists.

Only the test/probe form described in `docs/architecture/INCREMENTAL-P1-HOT-SCOPE-POLLING-PROTOTYPE.md` is authorized. A production polling scheduler, persistent dirty-scope queue, native delta, provider cursor, production `sync`, and destructive behavior remain unauthorized.

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
- production adaptive polling / scheduler beyond the bounded P1 feasibility prototype;
- persistent dirty-scope state until a later Architect decision.

## Recovery rule

If a future session asks to “continue” without a new Gate-5 architecture plan,
do **not** start coding.

First read:

- `PROJECT-STATE.md`;
- this file;
- `docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`;
- the blueprint Gate-5 section.

Then create/approve the Gate-5 architecture plan before assigning Worker work.
