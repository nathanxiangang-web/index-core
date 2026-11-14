# ADR-014: transactional Root budget registration and safe prior indexing

Status: PROPOSED, Owner-authorized Issue134 / PR148 follow-up. Independent
architecture review required. This does not authorize a production deployment.

## Decision and module boundaries

Register operational budget metadata at Root creation, rather than discovering
Roots from managed Watches once per Runtime actor. Migration0007 adds an AFTER
INSERT trigger on `index_root`, inserts one budget row with `ON CONFLICT DO
NOTHING`, and backfills **all existing Roots** once in the same migration
transaction. An empty, inactive or F4-disabled Root has an inert budget row;
selection still requires an ACTIVE Root and a due managed Watch. Space is
O(Roots), not O(Watches). Root lifecycle and Kernel ownership do not change.

The trigger writes only the operational budget table. It does not create Work,
Watch bindings, provider I/O, generations, Canonical changes or Journal events.
Registration failure aborts Root creation. Normal transaction retry can succeed
without restarting the Runtime. PostgreSQL crash recovery commits or rolls back
the Root and budget together. The migration's Root-table lock fences concurrent
Root inserts across trigger installation/backfill; run migrations with the
single writer stopped. Existing cursors/reservations are never overwritten.

`PrepareMirrorRolling` remains an O(1) singleton readiness check, performed once
per actor. No wake-time reconciliation scan or new recovery worker is added.
Remove redundant registration on each parent discovery. Every supported Root
creation path, including raw INSERT and F4-disabled administration, passes the
database trigger. Budget rows are persistent operational state, not a disposable
cache: manual deletion/disabled triggers are unsupported, and not an automatic
repair contract. Budget INSERT privileges must be available to the Root creator;
the trigger is invoker-rights, with no SECURITY DEFINER escalation.

Successful work acknowledgement releases a covered pre-existing reservation even
when F4 is now disabled. It still checks Root/scope/verified sequence inside the
existing completion transaction; newer Hint intent survives. Flag-off never
renews/rebinds/retires a Watch. Failure, cancelled/lost claims and unverified
work retain reservations. Durable global admission time is not reset on re-enable.

Migration0008 adds `(resource_id, observation_id DESC)` to authoritative identity
observations. `prior.go` SQL and returned context remain unchanged. No current
projection/cache becomes authority; no parallel Canonical writer or admission
batch is introduced. Index maintenance adds one B-tree write per observation;
index size/build duration/WAL on real history remain deployment measurements.

## Alternatives and extension constraints

- Re-run Watch backfill on every wake: rejected, unbounded and still race-prone.
- Patch only `CreateWatch`: misses transactional F2 enrollment and policy changes.
- Watch-row trigger: unnecessary per-Watch writes; UPDATE paths risk acquiring
  scheduling metadata after Watch locks. Root creation establishes metadata first.
- In-memory registration set: not crash durable and cannot atomically cover Root
  creation. A durable registration queue would add worker/repair complexity for
  one row per Root without demonstrated need.
- Whole-Root cache: not implemented. Generation alone cannot invalidate an
  evidence-only commit. A future cache needs a reviewed complete revision fence,
  transaction snapshot rules, immutable ownership, bounded memory and atomic
  invalidation with Canonical/evidence changes. Preserve FIFO/identity/Journal.

Future Watch creation mechanisms may assume the migration-enforced Root-creation
invariant, but must not delete budget state on expiry, flag-off or a failed claim.
Physical Root deletion/reclamation needs separate lifecycle/foreign-key review.
The scheduler still rotates Root/scope cursors; pre-registering idle Roots cannot
make them candidates or spend budget. Existing multi-Root/global-atomicity tests
remain required alongside dynamic registration tests.

## Runtime-account permission closure (2026-11-15 follow-up)

The whole-chain review found owner-only fixtures and Ping/schema-only doctor
insufficient for an invoker trigger. Keep0007 unchanged and invoker-rights.
Explicit base/rolling GRANT recipes now define the writer profiles; doctor and
serve perform bounded read-only has_schema_privilege/has_table_privilege checks
using the configured runtime account before actors/listeners. No automatic grant,
SECURITY DEFINER, ownership or PUBLIC grant is introduced. OFF still requires
budget INSERT for Root creation and SELECT/UPDATE for covered completion; ON
additionally needs global ledger/binding privileges. The runtime has no migration,
schema CREATE, budget DELETE or TRUNCATE requirement.

Real non-owner LOGIN tests execute the exact recipes and actual Fake HTTP/Kernel
paths in OFF and ON, and prove denied registration rolls back and can be retried.
A relation-lock barrier proves concurrent Root insertion waits across0007;
an0008 failure after committed0007 is resumable. See the
[complete-chain review](../p0/P0-134-F4-FINAL-REVIEW.md). Production identities and
ACLs remain NOT_VERIFIED until separately authorized operator preflight.

## Current acceptance boundary

See [functional and prior evidence](../p0/P0-134-F4-REGISTRATION.md) and
[deployment/rollback plan](../p0/P0-134-F4-DEPLOYMENT.md). The Owner moved20K,
throughput, resource and long stability validation to separately authorized real
deployment. Historical capacity NO-GO is retained, not converted to PASS and not
a blocker for this functional-development delivery. Do not merge or deploy
without the corresponding independent review/authorization.
