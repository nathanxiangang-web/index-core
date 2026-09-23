# Incremental Change Discovery Research Report

> Status: **IN PROGRESS**
>
> Research issue: #59
>
> Parent architecture issue: #57
>
> Production implementation: **NOT AUTHORIZED**

## 1. Research question

Can IndexCore discover real provider changes materially faster than ordinary
AList/OpenList cache visibility while:

- avoiding repeated full-provider traversal;
- keeping real-provider request volume small and controlled;
- reusing mature provider/driver implementations;
- preserving existing IndexCore safety semantics?

Primary scenario:

```text
user/provider creates a file
        ↓
real cloud drive already contains it
        ↓
AList/OpenList cached view is still stale
        ↓
IndexCore wants earlier visibility
        ↓
Web should see the resource earlier
```

## 2. Evidence vocabulary

Every important conclusion must be tagged:

- **DIRECT** — official docs/API/source proves it;
- **DERIVABLE** — behavior follows clearly from implementation/protocol;
- **DRIVER_DEPENDENT** — true only for specific provider/driver;
- **UNAVAILABLE** — checked and not supported;
- **UNKNOWN** — evidence insufficient.

## 3. Capability matrix

| Capability | 115 Open API | OpenList/AList | rclone | Evidence level | Notes |
| --- | --- | --- | --- | --- | --- |
| native change feed | TBD | TBD | TBD | UNKNOWN | |
| durable provider cursor | TBD | TBD | TBD | UNKNOWN | |
| recent/modified-since listing | TBD | TBD | TBD | UNKNOWN | |
| webhook/callback | TBD | TBD | TBD | UNKNOWN | |
| targeted directory refresh | TBD | TBD | TBD | UNKNOWN | |
| bypass cache / force refresh | TBD | TBD | TBD | UNKNOWN | |
| stable object ID | TBD | TBD | TBD | UNKNOWN | |
| move/rename fidelity | TBD | TBD | TBD | UNKNOWN | |
| delete fidelity | TBD | TBD | TBD | UNKNOWN | |
| pagination/truncation guarantees | TBD | TBD | TBD | UNKNOWN | |
| rate-limit/quota documentation | TBD | TBD | TBD | UNKNOWN | |
| token refresh failure semantics | TBD | TBD | TBD | UNKNOWN | |

## 4. Candidate strategies

### A. Mutation Hint

```text
our uploader/downloader/move operation succeeds
        ↓
known target path/scope
        ↓
trigger targeted verification
```

Research questions:

- how precise is the target scope?
- how soon is the provider list API consistent after mutation success?
- can OpenList/AList refresh only that scope?
- retry/backoff pattern?

### B. Native Delta

```text
provider cursor/change feed
        ↓
only changed objects
```

Research questions:

- does 115 officially expose this?
- durable replay?
- cursor expiry/reset?
- gap detection?
- retention window?
- delete/move fidelity?

### C. Scoped Refresh

```text
known/active directory
        ↓
OpenList/AList forced refresh
        ↓
small real-provider request
```

Research questions:

- exact refresh semantics?
- cache bypass?
- request count?
- recursive or one-directory only?
- huge-directory cost?

### D. Adaptive Polling

```text
hot scope: fast interval
quiet scope: progressively slower
mutation hint: immediately hot again
```

Research questions:

- safe request budget per account/root?
- jitter/backoff?
- account throttling?
- no-change cost over 24h?

### E. Full Scan

Baseline/fallback for:

- unsupported provider;
- uncertain continuity;
- excessive dirty scopes;
- cursor invalidation;
- periodic verification.

## 5. Duplicate-wheel assessment

For each candidate capability classify:

```text
USE_EXISTING
WRAP_EXISTING
ADAPT
CLEAN_ROOM_REWRITE
BUILD_NEW
DO_NOT_BUILD
```

Projects/tools to inspect:

- OpenList/AList;
- rclone;
- official 115 APIs/SDKs;
- mature indexing/checkpoint systems where relevant.

Licensing must be recorded before code reuse is proposed.

## 6. Scenario analysis

### S1 — external upload, AList cache stale

TBD.

### S2 — our own upload/downloader knows mutation destination

TBD.

### S3 — one small hot directory

TBD.

### S4 — one huge directory (10k–30k direct children)

TBD.

### S5 — rename/move

TBD.

### S6 — delete

TBD.

### S7 — provider rate limit / throttle

TBD.

### S8 — token expiry / auth refresh failure

TBD.

### S9 — provider eventual consistency

TBD.

### S10 — many roots share one provider account

TBD.

## 7. Request amplification model

Need to estimate:

```text
requests per targeted refresh
requests per changed object
requests per hot scope per hour/day
requests per provider account
requests for no-change polling
requests for large-directory pagination
```

Do not choose a fixed 2-minute interval before this model is understood.

## 8. Latency targets

Do not freeze a target yet.

Evaluate:

- event-driven seconds-level for known mutations;
- ~1–2 minute targeted refresh;
- 5–15 minute adaptive polling;
- full-scan fallback.

Final target must be evidence-based and provider-specific where necessary.

## 9. Risk register

TBD:

- provider throttling;
- account-risk/anti-abuse behavior;
- stale cache semantics;
- eventual consistency;
- huge-directory amplification;
- token refresh failures;
- driver bugs;
- private/undocumented API dependency;
- duplicate provider implementation;
- false deletion;
- excessive dirty-scope growth.

## 10. Live-test requirements

Only after static research identifies UNKNOWNs.

Potential live tests:

- compare cached list vs forced refresh;
- measure provider request count;
- measure visibility delay after upload;
- measure large-directory refresh;
- simulate auth failure;
- test repeated no-change refresh;
- test several roots on one account.

No production account stress test without an explicit request budget.

## 11. Final decision section

Leave blank until research is complete.

Decision must be one of:

```text
STOP
PROTOTYPE_MUTATION_HINT
PROTOTYPE_NATIVE_DELTA
PROTOTYPE_SCOPED_REFRESH
PROTOTYPE_HYBRID
KEEP_FULL_SCAN_ONLY
```

Implementation remains unauthorized until this section is Architect-approved.
