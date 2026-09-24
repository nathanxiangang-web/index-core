# Incremental P0 — Targeted Scoped Refresh Prototype

> Status: **AUTHORIZED — PROTOTYPE ONLY**
>
> Execution issue: #62
>
> Parent architecture: #57
>
> Accepted D0 research: #59 / PR #60
>
> Production implementation: **NOT AUTHORIZED**

## 1. Decision

D0 did not find a usable native delta/change cursor in the audited public 115 Open API surface.

The first prototype is therefore:

```text
Mutation Hint semantics
        +
Targeted Scoped Refresh
```

The prototype exists to answer one question:

> Given one known changed directory, can IndexCore refresh only that directory,
> ingest the positive observations safely, and make them visible through the
> existing Canonical Inventory faster than normal OpenList/AList cache expiry?

This is not a scheduler, not polling, and not a production sync subsystem.

## 2. P0 flow

```text
known changed scope
        ↓
OpenList /api/fs/list
refresh=true
        ↓
one direct-directory provider refresh
        ↓
single-response bounded directory observation
        ↓
PARTIAL / additive-safe Snapshot
        ↓
existing admission + Kernel + reconcile
        ↓
Canonical Inventory + Journal
```

## 3. Provider boundary

Acceptance path:

```text
IndexCore
  ↓ HTTP
OpenList
  ↓
community-maintained driver using 115 official Open API
  ↓
115
```

P0 must not:

- add a direct IndexCore 115 client;
- use the legacy/private 115 Cloud path as acceptance evidence;
- copy OpenList/AList provider code into IndexCore.

## 4. Single-response rule

D0 found that:

```text
page 1 refresh=true
page 2..N refresh=false
```

avoids repeated provider refresh, but still permits mixed cache generations.

P0 therefore uses one HTTP response for the canonical observation.

Conceptually:

```json
{
  "path": "<scope>",
  "page": 1,
  "per_page": "<max_entries + 1>",
  "refresh": true
}
```

P0 requires:

```text
total == len(content)
total <= max_entries
```

Otherwise it fails closed and submits no Snapshot.

The prototype may choose a conservative bounded default for `max_entries`, but it must be configurable in tests and must not silently truncate.

## 5. Scope semantics

Only direct children of the requested directory are observed.

Example:

```text
scope = /downloads

/downloads/a.mkv        observed
/downloads/newdir       observed
/downloads/newdir/x     not observed in this run
```

No recursion.

## 6. Snapshot semantics

The scoped observation must be converted to a Snapshot with:

```text
TraversalStatus                PARTIAL
CompletenessFlag               PARTIAL
FreshnessEvidence              FRESH_REFRESHED
CollectorCompletenessAssurance WEAK_FAILURE_VISIBILITY
ProviderIdentityAssurance      UNVERIFIED
SkippedScopes                  UNKNOWN
```

These semantics are mandatory.

Therefore:

- positive observations may add/update;
- missing entries do not create removal evidence;
- empty scope result does not delete anything;
- no root-wide completeness claim;
- no stronger provider identity claim;
- no 115 fid inference.

Frozen Kernel semantics remain unchanged.

## 7. Preferred implementation shape

Prototype-only additions may be made inside the existing collector/runtime packages.

Preferred internal shape:

```text
alist.Adapter.ScanScope(ctx, scope, maxEntries)
        ↓
adapter.RawScan
        ↓
scan.Service.ScanScope(ctx, rootID, scope, maxEntries)
        ↓
existing CreateDraftSnapshot
        ↓
SubmitAndAdmitSnapshot
        ↓
Coordinator.ProcessSnapshot
```

Do not change `adapter.Collector` unless the prototype proves it is necessary.

Existing `Adapter.Scan()` and `scan.Service.Scan()` behavior must remain unchanged.

## 8. Invocation

Do not add a production `indexcore sync` command.

Preferred invocation:

- gated integration test;
- test-only probe;
- internal prototype command only if unavoidable.

No public HTTP endpoint.

## 9. Permission boundary

OpenList `refresh=true` requires write-capable permission.

P0 live validation must use a dedicated non-production service identity.

The prototype may call only the list/refresh operation.

It must not call upload, delete, rename, move, copy, or other provider mutation endpoints.

## 10. Deterministic acceptance tests

### Collector

- one request only;
- `refresh=true`;
- direct children only;
- total/count equality required;
- max-entry overflow fails closed;
- 403 surfaces;
- provider error surfaces;
- empty result produces legal PARTIAL observation.

### Kernel/reconcile

Using real PostgreSQL:

- new file can be added;
- changed metadata can update according to frozen rules;
- missing known file is not removed;
- zero-entry scoped Snapshot removes nothing;
- unrelated resources remain unchanged;
- same-root FIFO remains intact.

### Regression

- full AList/OpenList Scan unchanged;
- rclone unchanged;
- Q1–Q9 unchanged;
- no migration;
- no schema change;
- `go test ./...` green.

## 11. Live validation gate

P0 is not Architect-accepted until one non-production 115/OpenList run proves the real scenario:

```text
OpenList cache stale
        ↓
file exists in real 115
        ↓
refresh=false still misses it
        ↓
P0 targeted refresh
        ↓
file becomes visible
        ↓
PARTIAL reconcile
        ↓
Q3/Q4/Q6 can see it
```

Record:

- direct-child count;
- OpenList version/commit;
- refresh request latency;
- response bytes;
- 115 Open page_size / limit_rate config;
- retries;
- any 401/403/429/provider errors;
- end-to-end time from P0 trigger to IndexCore visibility.

## 12. P0 exit

P0 PASS requires:

1. no root-wide traversal;
2. one forced scoped observation per attempt;
3. no truncation;
4. additive-only reconcile;
5. no removal evidence;
6. live earlier visibility than stale cache;
7. full scan regression preserved;
8. frozen contracts unchanged.

## 13. Explicitly deferred after P0

P0 does not authorize:

- adaptive polling;
- 2-minute recurring scheduler;
- mutation event bus;
- persistent dirty-scope queue;
- retry/backoff policy;
- provider-id passthrough;
- large-directory production policy;
- native delta;
- provider cursor;
- destructive delta;
- production sync CLI;
- public sync API.

Those decisions are made only after P0 evidence.
