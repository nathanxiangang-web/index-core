# Gate 4 — Reference Consumer Findings

**Repository:** `nathanxiangang-web/indexcore-reference-web`
**Upstream control issue:** `nathanxiangang-web/index-core#50`
**Authoritative IndexCore baseline:** `nathanxiangang-web/index-core@5d315a9d16fe9a5251c60080d7e581525d7f562c`
**Method:** new, disposable Next.js consumer consuming **only** the read-only IndexCore `/v1` HTTP Query Contract from the server side.

> This is the authoritative Gate 4 Reference Consumer report. A consumer-side mirror is kept at
> `indexcore-reference-web/docs/GATE4-REFERENCE-CONSUMER-REPORT.md`.

## 1. What was built

A minimal Web consumer with no database, no ORM, no IndexCore Go dependency, no AList/rclone
dependency, and no CloudSite code:

```text
Browser
   ↓
Reference Web (Next.js server components)
   ↓ server-side only
typed IndexCore client (src/lib/indexcore)
   ↓
IndexCore HTTP /v1  →  IndexCore  →  PostgreSQL
```

Pages: `/`, `/roots`, `/roots/[rootId]`, `/resources/[resourceId]`, `/resolve`, `/removed`,
`/journal`.

## 2. Boundary proof

| Requirement | Result |
| --- | --- |
| 0 direct PostgreSQL access | PASS |
| 0 IndexCore Go package dependency | PASS |
| 0 AList/OpenList direct dependency | PASS |
| 0 rclone dependency | PASS |
| 0 CloudSite runtime/code dependency | PASS |
| 0 application database | PASS |
| 0 canonical mutation path | PASS |
| 0 Go source in the consumer repository | PASS |
| All IndexCore calls server-side | PASS |
| Browser is never told IndexCore's internal address | PASS |
| Closed enums validated at runtime (not cast) | PASS |

Enforced automatically by `tests/indexcore/boundary.test.ts`: no UI file reads
`INDEXCORE_BASE_URL`/`process.env`, builds an IndexCore origin, renders `client.baseUrl`, or calls
`fetch` directly; `INDEXCORE_BASE_URL` is read in exactly one server-only module; the repository
contains **no `.go` files** and no database/provider/rclone/CloudSite dependency. `lifecycle_state`,
`resource_presence` and `event_type` are validated against their frozen sets in the client — an
unknown value is rejected as `malformed_response`, not silently cast. The controlled `COMPLETE`
verification fixture lives in index-core's test/verification side, never here.

## 3. Q1–Q9 coverage

| Query | Endpoint | Page | Difficulty |
| --- | --- | --- | --- |
| Q1 get_root | `GET /v1/roots/{rootId}` | `/roots/[rootId]` | easy |
| Q2 list_roots | `GET /v1/roots` | `/`, `/roots` | easy |
| Q3 get_resource | `GET /v1/resources/{resourceId}` | `/resources/[resourceId]` | easy |
| Q4 list_resources (hierarchy) | `GET /v1/roots/{rootId}/resources` | `/roots/[rootId]` | easy |
| Q5 resolve_path | `GET /v1/roots/{rootId}/resolve` | `/resolve` | easy; ambiguity explicit |
| Q6 list_active_resources | `GET /v1/roots/{rootId}/active` | `/roots/[rootId]?view=active` | easy; whole-root, distinct from Q4 |
| Q7 list_removed | `GET /v1/roots/{rootId}/removed` | `/removed` | easy, but see §5.3 |
| Q8 read_journal | `GET /v1/roots/{rootId}/journal` | `/journal` | easy |
| Q9 get_root_status | `GET /v1/roots/{rootId}/status` | `/roots/[rootId]` | easy |

## 4. Transport observations (awkward / worth noting)

These are observations, **not** requests to change IndexCore.

1. **Cursor is an opaque base64 JSON blob.** Correct and intentional; the consumer round-trips it
   verbatim. Fine. A consumer that tries to interpret it would be wrong; documented.
2. **`next_cursor` is omitted (empty) on the last page.** The typed client normalizes
   absent → `null`; consumers must not treat `""` as a cursor.
3. **Root visibility needs explicit opt-ins threaded through.** `get_root`/`root_status` return
   `404 not_found` unless `include_deprecated_root` / `include_deleted_root` are passed, while
   `list_roots` uses `include_deprecated` / `include_deleted`. This is the frozen visibility model,
   but the two different parameter names for "the same idea" require care in a consumer.
4. **`resourceDTO` omits null fields** (`omitempty` on most columns). Consumer types must be
   optional-tolerant; a naive "required field" schema would break on partial rows.
5. **`next_cursor` (wire) vs `Next` (Go field)** — cosmetic only.
6. **No global journal ordering** — exposed correctly as per-root `event_seq`; a consumer must not
   imply cross-root order. Our journal page is strictly per-root.
7. **`payload` is a JSON string** in a string field, not a nested object. Consumers parse it
   themselves; acceptable for an audit/journal view.
8. **Closed enums are strict.** `lifecycle_state`, `resource_presence` and `event_type` are closed
   sets; a consumer that casts instead of validating will accept a contract violation. The typed
   client validates them and fails as `malformed_response`.

## 5. Missing capability classification

No IndexCore contract gap was found. Items we considered:

| Item | Classification | Note |
| --- | --- | --- |
| Interpret/derive cursors client-side | Consumer responsibility | Cursors are intentionally opaque. |
| Root picker across many roots (search/sort) | Future product responsibility | `list_roots` returns all; a product would add UX, not a new contract. |
| Server-side pagination beyond `limit` | Consumer responsibility | Existing generation-bound cursor is sufficient. |
| Removed resources produced via `rclone` scans | Provider / Kernel property, not an HTTP gap | `rclone` is additive-only by frozen design (`skipped_scopes` UNKNOWN ⇒ never COMPLETE), so it cannot tombstone. Q7/Q5 are proven with a **controlled COMPLETE snapshot fixture** that lives in index-core's test/verification side (`internal/runtime/e2e/gate4_reference_consumer_fixture_test.go`) and uses the accepted safe ingress (`CreateDraftSnapshot → SubmitAndAdmitSnapshot → ProcessHead`), never the retired test-only path. |
| Authentication / multi-tenant scoping | Out of scope (Gate 4) | IndexCore is a private/loopback service in this gate. |
| Adapter/config introspection over HTTP | Out of scope | Administration is CLI-side, read-only HTTP is consumer-facing. |
| Write APIs | Out of scope | Gate 4 has no canonical mutation path. |

**Conclusion: the current `/v1` Query Contract is sufficient for a new product consumer.** No
separately-reviewed IndexCore change is requested.

## 6. Real IndexCore E2E — reproducible

Reproduce with a real Gate-3 IndexCore stack plus an index-core checkout:

```bash
INDEXCORE_SRC=/path/to/index-core npm run e2e:real   # see docs/E2E-RUNBOOK.md
```

The script builds and starts the Reference Web, prepares real `rclone` sources and roots through
the IndexCore CLI, seeds the controlled COMPLETE fixtures through the **index-core verification
fixture**, and asserts on rendered HTML. Last run: **`PASS=52 FAIL=0`**.

Run against the real Gate-3 IndexCore runtime (`indexcore serve`, PostgreSQL 18, schema v4) with a
real `rclone` scan where applicable, and controlled COMPLETE fixtures where the frozen Kernel
requires them.

**Data shape**

- Root A — real `rclone` scan of a nested tree (`top.txt`, `docs/{report,notes}.txt`,
  `docs/deep/nested.txt`, `media/movie.bin`): 8 entries, additive-only (`SUSPICIOUS`), gen 1 → later gen 2.
- Root B — controlled COMPLETE fixture: `removal-target.txt` confirmed REMOVED after two
  independent COMPLETE snapshots.
- Root C — controlled COMPLETE fixture: `amb.txt` re-appears with different content while the old
  row is still PRESENT (missing evidence inside grace) → two PRESENT canonical rows at one path.
- Root D — real `rclone` scan, then **DEPRECATED** (retained partition with `d-doc.txt`, `sub/deep.txt`).
- Root E — real `rclone` scan, then **DELETED** (retained partition with `e-doc.txt`, `esub/edeep.txt`).

**Observed (via the Web pages, server-side against `/v1`)**

| Scenario | Command (abridged) | Result |
| --- | --- | --- |
| Multiple roots | `GET /roots` | A listed by default; D/E hidden by default, shown with opt-ins |
| Nested hierarchy | `GET /roots/A`, `?parent=<docs>`, `?parent=<docs/deep>` | root level shows `docs/media/top.txt` (not flattened); `/docs` shows `deep/notes.txt/report.txt`; `/docs/deep` shows `nested.txt`; breadcrumb works |
| Whole-root active (Q6) | `GET /roots/A?view=active` | `Q6 list_active_resources`; nested resources included, proving it is not Q4 |
| Resource detail | `GET /resources/<docs>` | canonical path, is_dir, generations rendered |
| Path ambiguity | `GET /resolve?root=C&path=/amb.txt` | `ambiguous: true`, 2 matches, no winner chosen |
| Active / removed | `GET /removed?root=B` | `removal-target.txt` with `REMOVED`; its resource link keeps `include_removed=true` |
| Journal | `GET /journal?root=B` | `resource-added` ×4 then `resource-removed` |
| Pagination | `GET /roots/A?limit=1` | `next_cursor` returned; page 2 rendered |
| Journal pagination | `GET /journal?root=A&limit=3` → next link | next link uses the page's last `event_seq` (no skipped event) |
| Stale cursor UX | page 1 cursor → advance generation (new scan) → reuse cursor | rendered “Data changed while paging · Reload from the first page” (409 `stale_cursor`, not hidden) |
| Deprecated partition nav | `/roots?include_deprecated=1` → root D → `sub/` → breadcrumb → resource | visibility opt-in survives directory, breadcrumb and resource-detail links |
| Deleted partition nav | `/roots?include_deleted=1` → root E → resource | visibility opt-in survives into resource detail |
| Removed on a lifecycle root | `/removed?root=<deprecated>&include_deprecated_root=1` → tombstone | the resource link keeps both `include_removed=true` and `include_deprecated_root=true`, so Q3 is reachable |
| Journal on a lifecycle root | `/journal?root=<deprecated>&include_deprecated_root=1` → resource | the resource link keeps the root visibility opt-in into Q3 |
| Q6 on a lifecycle root | `/roots/<deprecated>?include_deprecated_root=1[&view=active]` | the Q6 entry is hidden; a forced `view=active` shows an explicit notice and falls back to hierarchy (no misleading not_found) |
| IndexCore unavailable | stop `indexcore` container | pages render “IndexCore is unreachable”; Web itself still serves HTTP 200; degraded page still leaks no address |
| Independent restart | start `indexcore` again | `/` recovers and lists roots again |

**Note on removed/ambiguity fixtures:** producing Q7 and Q5 through the normal runtime requires
Kernel-`COMPLETE` snapshots (confirmed-empty skips + strong failure visibility). `rclone` is
deliberately not COMPLETE-capable (ADR-001). The fixture therefore lives in **index-core's
test/verification side** and drives the accepted safe ingress
(`CreateDraftSnapshot → SubmitAndAdmitSnapshot → Coordinator.ProcessHead`); the retired test-only
ingress (`InsertSnapshotStub` / `MarkSnapshotSubmitted` / `ProcessSnapshot`) is not used. The
consumer repository contains no Go source and still touches only `/v1`; no IndexCore code or
contract was changed.

## 7. Verdict

The Reference Web demonstrates that a brand-new consumer can build useful resource pages using
only the public read-only IndexCore HTTP Query Contract, with strong boundary isolation and
without touching IndexCore internals. The contract held for hierarchy, ambiguity, removal,
journal, pagination, stale-cursor, unavailable and restart scenarios.
## 8. Round 1 review response (2026-09-24)

Round 1 accepted the architecture and the consumer boundary and raised seven close-out points.
All are resolved on `gate4/reference-consumer` (PR #2); no new PR was opened.

| # | Round 1 finding | Resolution |
| --- | --- | --- |
| 1 | Q6 existed only in the client/unit test; no page called it, and “via hierarchy” was not true | `/roots/[rootId]?view=active` now really calls `list_active_resources`; E2E asserts nested resources appear (whole-root), which Q4 does not |
| 2 | Journal paging used `after_seq = last_seq + 1`, silently skipping the next event | next link uses the page's last `event_seq` (IndexCore selects `event_seq > after_seq`); E2E asserts the boundary is continuous |
| 3 | Root visibility was not threaded through Q4/breadcrumbs | `include_deprecated_root` / `include_deleted_root` are now passed to `list_resources` and to breadcrumb `get_resource` |
| 4 | The home page rendered the internal `INDEXCORE_BASE_URL` into browser HTML | the address is no longer rendered; a boundary test forbids `.baseUrl` in UI code and the E2E asserts the rendered page never contains it |
| 5 | Real E2E was described in prose only | added `npm run e2e:real`, `scripts/e2e-real.sh`, `scripts/e2e/fixture/main.go`, `docs/E2E-RUNBOOK.md`; last run **PASS=29 FAIL=0** |
| 6 | Closed enums were TypeScript casts, not runtime-validated | `lifecycle_state` / `resource_presence` / `event_type` are validated against their frozen sets → `malformed_response`; contract tests added |
| 7 | The authoritative report must live in the IndexCore repository | added `index-core/docs/gate4/GATE4-REFERENCE-CONSUMER-REPORT.md`; this file is the consumer-side mirror |
## 9. Round 2 review response (2026-09-24)

Round 2 accepted the architecture, Q6, journal paging, enum validation, the address-free health
page, the E2E script shape and Q1–Q9 client coverage. It required closing the "E2E broke the
consumer boundary" gap plus four navigation/leak bugs. All are resolved on `gate4/reference-consumer`
(PR #2); no new consumer PR was opened.

| # | Round 2 finding | Resolution |
| --- | --- | --- |
| 1 | The consumer E2E fixture imported index-core internals, read a DSN, wrote Snapshots and used the retired Gate 3 test ingress → violated 0 Go / 0 PostgreSQL / 0 canonical mutation | fixture deleted from the consumer repo; the controlled COMPLETE fixture now lives in index-core `internal/runtime/e2e/gate4_reference_consumer_fixture_test.go` and uses the accepted safe ingress `CreateDraftSnapshot → SubmitAndAdmitSnapshot → Coordinator.ProcessHead`; a boundary test forbids any `.go` file in the consumer repo |
| 2 | IndexCore-down error text still rendered the internal base URL to the browser | `IndexCoreUnavailableError` and `normalizeBaseUrl` no longer embed the address in their messages (it stays on the structured `url` field); a unit test and an E2E assertion cover both the up and degraded states |
| 3 | Deprecated/deleted visibility was dropped by directory, breadcrumb and resource-detail navigation | `ResourceTable` and `Breadcrumbs` now thread a `linkQuery` of visibility opt-ins through every link |
| 4 | Removed resources lost `include_removed=true` when opened from `/removed` | `/removed` and `/journal` resource links keep `include_removed=true`; resource detail preserves it further |
| 5 | `/resolve` listed deprecated/deleted roots but its form had no matching visibility params | the form now exposes `include_deprecated_root` / `include_deleted_root`, and the match links keep them |
| 6 | No E2E regression for retained deprecated/deleted partitions | E2E now creates a DEPRECATED root D and a DELETED root E, asserting default hiding, opt-in visibility, and navigation across directory, breadcrumb and resource detail |
| 7 | The authoritative report had a commit but no real PR | a docs-only PR is opened against index-core `main` (this report) |

Round 2 verification: `npm run typecheck`, `npm run lint`, `npm test` (38 tests) and `npm run build`
are green; `INDEXCORE_SRC=/path/to/index-core npm run e2e:real` → **PASS=42 FAIL=0**.
## 10. Round 3 review response (2026-09-24)

Round 3 accepted index-core PR #53 (the controlled COMPLETE fixture) and confirmed the three-PR
split; it left exactly two narrow consumer navigation issues. Both are resolved on
`gate4/reference-consumer` (PR #2); the index-core fixture was **not** touched.

| # | Round 3 finding | Resolution |
| --- | --- | --- |
| 1 | `/removed` and `/journal` dropped the DEPRECATED/DELETED root opt-in when following a resource into Q3, so detail fell back to default visibility and returned `not_found` | both pages now accept `include_deprecated_root` / `include_deleted_root` (picker checkboxes + pagination/next links) and thread them into resource-detail links alongside `include_removed` |
| 2 | A DEPRECATED/DELETED root still offered the **Active resources (Q6)** entry, although frozen Q6 has no root-visibility option (misleading 404) | the Q6 entry is hidden for lifecycle roots; a forced `?view=active` renders an explicit notice and falls back to Hierarchy (Q4) |

Round 3 verification: `npm run typecheck`, `npm run lint`, `npm test` (38 tests) and `npm run build`
are green; `INDEXCORE_SRC=/path/to/index-core npm run e2e:real` → **PASS=52 FAIL=0** (adds
removed/journal lifecycle-root navigation and the Q6-refusal regression to the previous 42).