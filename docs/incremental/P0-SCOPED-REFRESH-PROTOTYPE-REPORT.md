# P0 Targeted Scoped Refresh — Prototype Delivery Report

> Status: **PROTOTYPE DELIVERED (Rounds 1–2 corrections applied) — DETERMINISTIC + POSTGRESQL VERIFIED — LIVE VALIDATION PENDING**
>
> **P0 NOT PASS** (live 115/OpenList validation not yet executed)
>
> Execution issue: #62
>
> Parent architecture: #57
>
> Production implementation: **NOT AUTHORIZED**
>
> `FROZEN_CONTRACT_CHANGES: NONE`

## 1. What was implemented

Prototype-only additions; **no existing file was modified**:

| File | Purpose |
| --- | --- |
| `internal/collector/alist/scoped.go` | `Adapter.ScanScope(ctx, scope, maxEntries)` — one forced `refresh=true` observation, no pagination, no recursion, fail-closed. |
| `internal/runtime/scan/scoped.go` | `Service.ScanScope(ctx, rootID, scope, maxEntries)` — PARTIAL Snapshot through the existing draft → admission → Kernel coordinator → reconcile path. |
| `internal/collector/alist/scoped_test.go` | Collector-level deterministic tests. |
| `internal/runtime/scan/scoped_test.go` | Real-PostgreSQL reconcile-safety tests. |
| `internal/runtime/scan/scoped_path_internal_test.go` | Scope containment (`scope` cannot escape the root). |

The generic `adapter.Collector` interface, `Adapter.Scan()`, `Service.Scan()`,
the Kernel, the Query Contract, the HTTP API, the CLI, and all migrations are
unchanged.

## 2. Contract implemented (Issue #62)

Request — exactly one per scope attempt:

```json
{ "path": "<resolved scope>", "password": "", "page": 1,
  "per_page": "<maxEntries + 1>", "refresh": true }
```

Fail closed (no Snapshot) unless:

```text
total == len(content)      (coherent single response)
total <= maxEntries        (no silent truncation)
```

Snapshot semantics (mandatory):

```text
TraversalStatus                = PARTIAL
CompletenessFlag               = PARTIAL
FreshnessEvidence              = FRESH_REFRESHED
CollectorCompletenessAssurance = WEAK_FAILURE_VISIBILITY
ProviderIdentityAssurance      = UNVERIFIED
SkippedScopes                  = UNKNOWN (nil, never confirmed-empty)
```

Scope semantics: only direct children are observed; subdirectories are never
traversed. `scope` is resolved relative to the root's configured adapter path.

Additional P0 rules (Round 2):

- a scope MUST NOT contain `.` / `..` components, and the resolved provider path
  is guaranteed to stay inside the root (no root escape);
- a **non-root** scope MUST already resolve to exactly one `PRESENT` canonical
  directory, otherwise the run fails closed — a scoped observation must never
  create orphan resources whose parent is absent from the Canonical Inventory;
- `max_entries` is clamped by a P0 hard cap (`alist.MaxScopedEntries = 10000`); a
  caller can never widen a scoped observation, and `maxEntries+1` cannot overflow;
- the canonical-parent guard validates the **provider-path namespace** that
  `Adapter.Scan()` records — e.g. configured root `/library` + scope `/sub`
  resolves to provider `/library/sub` and must be a unique PRESENT canonical
  directory — so scoped refresh is correct for **any** configured root path, not
  only `/` (Round 2 fix).

> **Important (Round 1 review): `max_entries` bounds only what IndexCore accepts
> and writes — it does NOT bound the real provider cost.** OpenList loads the
> entire provider directory with `fs.List` *before* applying HTTP pagination, so a
> 30k direct-child directory still triggers the full provider read even when
> IndexCore fails closed on overflow.

## 3. Verification performed

| Area | Evidence | Result |
| --- | --- | --- |
| One `refresh=true` request; `page=1`; `per_page=maxEntries+1` | `TestScanScopeSingleForcedRefresh` | PASS |
| No second page / no recursion | same test (call counter + no child request) | PASS |
| `total != len(content)` fails closed | `TestScanScopeTotalCountMismatchFailsClosed` | PASS |
| `total > maxEntries` fails closed | `TestScanScopeOverflowFailsClosed` | PASS |
| 403 `Refresh without permission` surfaces | `TestScanScopeRefreshPermission403Surfaces` | PASS |
| Provider list error surfaces | `TestScanScopeProviderErrorSurfaces` | PASS |
| Empty directory = legal PARTIAL zero-entry | `TestScanScopeEmptyIsLegalPartial` | PASS |
| Real PostgreSQL: new file adds, no removal on empty/missing, unrelated untouched, same-root generation advances, Q3/Q4/Q6/Q7 consistent | `TestScanScopeReconcileIsAdditiveSafe` | PASS |
| Scope containment (`..` rejected; no root escape) | `TestCanonicalScopePathRejectsTraversal`, `TestScopeAPIPathContainment` | PASS |
| Non-root scope requires exactly one PRESENT canonical directory | `TestScanScopeFailsClosedOnBadScope` | PASS |
| P0 hard cap rejects over-sized / overflow `max_entries` (no request made) | `TestScanScopeRejectsOverHardCap` | PASS |
| Removal evidence stays NONE / `missing_since` NULL / counter 0; persisted Snapshot is PARTIAL/PARTIAL + FRESH_REFRESHED + WEAK | `assertRemovalEvidenceClean` / `assertSnapshotScopedSemantics` | PASS |
| Identity continuity: same path + hash + size, changed mtime → UPDATE, `resource_id` unchanged, `resource-updated` Journal | `TestScanScopeReconcileIsAdditiveSafe` | PASS |
| Absolute same-root FIFO: a stranded older PENDING is drained first, no leapfrog | `TestScanScopeDrainsOlderPendingHead` | PASS |
| Persisted `acceptance_state = PARTIAL` | `assertSnapshotScopedSemantics` | PASS |
| Configured non-`/` root path (canonical parent uses the provider-path namespace) | `TestScanScopeConfiguredRootPathNamespace` | PASS |
| Hard-cap exact boundary (`maxEntries == MaxScopedEntries` allowed; `per_page == Max+1`) | `TestScanScopeAllowsExactHardCap` | PASS |
| Non-AList collector + DELETED root fail closed | `TestScanScopeFailsClosedForUnsupportedAndDeletedRoot` | PASS |
| Full regression `go test -p 1 ./...` | whole repo | PASS |
| `go vet ./...`, `gofmt` | whole repo | clean |

Environment: Go 1.27.1, real PostgreSQL 18 (`indexcore-pg`, port 55432).
No rclone/AList live dependency was added to the new tests — the AList mock is a
deterministic in-process HTTP server.

## 4. Not done (blocks P0 acceptance)

- **Live 115/OpenList validation is NOT executed.** The Worker has no disposable /
  non-production 115 account with the official `115 Open` driver. See
  `docs/incremental/P0-SCOPED-REFRESH-LIVE-TEST-RUNBOOK.md`.
- Until that run proves earlier visibility than the stale cache, **P0 is NOT PASS**
  and production incremental implementation remains unauthorized.

## 5. Frozen-contract statement

- No DB migration / schema change.
- No public HTTP endpoint.
- No production sync CLI.
- No scheduler / polling / dirty-scope queue / retry policy.
- No direct 115 client; no private `115 Cloud` path in the prototype.
- Kernel semantics unchanged; `FROZEN_CONTRACT_CHANGES: NONE`.