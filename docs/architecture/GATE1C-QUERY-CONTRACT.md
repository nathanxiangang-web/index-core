# Gate 1C — Query Contract

> Implementation-facing contract for the minimal provider-neutral, read-only
> Consumer surface over the Canonical Inventory and the Canonical Change Journal.
> Produced by the Codex Executor (Worker) for ChatGPT Architect review.
> Status: **PARTIAL_FOR_ARCH_REVIEW** — reworked per PR #43 Architect review (round 2); A/B/C are NOT FROZEN.
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
| Q5 | Resolve canonical path / hierarchy | `resolve_path(root_id, canonical_path, options?) -> PathResolution` | all live resource(s) at path + ambiguity | `DERIVED` |
| Q6 | List active resources | `list_active_resources(root_id, page?) -> Page[ResourceView]` | PRESENT only | `DERIVED` |
| Q7 | Explicit tombstone / history access | `list_removed(root_id, page?) -> Page[ResourceView]` | REMOVED only (explicit) | `DERIVED` |
| Q8 | Read Change Journal from cursor | `read_journal(scope, cursor_vector, limit) -> Page[JournalEventView]` | per-root ordered events | `DERIVED` |
| Q9 | Root / generation / status | `get_root_status(root_id) -> RootStatus \| None` | lifecycle + generation + counters | `DERIVED` |

> `DERIVED` Consumers must not directly mutate Canonical Inventory; the Query
> Contract has no mutating operation (GATE1A C3.3; INV-008). `FACT`.

> `DERIVED` (Architect, PR #43 review #5) — `canonical_path` is a NON-unique
> coordinate (doc A `C-C3a`). Q5 therefore returns a `PathResolution` set, never a
> single silently-chosen resource. `FACT`.

> `DERIVED` (Architect, PR #43 review round 2) — an all-roots journal read (Q8)
> guarantees ordering only WITHIN each root (`event_seq`); it defines NO canonical
> global order across roots. `FACT`.

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
| `canonical_path` | text \| null | derived, NOT identity, NOT unique |
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
>
> `DERIVED` `canonical_path` is a coordinate, not an identity: two `ResourceView`
> rows may carry the same `canonical_path` (R8 imposter / path reuse). Consumers
> MUST key on `resource_id`. `FACT` (Architect, PR #43 review #5).

### 2.3 `JournalEventView`

| Field | Type | Notes |
|-------|------|-------|
| `event_seq` | integer | per-root logical sequence — the authoritative cursor |
| `event_id` | integer \| null | Opaque surrogate identity ONLY. MUST NOT be used as an ordering or cross-root consumption cursor. |
| `root_id` | id | |
| `generation_number` | integer | |
| `intra_generation_seq` | integer | orders the RENAME/MOVE + UPDATE pair |
| `event_type` | enum | `resource-added` / `resource-updated` / `resource-renamed` / `resource-moved` / `resource-removed` / `root-deprecated` / `root-deleted` |
| `resource_id` | id \| null | null for root events |
| `payload` | opaque | event-specific |
| `committed_at` | timestamp | |

> `DERIVED` (Architect, PR #43 review #1) — the only authoritative journal cursor
> is the per-root `event_seq`. `event_id` is retained for opaque identity/debug
> lookup and MUST NOT be used to order or resume consumption. `FACT`.
>
> `DERIVED` (Architect, PR #43 review round 2) — `JournalEventView` ordering is
> per-root only. An all-roots result has NO canonical cross-root order; consumers
> MUST NOT treat the physical merge order as authoritative. `FACT`.

### 2.4 `RootStatus`

| Field | Type | Notes |
|-------|------|-------|
| `root_id` | id | |
| `lifecycle_state` | enum | |
| `current_generation` | integer | |
| `last_applied_admission_seq` | integer \| null | ordering high-water mark (may be admin-scoped) |

> `CANDIDATE` — whether `last_applied_admission_seq` is consumer-visible or
> admin-only. Worker recommends admin-only (not needed by normal consumers).

### 2.5 `PathResolution` (Q5 result)

`DERIVED` (Architect, PR #43 review #5) — because R8 permits an old resource to
remain `PRESENT` with MISSING evidence while a new resource occupies the same
path, path resolution MUST express overlap explicitly and MUST NOT fabricate a
unique winner. `FACT`.

| Field | Type | Notes |
|-------|------|-------|
| `matches` | `[ResourceView]` | all resources at the path (default `PRESENT` only; `REMOVED` only when explicitly requested) |
| `ambiguous` | `bool` | `true` iff `matches` contains more than one entry |

> `CANDIDATE` — whether an optional deterministic tie-break hint (e.g. newest
> `introduced_at_generation`, or the row with `removal_evidence_state='NONE'`) is
> also returned is deferred. Any such hint is advisory: it never removes the
> `ambiguous` signal or the full `matches` set.

---

## 3. Visibility rules

| # | Rule | Tag | Evidence |
|---|------|-----|----------|
| V1 | Default resource reads return `resource_presence = 'PRESENT'` only. | `DERIVED` | GATE1A C3.2; GATE1B-SAFE-RECONCILE Sec 1.3 |
| V2 | `REMOVED` tombstones are returned ONLY when explicitly requested (`options.include_removed = true`, or Q7). | `DERIVED` | GATE1B-DOMAIN-MODEL 1.4 ("active queries normally exclude REMOVED") |
| V3 | `removal_evidence_state` / `missing_since` / missing counters are NEVER exposed as resource status. | `DERIVED` | Blocker F |
| V4 | `list_roots` default filter: `ACTIVE` (and `NEW`). | `DERIVED` | Architect acceptance, PR #43 review round 2; GATE1B Sec 10.4 |
| V5 | `DEPRECATED` roots are returned only with `options.include_deprecated = true`. | `DERIVED` | Architect acceptance, PR #43 review round 2 |
| V6 | `DELETED` roots are returned only with `options.include_deleted = true`; their resources remain readable (retained partition) when explicitly requested. | `DERIVED` | Architect acceptance, PR #43 review round 2; GATE1B Sec 10.3 |
| V7 | A DELETED root still reports its last committed generation and its resources at their last committed presence (some MAY still be `PRESENT`, because root deletion is a ROOT-level tombstone and does NOT cascade) for audit. | `DERIVED` | Architect acceptance, PR #43 review round 3; GATE1B Sec 10.3 |
| V8 | A resource reappearing after removal is a NEW `resource_id` (fresh ADD); queries never merge it with the prior tombstone. | `DERIVED` | GATE1B-SAFE-RECONCILE Sec 1.3.4 |
| V9 | `resolve_path` returns ALL live matches and sets `ambiguous=true` when a path holds overlapping `PRESENT` resources (R8 imposter); it never fabricates uniqueness. | `DERIVED` | PR #43 review #5; doc A `C-C3a` |

> `DERIVED` (Architect acceptance, PR #43 review round 2) — Root default
> visibility is **CLOSED**: `ACTIVE` (+`NEW`) are default-visible; `DEPRECATED`
> and `DELETED` require explicit request; a deleted root's partition is retained
> for audit. V4-V7 are accepted, NOT proposals. `FACT` (Architect).

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
| JC2 | The authoritative per-root cursor is `event_seq`. There is NO global `event_id` cursor; `event_id` is opaque identity only. Cross-root consumers track a per-root cursor vector `{root_id: event_seq}`. | `DERIVED` (Architect, PR #43 review #1) | doc A Sec 3.9 |
| JC3 | A consumer stores its last-seen per-root `event_seq` and resumes from `last_seen + 1`; within a root it may see duplicates only if it re-reads an inclusive boundary, never gaps. It MUST NOT assume `event_id` allocation order equals commit-visibility order. | `DERIVED` | doc A Sec 3.9 |
| JC4 | Corrective events (journal repair) are normal appended events; consumers process them like any event (canonical-wins). | `DERIVED` | GATE1B-SAFE-RECONCILE J6, Sec 3.4 |
| JC5 | The Journal never contains `MISSING`/`REMOVAL_CANDIDATE`/`UNCHANGED`/`CONFLICT`/`REJECTED`. | `DERIVED` | Sec 3.1 |
| JC6 | Projection rebuild may be triggered by replaying from a chosen cursor; canonical is never edited to satisfy a projection. | `DERIVED` | GATE1A C4.2, J6 |
| JC7 | `read_journal(scope=all_roots, cursor_vector, limit)` consumes the per-root cursor vector and returns each root's events after that root's cursor; a root absent from the vector starts from its beginning or a caller-chosen floor. | `PROPOSED` | — |
| JC8 | An all-roots journal read guarantees ordering only WITHIN each root (`event_seq`); it defines NO canonical global order across roots. Any physical merge order is non-canonical. | `DERIVED` (Architect, PR #43 review round 2) | doc A Sec 3.9 |

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
| P1 | `list_resources` / `list_active_resources` are ordered by a total deterministic key (e.g. (`canonical_path`, `resource_id`) or (`name`, `resource_id`)); ties break on immutable `resource_id`. | `PROPOSED` |
| P2 | A page cursor is bound to (`root_id`, `generation`, `sort_key`, last-seen key). | `DERIVED` (Architect, PR #43 review #6) |
| P3 | If `index_root.current_generation` differs from the cursor's bound generation when the next page is requested, the Query Contract returns `STALE_CURSOR`; the consumer MUST restart paging at the current generation. Pages from generation G and G+1 MUST NOT be mixed. | `DERIVED` (Architect, PR #43 review #6) |
| P4 | `read_journal` is strictly ordered per root by `event_seq`; journal consumption uses the per-root cursor (Sec 5), not the generation pagination rule. | `DERIVED` |
| P5 | Page size and limits are implementation config. | `CANDIDATE` |

> `DERIVED` (Architect, PR #43 review #6) — a `Page` carries a `next_cursor` bound
> to the generation it was produced at. Consuming a cursor from a stale generation
> yields `STALE_CURSOR` rather than a silently mixed page. `FACT`.

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
| — | Root default visibility closed (ACTIVE+NEW default; DEPRECATED/DELETED explicit) | Sec 3 V4-V7 | `COVERED` |
| — | Journal cursor consumption is per-root and commit-safe | Sec 5 JC2/JC3/JC7 | `COVERED` |
| — | All-roots journal has no canonical global order | Sec 5 JC2/JC7/JC8 | `COVERED` |
| — | Pagination cursor is generation-bound (`STALE_CURSOR`) | Sec 7 P2/P3 | `COVERED` |
| — | Path overlap surfaced explicitly, never a false unique | Sec 2.5, V9 | `COVERED` |
| — | Provider-neutral / CloudSite-agnostic | Sec 8 | `COVERED` |

---

## 11. Open items

| Item | Tag | Note |
|------|-----|------|
| ~~Global vs per-root journal cursor exposure~~ | `DERIVED` (CLOSED — Architect, PR #43 review #1, round 2) | Per-root `event_seq` authoritative; cross-root vector; no global cursor; all-roots read has no canonical global order. Sec 5 JC2/JC8. |
| ~~Pagination strategy~~ | `DERIVED` (CLOSED — Architect, PR #43 review #6) | Cursor bound to `root_id + generation + sort key`; `STALE_CURSOR` on generation change. Sec 7. |
| ~~Default visibility of DEPRECATED/DELETED roots~~ | `DERIVED` (CLOSED — Architect, PR #43 review round 2) | `ACTIVE` (+`NEW`) default-visible; `DEPRECATED`/`DELETED` explicit; deleted partition retained for audit. Sec 3 V4-V7. |
| Path-resolution disambiguation policy | `CANDIDATE` | Overlap is explicit; optional deterministic tie-break hint deferred. Sec 2.5. |
| Page size default / max | `CANDIDATE` | Sec 7 P5. |
| Cross-root journal total order | `DEFERRED` | Not defined; per-root ordering only. Sec 5 JC8. |
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
| QC4 | `read_journal(cursor_vector)` resuming per root | no gaps within a root; ordered by `event_seq` |
| QC5 | Consumer attempts any mutation | no such operation exists (compile-time absence) |
| QC6 | `list_roots` default | ACTIVE (+NEW); no DEPRECATED/DELETED |
| QC7 | `list_roots(include_deleted=true)` | DELETED roots visible; their resources readable at their last committed presence (some remain `PRESENT`); NO cascade tombstone |
| QC8 | ResourceView inspection | contains no `removal_evidence_state` / `missing_since` |
| QC9 | Concurrent reconcile in progress | consumer read sees the prior committed generation, never a half-commit |
| QC10 | Projection stale vs canonical | consumer rebuilds from Journal; canonical unchanged |
| QC11 | Page 1 read at generation G; root advances to G+1; Page 2 requested with the Page-1 cursor | `STALE_CURSOR`; consumer restarts paging; no page mixes G and G+1 |
| QC12 | `resolve_path` for a path holding an old MISSING-but-PRESENT resource and a new imposter | returns BOTH matches with `ambiguous=true`; never a silent single winner |
| QC13 | Two roots produce journal events concurrently; consumer uses a per-root cursor vector | every committed event is read exactly once per root; no reliance on `event_id` order |
| QC14 | `read_journal(scope=all_roots)` with a per-root cursor vector | returns each root's events in `event_seq` order; NO cross-root canonical order is asserted or relied upon |
| QC15 | Root deleted while child resources were `PRESENT` | children stay `PRESENT` (root deletion is ROOT-level only, NO cascade tombstone); excluded from default reads, but readable via the root's retained partition / explicit request |

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
