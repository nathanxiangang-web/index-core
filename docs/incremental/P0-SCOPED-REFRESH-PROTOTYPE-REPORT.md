# P0 Targeted Scoped Refresh — Prototype Delivery Report

> Status: **PROTOTYPE DELIVERED — DETERMINISTIC + POSTGRESQL VERIFIED — LIVE VALIDATION PENDING**
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