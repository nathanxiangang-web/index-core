# Gate 1C — Query Contract

> Implementation-facing contract for the minimal provider-neutral, read-only
> Consumer surface over the Canonical Inventory and the Canonical Change Journal.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Baseline: remote `main` = `6a131f17657807d9aee2921be1f286ceaff784e4`.
> Depends on `GATE1C-POSTGRESQL-STORE.md` (A) and `GATE1C-TRANSACTION-BOUNDARY.md` (B).

---

## 0. Scope and tag system

### 0.1 Scope

This document freezes the minimum read-only Query Contract required by Issue #40
deliverable C. It is **provider-neutral** and **CloudSite-agnostic**: it exposes
Kernel canonical truth only.

Non-goals: Store Interface (Kernel-private, doc A/B), Collector adapter
(deliverable E), CloudSite endpoints (forbidden), write paths (forbidden), authz
implementation (`DEFERRED`).

### 0.2 Tag system

Same as docs A/B: `DERIVED` / `PROPOSED` / `CANDIDATE` / `DEFERRED` / `REJECTED`,
evidence `FACT` / `INFERENCE` / `UNKNOWN`.

### 0.3 Two interfaces, never collapsed

> `DERIVED` Store Interface is Kernel-private (read-for-reconcile + write canonical
> + journal + commit/rollback). Query Contract is Consumer-facing (read-only).
> The Consumer holds no reference to the Store Interface (GATE1A C3.1). `FACT`.

```text
Kernel  --uses-->  Store Interface  --backed by-->  PostgreSQL
Consumer --uses--> Query Contract   --backed by-->  PostgreSQL (read-only)
```

---

## 1. Query Contract operations (minimum surface)

`PROPOSED` — operation names and shapes are the Worker's proposal; the
**operation set** is required by Issue #40 deliverable C.

| # | Operation | Signature (conceptual) | Returns | Tag |
|---|-----------|------------------------|---------|-----|
| Q1 | Get root | `get_root(root_id) -> RootView \| None` | one root | `DERIVED` |
| Q2 | List roots | `list_roots(filter?) -> Page[RootView]` | roots | `DERIVED` |
| Q3 | Get resource | `get_resource(resource_id, options?) -> ResourceView \| None` | one resource | `DERIVED` |
| Q4 | List resources under root/parent | `list_resources(root_id, parent_resource_id?, options?, page?) -> Page[ResourceView]` | children of a parent | `DERIVED` |
| Q5 | Resolve canonical path / hierarchy | `resolve_path(root_id, canonical_path) -> ResourceView \| None` | resource at path | `DERIVED` |
| Q6 | List active resources | `list_active_resources(root_id, page?) -> Page[ResourceView]` | PRESENT only | `DERIVED` |
| Q7 | Explicit tombstone / history access | `list_removed(root_id, page?) -> Page[ResourceView]` | REMOVED only (explicit) | `DERIVED` |
| Q8 | Read Change Journal from cursor | `read_journal(scope, from_cursor, limit) -> Page[JournalEventView]` | ordered events | `DERIVED` |
| Q9 | Root / generation / status | `get_root_status(root_id) -> RootStatus \| None` | lifecycle + generation + counters | `DERIVED` |

> `DERIVED` Consumers must not directly mutate Canonical Inventory; the Query
> Contract has no mutating operation (GATE1A C3.3; INV-008). `FACT`.

---

## 2. View shapes

### 2.1 `RootView`

| Field | Type | Notes |
|-------|------|-------|
| `root_id` | id | immutable |
| `scope_descriptor` | opaque | provider-neutral; no vendor semantics interpreted |
| `lifecycle_state` | enum | `NEW` / `ACTIVE` / `DEPRECATED` / `DELETED` |
| `current_generation` | integer | version marker |
| `created_at` | timestamp | |

### 2.2 `ResourceView`

| Field | Type | Notes |
|-------|------|-------|
| `resource_id` | id | canonical identity |
| `root_id` | id | partition |
| `canonical_path` | text \| null | derived, not identity |
| `parent_resource_id` | id \| null | derived hierarchy |
| `name` | text | |
| `is_dir` | bool | |
| `size` | int \| null | |
| `mtime` | timestamp \| null | |
| `content_hash` | text \| null | OPTIONAL fingerprint |
| `content_type` | text \| null | |
| `resource_presence` | enum | `PRESENT` / `REMOVED` |
| `introduced_at_generation` | integer | |
| `last_confirmed_generation` | integer | |

> `DERIVED` `ResourceView` **MUST NOT** expose `removal_evidence_state`,
> `missing_since`, or the consecutive-missing counter as resource status. Those
> are Kernel-internal (GATE1B-SAFE-RECONCILE Sec 1.3, Blocker F). `FACT`.
>
> `DERIVED` `resource_presence` is the ONLY consumer-visible presence signal.
> `FACT`.

### 2.3 `JournalEventView`

| Field | Type | Notes |
|-------|------|-------|
| `event_seq` | integer | per-root logical sequence |
| `event_id` | integer | global physical order (cross-root cursor) |
| `root_id` | id | |
| `generation_number` | integer | |
| `intra_generation_seq` | integer | orders the RENAME/MOVE + UPDATE pair |
| `event_type` | enum | `resource-added` / `resource-updated` / `resource-renamed` / `resource-moved` / `resource-removed` / `root-deprecated` / `root-deleted` |
| `resource_id` | id \| null | null for root events |
| `payload` | opaque | event-specific |
| `committed_at` | timestamp | |

### 2.4 `RootStatus`

| Field | Type | Notes |
|-------|------|-------|
| `root_id` | id | |
| `lifecycle_state` | enum | |
| `current_generation` | integer | |
| `last_applied_admission_seq` | integer \| null | ordering high-water mark (may be admin-scoped) |

> `CANDIDATE` — whether `last_applied_admission_seq` is consumer-visible or
> admin-only. Worker recommends admin-only (not needed by normal consumers).

---

## 3. Visibility rules

| # | Rule | Tag | Evidence |
|---|------|-----|----------|
| V1 | Default resource reads return `resource_presence = 'PRESENT'` only. | `DERIVED` | GATE1A C3.2; GATE1B-SAFE-RECONCILE Sec 1.3 | 
| V2 | `REMOVED` tombstones are returned ONLY when explicitly requested (`options.include_removed = true`, or Q7). | `DERIVED` | GATE1B-DOMAIN-MODEL 1.4 ("active queries normally exclude REMOVED") |
| V3 | `removal_evidence_state` / `missing_since` / missing counters are NEVER exposed as resource status. | `DERIVED` | Blocker F |
| V4 | `list_roots` default filter: `ACTIVE` (and `NEW`). | `PROPOSED` | GATE1B Sec 10.4 defers root visibility to Gate 1C |
| V5 | `DEPRECATED` roots are returned only with `options.include_deprecated = true`. | `PROPOSED` | GATE1B Sec 10.4 |
| V6 | `DELETED` roots are returned only with `options.include_deleted = true`; their resources remain readable (retained partition) when explicitly requested. | `PROPOSED` | GATE1B Sec 10.3/10.4 |
| V7 | A DELETED root still reports its last committed generation and tombstoned resources for audit. | `PROPOSED` | GATE1B Sec 10.3 |
| V8 | A resource reappearing after removal is a NEW `resource_id` (fresh ADD); queries never merge it with the prior tombstone. | `DERIVED` | GATE1B-SAFE-RECONCILE Sec 1.3.4 |

> `UNKNOWN` — the default visibility of `DEPRECATED`/`DELETED` roots is a Gate 1C
> Query decision (GATE1B Sec 10.4). V4-V7 are the Worker's `PROPOSED` answer and
> require Architect acceptance.

---

## 4. Consistent read at a generation

| # | Rule | Tag |
|---|------|-----|
| CR1 | Every read returns data from a single committed generation; a consumer never observes a half-committed reconcile. | `DERIVED` (GATE1A C3.2) |
| CR2 | `RootView.current_generation` / `RootStatus.current_generation` is the version marker a consumer can record and compare to detect change. | `PROPOSED` |
| CR3 | Reads of *arbitrary historical* generations (time travel) are NOT part of the minimal contract. | `DEFERRED` (POST_MVP) |
| CR4 | Journal replay is the mechanism for reconstructing history, not historical snapshot reads. | `DERIVED` (GATE1A C3.2, C4.2) |

> `INFERENCE` for CR3: the schema (doc A) stores current canonical state plus an
> append-only journal; arbitrary point-in-time canonical reconstruction is a
> derived projection concern, not a kernel read surface.

---

## 5. Journal cursor / projection catch-up

| # | Rule | Tag | Evidence |
|---|------|-----|----------|
| JC1 | Journal reads are ordered and gap-free within committed transactions. | `DERIVED` | GATE1A C1.3 |
| JC2 | Per-root cursor is `event_seq`; global cursor is `event_id`. | `PROPOSED` | doc A Sec 3.9 |
| JC3 | A consumer stores its last-seen cursor and resumes from `last_seen + 1`; it may see duplicates only if it re-reads an inclusive boundary, never gaps. | `PROPOSED` | — |
| JC4 | Corrective events (journal repair) are normal appended events; consumers process them like any event (canonical-wins). | `DERIVED` | GATE1B-SAFE-RECONCILE J6, Sec 3.4 |
| JC5 | The Journal never contains `MISSING`/`REMOVAL_CANDIDATE`/`UNCHANGED`/`CONFLICT`/`REJECTED`. | `DERIVED` | Sec 3.1 |
| JC6 | Projection rebuild may be triggered by replaying from a chosen cursor; canonical is never edited to satisfy a projection. | `DERIVED` | GATE1A C4.2, J6 |

> `DERIVED` A projection that disagrees with canonical rebuilds from the Journal;
> canonical wins (GATE1A C4.2; J6). `FACT`.

---

## 6. Write prohibition

| # | Prohibition | Tag | Evidence |
|---|-------------|-----|----------|
| W1 | The Query Contract exposes NO mutating operation. | `DERIVED` | INV-008 |
| W2 | Consumers hold no reference to the Store Interface. | `DERIVED` | GATE1A C3.1 |
| W3 | Consumers must not access the database directly (no SQL/ORM). | `DERIVED` | GATE1A C3.3 |
| W4 | A consumer may only *request* that a reconcile be scheduled; it never runs one. | `DERIVED` | GATE1A C3.2/C3.3 |
| W5 | A consumer treats its own projection as non-authoritative. | `DERIVED` | GATE1A C4.2 |

---

## 7. Paging, ordering, stability

| # | Rule | Tag |
|---|------|-----|
| P1 | `list_resources` is ordered by (`canonical_path` or `name`) for stable paging. | `PROPOSED` |
| P2 | Paging uses a stable key (cursor or (path, resource_id)) rather than offset under concurrent writes. | `CANDIDATE` |
| P3 | `read_journal` is strictly ordered by cursor. | `DERIVED` |
| P4 | Page size and limits are implementation config. | `CANDIDATE` |

---

## 8. Provider neutrality and CloudSite independence

| # | Rule | Tag |
|---|------|-----|
| N1 | No operation is provider-specific (no AList/rclone/vendor field). | `DERIVED` (INV-006) |
| N2 | No CloudSite-specific endpoint, field, or business rule appears. | `DERIVED` (forbidden in Gate 1C) |
| N3 | `scope_descriptor` is opaque; the Query Contract does not interpret it. | `DERIVED` |

---

## 9. Authz / transport (deferred)

| Item | Tag |
|------|-----|
| Authentication / authorization model | `DEFERRED` |
| Transport (in-process, SQL, gRPC, event bus) | `DEFERRED` |
| Cross-consumer fan-out / event bus | `DEFERRED` (GATE1A C3.4) |
| Rate limiting / quotas | `DEFERRED` |

---

## 10. Consistency-check mapping (Issue #40)

| # | Check | Where | Status |
|---|-------|-------|--------|
| 9 | Consumers have no write path around Kernel | Sec 6 | `COVERED` |
| — | `removal_evidence_state` not exposed | Sec 2.2, V3 | `COVERED` |
| — | Tombstone/history only on explicit request | V2, Q7 | `COVERED` |
| — | Journal cursor consumption | Sec 5 | `COVERED` |
| — | Provider-neutral / CloudSite-agnostic | Sec 8 | `COVERED` |

---

## 11. Open items

| Item | Tag | Note |
|------|-----|------|
| Default visibility of DEPRECATED/DELETED roots | `PROPOSED` (needs Architect) | Sec 3 V4-V7 |
| Global vs per-root journal cursor exposure | `CANDIDATE` | Sec 5 JC2 |
| Paging strategy | `CANDIDATE` | Sec 7 |
| Admin/ops visibility of removal evidence | `DEFERRED` | Sec 2.4 |
| Arbitrary historical generation reads | `DEFERRED` (POST_MVP) | Sec 4 CR3 |
| Authz | `DEFERRED` | Sec 9 |

---

## 12. Golden cases

| # | Case | Expected |
|---|------|----------|
| QC1 | `list_active_resources` on a root with tombstones | returns PRESENT only |
| QC2 | `get_resource(removed_id)` default | `None` |
| QC3 | `get_resource(removed_id, include_removed=true)` | returns REMOVED tombstone |
| QC4 | `read_journal(from_cursor=last_seen+1)` | no gaps; ordered |
| QC5 | Consumer attempts any mutation | no such operation exists (compile-time absence) |
| QC6 | `list_roots` default | ACTIVE (+NEW); no DEPRECATED/DELETED |
| QC7 | `list_roots(include_deleted=true)` | DELETED roots visible with tombstoned resources readable |
| QC8 | ResourceView inspection | contains no `removal_evidence_state` / `missing_since` |
| QC9 | Concurrent reconcile in progress | consumer read sees the prior committed generation, never a half-commit |
| QC10 | Projection stale vs canonical | consumer rebuilds from Journal; canonical unchanged |

---

## 13. Self-check against Gate 1C constraints

| Constraint | Status |
|------------|--------|
| No CloudSite-specific endpoints | Met |
| No UI | Met |
| No product MVP | Met |
| Consumer has no write path | Met (Sec 6) |
| No new gate | Met |
| No silent Gate 1B change | Met |
| Did not merge own PR | Met |