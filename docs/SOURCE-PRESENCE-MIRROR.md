# ADR: opt-in source presence mirror

## Issue115 correction: virtual-overlay success is not completeness

The initial live acceptance below was insufficient. A later successful root list
contained only a virtual alias and caused749 false removals. OpenList v4.2.6
internal/fs/list.go suppresses a backend List error whenever virtual children
exist. Mount detail fields can be hidden, so item shape/count cannot detect it.

Coverage v2 now requires independent before/after storage topology evidence from
the optional protected topology credential. Only a bounded GET of
/api/admin/storage/list uses it; decode only routing facts, never persist/log
provider addition/password fields. Config stores topology_token_env, not a token.
No API/HTTP error, truncated topology, unhealthy covering storage, nested virtual
mount, missing credential or changed topology can mint absence authority.
Such observations remain additive PARTIAL; positive inventory/discovery continues.
The coordinator rejects all old v1 proofs and config lacking the topology ref.
IO3 v5 includes the topology digest, avoiding replay of old false-removal decisions.

Do not disable unrelated mounts or move physical files to bypass this guard.
A scope containing a nested virtual mount cannot safely mirror absence with this
OpenList API. Its deletions remain paused; require an independently trustworthy
source view before enabling. Only a concrete115 Open covering storage is
authority-qualified; Alias/unknown drivers cannot certify another namespace.
Other drivers need dedicated failure/completeness contracts and tests. Safe
child scopes may retain bounded confirmation.
Recovery uses existing root-scoped queue CAS and normal re-observation, not SQL
resurrection of old tombstones. Confirm all library/download partitions and
surviving sibling subtrees over subsequent cycles, not a single initial total.

Original upstream behavior: https://github.com/OpenListTeam/OpenList/blob/v4.2.6/internal/fs/list.go

## Original motivation and bounded design

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
