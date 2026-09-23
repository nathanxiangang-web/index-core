# Gate 1A Worker B — Collector / Scanner Contract Boundary

> Phase: Gate 1A — Responsibility Boundary & Contract Skeleton
> Worker: B (Collector / Scanner contract)
> Baseline: `6190d00`
> Date: 2026-09-23
> Status: READY_FOR_FOREMAN_CROSSCHECK
> Output target: `.work/gate1a/W-B-COLLECTOR-CONTRACT.md`
>
> Scope rule: this document defines the responsibility boundary and the minimal
> conceptual contract between the Collector/Scanner layer and the Index Kernel.
> It does NOT freeze the final SnapshotEntry schema, does NOT design the
> `complete=true` acceptance algorithm, does NOT select a Collector, does NOT
> write product code, and does NOT design PostgreSQL tables.
>
> Evidence base: ARCHITECTURE-INVARIANTS (INV-001..INV-020), PROJECT-CONTEXT,
> blueprint v0.1 sections 3/4/9, D02 report (AList/OpenList), D03 report
> (rclone/fsspec gap comparison).

---

## 0. Layering context

```text
Provider (external)
      |
Collector / Scanner   <-- this document
      |  Snapshot + evidence
Index Kernel          <-- Worker A boundary
      |  Canonical Inventory + Change Journal
Store (PostgreSQL)    <-- Worker C boundary
      |
Consumers             <-- Worker C boundary
```

The Collector/Scanner layer sits between external providers and the Kernel.
Its formal output is a **Snapshot** (a first-class object, blueprint section 9)
plus **Snapshot-level evidence**. The Kernel consumes the Snapshot; it never
reaches into provider APIs directly (INV-006, INV-009).

---

## B1. Collector Input (concepts only)

The Collector accepts a **ScanRequest**. Concrete provider configuration format
is NOT designed here (CANDIDATE). The following concepts are the minimal input
surface:

| Concept | Meaning | Status |
|---------|---------|--------|
| `SourceRef` | Identifies the external source: provider kind (alist / openlist / rclone / webdav / s3 / local / feed / snapshot-file) plus endpoint/address. NOT a stable resource identity. | ACCEPTED_BOUNDARY |
| `RootRef` | The ResourceRoot being scanned. Carries `root_id` (Kernel-assigned) and `source_ref`. One Collector run targets one root. | ACCEPTED_BOUNDARY |
| `CredentialRef` | A **reference** (handle / secret name / vault path) to credentials. The Collector never receives raw secrets in the contract; it resolves them at the adapter boundary. The contract carries only the reference. | ACCEPTED_BOUNDARY |
| `ScanScope` | Which subtree(s) of the root to traverse. Default = entire root. May be a set of path prefixes. Does NOT imply completeness of the un-scanned portion. | ACCEPTED_BOUNDARY |
| `RefreshPolicy` | Cache / freshness directive: `force_refresh` (bypass provider cache), `allow_cached` (use cache, record staleness), `max_age`. Maps to AList `refresh=true` / rclone VFS cache options. | CANDIDATE |
| `TraversalOptions` | Adapter-level hints: max depth, concurrency, pagination size, timeout. These are **hints**, not Kernel domain. The Kernel does not depend on them. | CANDIDATE |
| `CollectorMode` | `full_snapshot` (default for Gate 1) vs `delta_hint` (deferred, see D-DEFER-1). The Kernel treats all input as snapshot evidence; a delta hint is an optimization, not a separate truth path. | ACCEPTED_BOUNDARY (full_snapshot) / DEFERRED_POST_MVP_INCREMENTAL (delta_hint) |

### B1 boundaries

- **ACCEPTED_BOUNDARY**: The Kernel never constructs a `ScanRequest`. The
  Kernel may request "a fresh snapshot for root R" but the Collector (or an
  orchestrator above it) assembles the `ScanRequest`.
- **ACCEPTED_BOUNDARY**: Credentials cross the Collector adapter boundary, not
  the Kernel contract boundary. The Kernel never sees raw credentials.
- **REJECTED**: The Collector does NOT receive `canonical_resource_id` as input.
  Identity is a Kernel output, not a Collector input (INV-010, blueprint section 7).

---

## B2. Collector Output — SnapshotEntry (CANDIDATE, not frozen)

The Collector produces a Snapshot containing a list of `SnapshotEntry` records.
The field set below is a **candidate**, not a frozen schema. Final field names,
types, and optionality are deferred to Gate 1B (D-DEFER-2).

```text
SnapshotEntry (CANDIDATE):
  provider_object_id   : optional   -- DRIVER_DEPENDENT; absent on local/webdav/s3
  parent_ref           : required   -- reference to parent entry or root (tree reconstruction)
  path                 : required   -- observed path at scan time; NOT stable identity (INV-010)
  name                 : required   -- leaf name
  is_dir               : required   -- directory flag
  size                 : optional   -- byte size; absent or 0 for directories
  mtime                : optional   -- modification time (provider-reported)
  hash                 : optional   -- DRIVER_DEPENDENT; blueprint: "hash optional", never mandatory
  metadata             : optional   -- provider-specific bag (e.g. thumb, ctime, storage_id)
```

### B2 optionality rules (ACCEPTED_BOUNDARY)

| Field | Mandatory? | Reason |
|-------|-----------|--------|
| `path` | YES (per entry) | Needed for tree reconstruction and path-based matching key. NOT identity. |
| `name` | YES | Leaf component. |
| `is_dir` | YES | Distinguishes file / directory. |
| `provider_object_id` | **NO** | DRIVER_DEPENDENT (D02 section 3, D03 Q1). Local/WebDAV/S3 do not provide it. Must remain optional (INV-011). |
| `hash` | **NO** | DRIVER_DEPENDENT (D02 section 3, D03 section 4). Blueprint section 6: "first stage must never require all providers to have hash". Must remain optional. |
| `size` | NO | May be absent for directories or unsupported providers. |
| `mtime` | NO | May be absent or unreliable (D02: ctime may fall back to mtime). |
| `metadata` | NO | Open bag; Kernel must not depend on specific keys. |

- **ACCEPTED_BOUNDARY**: `provider_object_id`, `hash`, and any native-delta
  field are OPTIONAL in the contract. An adapter that provides them fills them;
  an adapter that does not leaves them absent. The Kernel must not require them
  (INV-011).
- **REJECTED**: Making `hash` or `provider_object_id` mandatory. This would
  silently exclude local/WebDAV/S3 providers and violate the blueprint.
- **CANDIDATE**: `parent_ref` representation (opaque id vs path string) is not
  frozen. Gate 1B will decide the tree-reconstruction mechanism.

---

## B3. Snapshot-level evidence (what the Kernel needs)

The Collector must provide Snapshot-level evidence alongside the entry list.
The Collector provides **evidence**; the Kernel **decides** completeness
acceptance (B4). This section defines what evidence the Kernel needs, NOT the
final acceptance algorithm (D-DEFER-3).

### B3.1 Required Snapshot evidence

| Evidence | Meaning | Status |
|----------|---------|--------|
| `source_ref` | Which source produced this snapshot. | ACCEPTED_BOUNDARY |
| `root_ref` | Which ResourceRoot this snapshot belongs to. | ACCEPTED_BOUNDARY |
| `started_at` | Wall-clock start of the scan. | ACCEPTED_BOUNDARY |
| `finished_at` | Wall-clock end of the scan (absent if scan did not finish). | ACCEPTED_BOUNDARY |
| `traversal_status` | One of: `success`, `partial`, `failed`, `interrupted`. Set by the Collector based on traversal outcome. **Not** the final completeness verdict. | ACCEPTED_BOUNDARY |
| `error_summary` | Aggregated errors: count, typed categories (permission_denied / not_found / aborted / timeout / unknown), first error message. The Collector captures provider errors; it does NOT interpret them as removal. | ACCEPTED_BOUNDARY |
| `skipped_scopes` | List of path prefixes or directories that were skipped, each with a reason. May be empty. May be incomplete if the adapter cannot enumerate skips (D03: rclone logs skips but does not return a structured skipped-set via RC). | CANDIDATE |
| `freshness_evidence` | Cache state at scan time: `cache_bypassed` (refresh was forced), `cache_age_seconds` (if cached), `provider_cache_ttl`. Lets the Kernel judge staleness. | CANDIDATE |
| `entry_count` | Number of entries in the snapshot. Material for weak sanity check only. | CANDIDATE |
| `byte_count` | Sum of sizes. Material for weak sanity check only. | CANDIDATE |
| `adapter_generation` | Optional adapter-reported generation/cursor if the provider exposes one (e.g. rclone ChangeNotify token). Absent for most providers. | DEFERRED_POST_MVP_INCREMENTAL |

### B3.2 What `traversal_status` means (ACCEPTED_BOUNDARY)

- `success`: the traversal completed without errors. This is a **contract-level
  success signal from the adapter**, NOT proof of provider-complete snapshot
  (D02 section 5, D03 Q3). Silent backend truncation cannot be disproven.
- `partial`: the traversal encountered errors but produced a partial entry list.
  The Collector MUST set this when `error_summary` is non-empty.
- `failed`: the traversal did not produce a usable entry list.
- `interrupted`: the scan was stopped (timeout, cancel, `--max-duration`) before
  completion.

### B3.3 What the Kernel needs for completeness judgment (ACCEPTED_BOUNDARY)

The Kernel's completeness acceptance (final algorithm deferred, D-DEFER-3) needs
at minimum:

1. `traversal_status` — did the adapter report success/partial/failed?
2. `error_summary` — were there typed errors?
3. `skipped_scopes` — were subtrees skipped?
4. `freshness_evidence` — was the data cached or fresh?
5. Historical context (Kernel-owned): prior snapshot generation, expected entry
   count range, root lifecycle state.

The Collector supplies items 1-4. Item 5 is Kernel-internal. The Collector does
NOT compute or set a `complete=true` verdict (B4).

### B3.4 Weak sanity check boundary (ACCEPTED_BOUNDARY)

`entry_count` and `byte_count` are **weak sanity check material**. Per D02
section 5: `len(content) == total` cannot prove provider-complete snapshot
because `total` may be computed from the returned list. The Kernel may use these
as one input to a safety gate (e.g. unexpected-shrink guard), but they are
never sufficient alone to authorize destructive reconcile (INV-004).

---

## B4. Collector must NOT decide

These responsibilities are explicitly **REJECTED** for the Collector layer.
They belong to the Kernel (Worker A boundary) or are deferred.

| Responsibility | Owner | Status | Reason |
|----------------|-------|--------|--------|
| Canonical `resource_id` assignment | Kernel | REJECTED (from Collector) | Identity is Kernel domain (INV-010, blueprint section 7). Collector provides `provider_object_id` as a hint; Kernel decides identity. |
| Confirmed removal (`missing -> deleted`) | Kernel | REJECTED (from Collector) | INV-003: missing is not deleted. Collector reports absence; Kernel runs the Missing -> Removal Candidate -> Validation -> Confirmed Removed pipeline (blueprint section 8). |
| Canonical generation number | Kernel | REJECTED (from Collector) | Generation is Kernel/Store domain (blueprint section 6, Worker C). |
| Canonical change type (add/update/rename/move/remove) | Kernel | REJECTED (from Collector) | Change type is a reconcile output (blueprint section 6, NEXT-ACTIONS E). Collector reports observed state, not semantic change. |
| Final completeness acceptance (`complete=true`) | Kernel | REJECTED (from Collector) | The Collector reports `traversal_status` + evidence; the Kernel decides whether the snapshot is acceptable for destructive reconcile (INV-004, D02 section 5). |
| Identity continuity across snapshots | Kernel | REJECTED (from Collector) | Stable identity across rename/move is a Kernel algorithm (blueprint section 7, deferred to Gate 1B). |
| Conflict resolution | Kernel | REJECTED (from Collector) | When identity is ambiguous, the Kernel emits `conflict`; it never auto-binds (blueprint section 7). |
| Canonical Change Journal entries | Kernel | REJECTED (from Collector) | The Change Journal is canonical and distinct from provider-native delta (INV-012, blueprint section 6). |
| Direct mutation of Canonical Inventory | — | REJECTED (from Collector) | INV-007, INV-009: external facts cross a defined contract boundary; implicit direct writes are prohibited. |

---

## B5. Adapter fit analysis (NO selection)

This section maps AList and rclone to the contract defined above. It is a **fit
analysis only**. It does NOT conclude "must use rclone" or "must use AList".
Final Collector selection is an Architect decision after Gate 1A cross-check.

### B5.1 AList / OpenList adapter fit

Evidence: D02 report (sections 3, 4, 5, 6, 9).

| Contract element | AList `/api/fs/list` | OpenList `/api/fs/list` |
|------------------|---------------------|------------------------|
| `name` | DIRECT | DIRECT |
| `size` | DIRECT | DIRECT |
| `is_dir` | DIRECT | DIRECT |
| `mtime` (`modified`) | DIRECT | DIRECT |
| `path` | DIRECT (`virtual_path`) | DERIVABLE (reconstruct `Join(parent, name)`) |
| `provider_object_id` | DRIVER_DEPENDENT (cloud drivers only; local/webdav/s3 empty) | UNAVAILABLE (API does not expose `id`) |
| `hash` | DRIVER_DEPENDENT (`hash_info`; local/webdav/s3 empty) | DRIVER_DEPENDENT |
| `metadata` | DIRECT (thumb, created, provider) | DIRECT (created, provider) |
| `traversal_status` | Can set `partial` only if adapter detects error; **D02 section 4: `storage.List` error is silently swallowed** when `virtualFiles` non-empty — adapter may incorrectly report `success` | Same as AList |
| `error_summary` | WEAK — errors swallowed by `storage.List`; adapter must add explicit error capture at its own boundary | WEAK — same |
| `skipped_scopes` | NOT AVAILABLE from API; adapter must infer from missing subtrees | NOT AVAILABLE |
| `freshness_evidence` | DIRECT — adapter knows whether `refresh=true` was sent and cache TTL (default 30 min, D02 C13) | DIRECT — same |
| `entry_count` / `total` | DIRECT but **weak** — `total` is post-filter count; `len(content)==total` does not prove completeness (D02 section 5) | DIRECT but weak — same |

**AList/OpenList fit summary:**
- CAN produce SnapshotEntry candidates with `name/size/is_dir/mtime/path` and
  optional `provider_object_id`/`hash` (driver-dependent).
- CAN provide `freshness_evidence` (refresh flag, cache TTL).
- **Missing/weak evidence**: structured `skipped_scopes` (error silently
  swallowed), reliable `traversal_status` (HTTP 200 does not mean complete),
  `error_summary` (errors hidden by `storage.List`).
- **Gap**: the adapter must add its own error-capture boundary to compensate
  for the provider's silent error swallowing. Even with that, it cannot prove
  provider-complete snapshot (D02 section 5).
- OpenList additionally lacks `provider_object_id` entirely.

### B5.2 rclone adapter fit

Evidence: D03 report (sections 4, 5), D03 W-D matrix (section 2).

| Contract element | rclone (`lsjson` / RC `list`) |
|------------------|-------------------------------|
| `name` | DIRECT |
| `size` | DIRECT |
| `is_dir` | DIRECT |
| `mtime` (`ModTime`) | DIRECT |
| `path` | DIRECT |
| `provider_object_id` | DRIVER_DEPENDENT — `IDer` optional interface; implemented by GDrive/OneDrive/Box/B2/Aliyundrive/115; **absent on local/webdav/s3** (D03 Q1, W-D 2.1) |
| `hash` | DRIVER_DEPENDENT — `Hashes()` in core interface, 68 backends implement; WebDAV returns `hash.None`; S3 ETag is not a content hash for multipart (D03 W-D 2.6) |
| `metadata` | DIRECT (opt-in via `lsjson` options) |
| `traversal_status` | STRONGER than AList — explicit errors propagate (`listRwalk` returns non-nil error even when continuing other directories, D03 Q3); adapter can reliably set `partial`/`failed` on explicit error. Still cannot detect silent backend truncation. |
| `error_summary` | PARTIAL — typed errors (`ErrorDirNotFound`, `ErrorPermissionDenied`, `ErrorListAborted`); error count reported; only first error wrapped (W-D 2.3) |
| `skipped_scopes` | NOT structured via RC — skipped directories are logged with path but not returned as a structured set (W-D 2.3). Adapter must parse logs or track skips itself. |
| `freshness_evidence` | DIRECT — adapter knows VFS cache config; `--refresh` / `no-cache` options |
| `entry_count` | DIRECT (count of returned entries) |
| `checkpoint/resume` | NO — rclone has no scan-state persistence; interrupted scan restarts from root (W-D 2.4). Blueprint defers checkpoint to Scanner Resume phase (section 18). |

**rclone fit summary:**
- CAN produce SnapshotEntry candidates with the same core fields as AList plus
  stronger typed-error propagation.
- CAN reliably set `traversal_status = partial` when the backend returns an
  explicit error (stronger than AList's silent swallow).
- **Missing/weak evidence**: structured `skipped_scopes` (logged but not
  returned via RC), no proof against silent backend truncation (D03 Q3),
  `provider_object_id` absent on local/webdav/s3.
- **Gap**: cannot authoritatively prove `complete=true` (D03 Q3: "contract-level
  completeness with explicit failure propagation, not proof against silent
  backend truncation").
- No checkpoint/resume (structural absence, not a config gap).

### B5.3 Comparison (NO selection)

| Dimension | AList/OpenList | rclone |
|-----------|----------------|--------|
| Core entry fields | Sufficient | Sufficient |
| `provider_object_id` | DRIVER_DEPENDENT / UNAVAILABLE | DRIVER_DEPENDENT (more backends implement `IDer`) |
| `hash` | DRIVER_DEPENDENT | DRIVER_DEPENDENT (more backends, but S3 multipart caveat) |
| Error propagation | WEAK (silently swallowed) | STRONGER (typed, propagated) |
| `traversal_status` reliability | WEAK (HTTP 200 != complete) | STRONGER (explicit error -> partial) |
| `skipped_scopes` | Not available | Logged but not structured via RC |
| Silent truncation proof | NO | NO |
| Checkpoint/resume | NO | NO |
| Native delta | NO | DRIVER_DEPENDENT (polling, 14/69 backends) |

- **ACCEPTED_BOUNDARY**: Neither adapter can authoritatively set
  `complete=true`. Both require the Kernel's completeness acceptance gate
  (B4, INV-004).
- **ACCEPTED_BOUNDARY**: Neither adapter provides a universal stable identity.
  Both leave identity to the Kernel (INV-010).
- **CANDIDATE**: Both are viable adapter candidates. rclone has stronger error
  semantics; AList/OpenList are already deployed in the target ecosystem.
  Selection is an Architect decision, not a Worker B conclusion.
- **REJECTED**: Concluding "must use rclone" or "must use AList" from this
  analysis.

---

## Boundary summary

### Collector / Scanner OWNS

| Responsibility | Status |
|----------------|--------|
| Traversal / pagination of external provider | ACCEPTED_BOUNDARY |
| Producing SnapshotEntry records (candidate schema) | ACCEPTED_BOUNDARY |
| Producing Snapshot-level evidence (B3.1) | ACCEPTED_BOUNDARY |
| Provider error capture (to the extent the adapter can observe) | ACCEPTED_BOUNDARY |
| Refresh / cache-bypass policy execution | ACCEPTED_BOUNDARY |
| Adapter-level timeout / concurrency / depth hints | CANDIDATE |
| Scan checkpoint / resume (blueprint section 18: deferred to Scanner Resume phase, NOT Kernel) | DEFERRED_POST_MVP_SCANNER_RESUME |

### Collector / Scanner does NOT own (REJECTED)

See B4. Summary: canonical identity, removal confirmation, generation, change
type, completeness acceptance, identity continuity, conflict resolution,
Change Journal, direct inventory mutation.

### Kernel OWNS (cross-reference, not this Worker's scope)

Canonical identity, completeness acceptance, safe reconcile, canonical
inventory, change journal, conflict resolution, generation. (Worker A
boundary.)

---

## Deferred items

| ID | Item | Deferred to | Reason |
|----|------|-------------|--------|
| D-DEFER-1 | Delta / incremental input mode (`CollectorMode = delta_hint`) | DEFERRED_POST_MVP_INCREMENTAL | Blueprint section 10: full snapshot first; section 19: phase 3 incremental. |
| D-DEFER-2 | Final SnapshotEntry schema (field names, types, `parent_ref` representation) | DEFERRED_TO_GATE1B | Gate 1A freezes boundary, not final schema. |
| D-DEFER-3 | Completeness acceptance algorithm (`complete=true` decision) | DEFERRED_TO_GATE1B | NEXT-ACTIONS B + blueprint section 8: Safe Reconcile semantics. Collector provides evidence; Kernel decides. |
| D-DEFER-4 | Scan checkpoint / resume design | DEFERRED_POST_MVP_SCANNER_RESUME | Blueprint section 18: Scanner Resume phase, after MVP stable. |
| D-DEFER-5 | `adapter_generation` / native delta token semantics | DEFERRED_POST_MVP_INCREMENTAL | Native delta != Change Journal (INV-012); delta mode itself is deferred (D-DEFER-1). |
| D-DEFER-6 | Final Collector selection (AList vs rclone vs direct vs combination) | DEFERRED_TO_GATE1C | Architect decision after contract fit analysis; not a Worker conclusion. |

---

## Consistency check against invariants

| Invariant | How this contract respects it |
|-----------|------------------------------|
| INV-003 (missing != deleted) | B4: Collector does not decide confirmed removal. |
| INV-004 (incomplete input cannot authorize destructive reconcile) | B3/B4: Collector provides evidence; Kernel decides completeness acceptance. |
| INV-006 (Kernel is provider-neutral) | B2: contract is provider-neutral; provider-specific fields are optional. |
| INV-007 (Collector does not own canonical state) | B4: Collector cannot mutate canonical inventory. |
| INV-009 (snapshot/change input is explicit boundary) | This document defines that boundary. |
| INV-010 (identity != path) | B2: `path` is observed path, not identity. B4: identity is Kernel-owned. |
| INV-011 (driver capability != universal capability) | B2: `provider_object_id`/`hash` are optional. |
| INV-012 (native delta != change journal) | B4: Change Journal is Kernel-owned. D-DEFER-1/5 defer delta. |
| INV-020 (keep kernel small) | Checkpoint, traversal, error capture stay in Collector. |

---

## Open questions for Foreman cross-check

1. Should `skipped_scopes` be REQUIRED (even if often empty) or CANDIDATE?
   rclone and AList both struggle to populate it structurally. Marking it
   CANDIDATE lets adapters that can populate it do so without forcing others
   to fabricate it.
2. Should `CredentialRef` be part of this contract or handled entirely above
   the Collector (by an orchestrator)? This document puts it at the Collector
   boundary but not in the Kernel contract.
3. `adapter_generation` is marked DEFERRED_TO_GATE1B. Confirm this is not
   needed for Gate 1A boundary freezing.
