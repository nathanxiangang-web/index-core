# ADR: opt-in source presence mirror

2026-10-18. Human requests that deleting a real drive directory promptly removes
its indexed files from CloudSite, rather than preserving them indefinitely.
Production evidence: `/图库` no longer exists in a successful forced parent list,
but four children remain PRESENT. No recurring watch exists and ordinary scoped
PARTIAL input never starts the root's 24-hour removal grace.

## Decision and module boundaries

Preserve ordinary PARTIAL/full-snapshot rules. Introduce a separate explicit
**direct-child coverage** gate for an opt-in source mirror. Do not label a scoped
listing as a whole-root COMPLETE snapshot, weaken failed-scan rules, delete
physical files, or let a consumer redefine presence.

- Collector: one forced list, explicit total matching all returned children,
  bounded entries, valid unique names, successful source-identity checks before
  and after the list. A complete parent list can establish a missing directory;
  a 500/404 from the missing directory itself cannot authorize removal.
- Scan service: persist versioned `directory_coverage` in immutable Snapshot
  provenance, bound to exact adapter digest, scope and observed root generation.
- Coordinator: under Root lock, recheck fresh evidence, direct-child shape,
  config opt-in and adapter row lock, namespace/scope and generation fence.
  Include coverage in IO3 identity, including empty scopes. Mismatch is stale
  input, not deletion; storage errors roll back rather than becoming stale.
- Pure Kernel: reconcile positive observations normally; confirm missing direct
  children immediately. A missing directory confirms its prior path subtree;
  siblings and identities observed at a new path are excluded. Preserve logical
  tombstones and existing atomic generation/Journal writes. Conflicted observed
  paths are protected, but identity drift elsewhere cannot freeze confirmed
  absence. Coverage IO3 uses v4-directory-coverage to avoid replaying the earlier
  all-conflicts-block-absence decision. This gate bypasses whole-root grace.
- Existing incremental executor: mirror results enqueue bounded child scopes via
  existing atomic parent/child completion (1000 per scope, eight levels). Other
  roots retain their configured discovery prefix. No new scheduler or Job engine.
- `root watch`: operator enables/disables the root's existing HOT watch by CAS;
  default interval30s, configurable15s..24h; root HIGH priority stays ahead of its
  NORMAL descendant expansion without bypassing cadence. Each observation is bounded;
  large trees may need multiple cycles, so no universal30s SLA is claimed.
- Consumers: continue reading PRESENT and consuming resource-removed Journal;
  no frontend hide list, direct provider crawl or hand-written canonical UPDATE.

## Configuration and operational rollback

Adapter JSON adds `mirror_presence: true` and `expected_base_path` matching the
actual OpenList account namespace (not the root-relative scan scope). Default is
off. A changed login/base path must fail closed instead of erasing the inventory.
Keep provider secrets as existing environment references. No migration required;
coverage uses existing provenance JSON and watch uses existing operational tables.

Deploy only the owned IndexCore binary after isolated PostgreSQL/HTTP regression
and exact-head CI. Back up binary/config and database before enabling the single
owned Root. Retain source/provider data and tombstones. To stop future mirroring,
disable the watch and restore the adapter config, then restore the previous
binary. A binary rollback does not resurrect confirmed tombstones; reappearance
must be observed normally, never reverse canonical rows by SQL.

## Acceptance and extension constraints

Test directory/subtree removal, ordinary file and empty-directory removal,
path-prefix siblings, default PARTIAL omission, malformed/truncated/HTTP/auth
failure, namespace change, stale generation/config, empty-scope identity,
transaction rollback/Journal atomicity and replay. Run existing core/PG tests.
Production acceptance requires removed Canonical + Q8 events + actual resource
API absence, with surviving resources still PRESENT. Never call StartDownload.

Future providers need an equivalent explicit coverage/namespace contract and
the same pure/PG tests; a generic absence or provider error is not equivalent.

## Live evidence, 2026-10-18

Code efd3cdca8d6443bc0431a7df97b165f177c26df3 / CI39100197664 SUCCESS is deployed
to the owned public catalog process. Isolated PostgreSQL18 directory/mirror/watch
tests and vet passed; full exact-head CI passed. An initial remote full run hit
the existing50ms P5 deadline-before-claim test under constrained Docker load;
that test was not weakened, and the full GitHub CI passes.

The first live rollout did not remove the gallery: unrelated mtime identity
conflicts suppressed absence and NORMAL root watch work was starved by children.
The successor scopes conflict protection to observed identities/paths, uses the
v4 coverage IO3 contract and HIGH root watch priority, with pure/PG regressions.
10 gallery rows now REMOVED,10 matching Q8 removals in one generation, and four
image Q5 lookups return no PRESENT match. Public authenticated CloudSite inventory
contains zero gallery rows with276 surviving files. Three obsolete alias rows
also absent from this namespace converged, so total new root removals are13.
Only logical tombstones were written through the coordinator; no physical file
deletion, direct canonical SQL, new Provider start, schema change or other Root
configuration change. Protected old binary/config/database backups remain.
