# IndexCore — Next Actions

> Last reviewed: 2026-11-06. This file is the current action board, not a replacement for historical evidence.

## 1. Verified baseline

~~~text
Repository: nathanxiangang-web/index-core
Branch: main
Current main: 9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e
Latest accepted code PR: #145
Exact merged-main CI: #40265606961 SUCCESS
Operating mode: Stable Alpha Foundation / Incremental Hardening
~~~

Gate 1–4 and incremental P0–P11 are Architect-accepted. P12 shortened cross-repository test validation is accepted. The original >=30-minute / >=100-visibility P12 acceptance soak was not run and is not claimed. Gate 5 remains NOT AUTHORIZED.

## 2. Current technical workstreams

### A. Production correctness / stability — Issue #132 and Issue #144

- PR #145 merged as 9a33d011.
- The last recorded operator receipt (2026-10-26) reports N1 hotfix deployed and observed for 31 probes / 1800 seconds with zero fatal, cycle-failure, and unplanned-restart deltas.
- The same receipt did not witness a natural changed-source/APPLIED result. Actual source HTTP starts and 429/5xx were NOT_TRACKED.
- N2 remains HOLD; performance acceptance is separate.
- Before any further production decision, obtain a fresh read-only host receipt. Do not call the 2026-10-26 evidence today's live status.

References: [Issue #132](https://github.com/nathanxiangang-web/index-core/issues/132), [Issue #144](https://github.com/nathanxiangang-web/index-core/issues/144), [PR #145](https://github.com/nathanxiangang-web/index-core/pull/145).

### B. First-discovery and mirror-revisit latency — Issue #134

- Issue remains OPEN.
- Repeated scope work and long due-age samples were observed in the 2026-10-26 receipt.
- F1 first-discovery and F2 mirror-revisit remain default-off.
- Next evidence must distinguish queue wait, provider HTTP time, Store/Kernel time, and consumer Q5/Q8 visibility.
- Require fairness and freshness-debt/capacity analysis before any production flag change.

Reference: [Issue #134](https://github.com/nathanxiangang-web/index-core/issues/134).

### C. Bounded source-read pipeline — Issue #138

- Issue remains OPEN.
- PR #142 and PR #143 are merged; PR #143 added a bounded read/serial-commit pipeline with reader default 1 and explicit 2/4/8 opt-ins.
- The 1/2/4/8 performance evidence is isolated fake-source evidence, not a real 115 quota or production SLA.
- Do not raise production reader count or shorten source intervals without actual HTTP starts/statuses, shared upstream quota evidence, fairness/capacity results, rollback-to-serial proof, and operator authorization.

Reference: [Issue #138](https://github.com/nathanxiangang-web/index-core/issues/138), [ADR-012](docs/decisions/ADR-012-RUNTIME-READ-POOL.md).

### D. Reliability / capacity follow-ups

- Issue #120 remains OPEN; whole P0 reliability is not accepted.
- PR #133 remains OPEN / unmerged; it requires independent review and exact-head CI.
- PR #126 remains OPEN / draft with incomplete 50k capacity evidence; 50k is NOT PASSED.
- Do not mix these candidates into unrelated fixes or deploy an unmerged PR.

References: [Issue #120](https://github.com/nathanxiangang-web/index-core/issues/120), [PR #133](https://github.com/nathanxiangang-web/index-core/pull/133), [PR #126](https://github.com/nathanxiangang-web/index-core/pull/126).

## 3. Execution order for the next session

1. Verify the current remote main, open PRs, relevant Issues, and exact CI. Do not rely on the baseline above without refreshing it.
2. Confirm the Owner's requested outcome and classify it as Collector, Kernel, Store, Query/Transport, Consumer, Operations, or Out of Scope.
3. Select one bounded Issue. Do not start multiple workstreams at once.
4. Capture a read-only baseline before any runtime/provider change.
5. Write explicit acceptance predicates and forbidden changes.
6. Assign one Executor; branch from the exact verified main SHA.
7. Implement, test, submit one PR, and wait for independent Architect review.
8. Update this file and PROJECT-STATE.md only when accepted facts change.

The current documentation standard is defined in AGENTS.md, docs/DEVELOPMENT.md, docs/AI-DEVELOPMENT-PROTOCOL.md, and docs/OPERATING-STANDARDS.md.

## 4. Task state vocabulary

Use:

~~~text
TODO
READY
IN_PROGRESS
REVIEW
FIX_REQUIRED
BLOCKED
DONE
~~~

DONE means independently accepted, merged, and post-merge verified. REVIEW is not accepted. BLOCKED must state the missing evidence, dependency, or authorization.

## 5. Not authorized by a generic “continue”

- Gate 5 or a successor product architecture.
- Hybrid runtime default-on.
- Production F1/F2 activation.
- Production source readers above 1.
- Provider-native delta/cursor or direct 115 integration.
- Public/network-reachable Mutation Hint.
- Second writer, multi-daemon HA, or parallel P6 cycles.
- Automatic INTERNAL retry or automatic BLOCKED/SUSPENDED repair.
- Destructive scoped removal.
- Schema/contract expansion or migration without a separate accepted plan.
- Product auth/users, search/catalog, preview/download, sharing, or AI product features.

A future explicit Owner/Architect decision may authorize a bounded proposal. An open Issue, draft PR, benchmark, or historical production receipt does not grant authorization.

## 6. Recovery rule

A new Architect session should read:

1. AGENTS.md;
2. PROJECT-CONTEXT.md;
3. PROJECT-STATE.md;
4. ARCHITECTURE-INVARIANTS.md;
5. this file;
6. the current authorized Issue/PR and relevant evidence.

Then read only the module contracts and source needed for the task.

**Chat history is cache; Git is project memory.**