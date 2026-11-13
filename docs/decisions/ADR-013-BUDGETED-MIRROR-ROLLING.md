# ADR-013: budgeted mirror rolling and identity-bound Watch lifecycle

Status: PROPOSED / Owner-authorized implementation candidate for Issue134.
Base: 9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e. Independent review required.
This does not accept production F1/F2 or supersede the F2 capacity NO-GO.

## Scope and modules

The Owner explicitly requested budget-constrained rolling scheduling, Watch
lifecycle and actual Runtime query costs, reusing merged readers. This separate
branch does not incorporate draft PR147 or unmerged PR146 documentation.

* `incremental/rolling`: validated operational policy, no database/provider I/O.
* `store/postgres`: durable Root/scope cursors, one unresolved background poll
  reservation per Root, admission timestamp, identity/adapter binding and
  bounded lifecycle maintenance. New migration0006 is operational only.
* `runtime/incrementalorch`: bounded maintenance and at most one new background
  emission per cycle; ordinary HOT/Hint/work execution retains existing paths.
* `sourcebudget`: actual background HTTP starts obey an additional fraction of
  existing global/Root/provider ceilings. Existing total ceilings and429 rules
  still apply. Background classification requires the exact durable poll epoch;
  merged foreground intent is not downgraded.
* `runtime/scan`: supplies classification to existing serial/pooled read paths.
* `store/postgres` query tracer: bounded redacted SQL fingerprints, aggregate
  client elapsed/rows/errors. Opt-in at the actual app pool; no query arguments,
  SQL text, source paths, credentials or raw error strings in output.

Kernel, Canonical, Journal, Snapshot admission/ordering/deletion and the serial
commit lane retain their contracts. No new writer, repair or public endpoint.

## Admission and backpressure

Default OFF. Enabling requires F2 and an explicit background share1..50 percent.
The conservative admission spacing is eight estimated HTTP calls multiplied by
the largest configured global/Root/provider spacing, divided by the share.
Eight is an estimate, never reported as an actual request count. Actual HTTP is
also paced separately by class, including login/identity/topology/list requests.
The HTTP governor remains process-local; the poll admission timestamp and
unresolved Root reservations survive restart. This is not a distributed quota
ledger or knowledge of upstream account-wide capacity.

Existing managed watches are adopted lazily with unique PRESENT directory proof.
Root and scope cursors rotate even when proof or protected work prevents emission.
One unresolved F4-admitted background epoch per Root bounds NEW materialization;
RETRY_WAIT/BLOCKED/SUSPENDED/cancellation cannot silently free it. A successfully
verified prefix releases its reservation without acknowledging newer Hint work.
Other Roots and foreground execution continue. Unresolvable work remains visible
as backpressure/debt; no automatic retry/repair policy is added.
Inherited legacy POLL intents are retained outside the new reservation ledger;
transition with a populated legacy backlog is not accepted by this evidence and
requires a separate natural-drain/transition gate before any production enablement.

The requested F2 interval is unchanged. The cursor controls service opportunities,
not a promise that every directory is inspected within that interval. Freshness
debt is anchored to last successful verification, not reset by admission, parent
publication, lease expiry, queue coalescing or a longer scheduling interval.
The scope-coverage freshness clock also survives binding/adapter epoch changes;
only a successful observation of that scope advances it. Unknown coverage is
conservative debt, not permission to reuse another identity's verification.
Both legacy and rolling measurements count successful verification from all
accepted trigger sources, not just POLL_SCHEDULE Watch.last_success_at.
Unsatisfied offered load remains NO-GO even when the Work queue is bounded.

## Watch lifecycle

Only exact WARM/MIRROR_REVISIT/BACKSTOP_ENROLL/NORMAL policy is managed. Bindings
contain canonical resource ID, adapter digest plus tuple revision, and a binding
epoch. New identity or adapter ABA requires positive child discovery and a new
binding epoch with preserved/remerged recovery intent. Operator policy is retained.

An initial24h binding lease is renewed by successful observation of that exact
scope/identity/configuration. Parent listings do not renew child content freshness.
Expiry records EXPIRED but does not stop service or authorize deletion. Retirement
changes only the managed Watch to COLD, retaining all operational/canonical history.
It requires matching expected Watch version/binding epoch/current adapter, unique
ownership, no unresolved Work/Hint, and a committed Kernel resource-removed or
relocation Journal event anchored to an accepted producing Snapshot. Missing,
404, incomplete observations, inactive Root or a replacement at the old path are
not retirement proof. Reappearance can reactivate only an unchanged auto-retired
Watch version, with a fresh binding epoch and recovery intent.

Maintenance visits at most five bindings per cycle with a durable cursor. No
unbounded event stream is created for renewals; committed transition counters are
stored on the binding. Watch totals retain historical COLD rows. This is not
physical Watch garbage collection or a hard bound under arbitrary source churn.

## Validation, compatibility and rollback

Use only the owned dsh PG18/Fake OpenList. Test durable admission/backpressure,
Root fairness, cancellation/claim expiry/restart, adapter ABA/path replacement,
operator changes/Hint races, lifecycle counters, rollback atomicity and no new
canonical/journal effects. Run actual P10 and bounded20K population experiments,
HTTP receiver evidence and tracer aggregates. SQL population is not full traversal;
requested120s freshness remains NO-GO if debt grows. No shortened/weaker SLA.

Migration0006 is explicit; serve never migrates. Old binaries reject the newer
schema. Production rollout, backup/restore, full traversal and sustained soak
remain separate review gates. Flag-off stops the new policy but does not undo
committed Work or resurrect retired Watches; do not run legacy F2 blindly after
retirement. Prefer disable F2 and drain normally while retaining the new binary
and schema; restore an independently validated backup for old-binary rollback.
