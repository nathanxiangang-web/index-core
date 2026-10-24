# ADR010 proposal: bounded fetch seam, not parallel Kernel execution

Status: Issue138 FIRST-PR PROTOTYPE, pending independent Architect review.
Prerequisite: exact c5bb6a68 main CI40070457955 attempt2 SUCCESS; failed attempt1
is retained, historical Hint startup cause UNKNOWN. PR139 independently hardens
that test fixture and is merged as main07492838128f, now included in this branch.
Original prototype head0da97cd6 CI40076090617 SUCCESS does not certify the merged
branch HEAD: new exact-head CI and independent review remain required. Nothing
in this decision authorizes production enablement.

## Module contract

Legacy ScanScope -> shared private fetch/commit implementation, serial as before.
Experimental FetchPrototype -> FetchScope(claim) -> opaque immutable observation
-> one serial CommitFetchedScope -> SAME draft/admission/Coordinator/Kernel path.
No constructor is wired into app, runtime, CLI or configuration. No live pool,
parallel writer, new scheduler, migration or provider credential flow is added.

The prototype bounds retained observations to1..8, one per root,1000 direct
children,8 relative path levels, and a finite scanner timeout. Different roots
may fetch concurrently. Same-root speculative fetches are intentionally rejected
before provider I/O: under the current generation-bound coverage contract the
first commit invalidates observations taken at an older generation. Do not
silently rebase their generation or observed_at. Same-root pipelining/lineage
requires a separate reviewed contract and is NOT proved by multi-root speedup.

The caller owns durable ClaimWork and typed completion/recovery, with existing
Root->Watch->Work order and claimed-prefix semantics. Fetch never mutates any DB
row. Late signals remain pending; claim version alone is not stable across a
late Hint, so authorization uses claimed sequence, attempt and start timestamp.
Discard/cancel releases memory only, never marks work VERIFIED.

## Ordered durable commit fence

Persist optional version1 scoped_fetch_fence in existing Snapshot provenance:
captured generation/admission frontier, adapter digest AND PostgreSQL tuple xmin
(ABA revision), parent canonical ID/path, scope and exact durable claim token.
The xmin token is only a conservative fence for the120s observation lifetime,
not a permanent ID or an unbounded logical configuration epoch. Rewrites or tuple
maintenance may conservatively invalidate a fetch. Source timestamps use PG's
microsecond precision, never a synthetic later commit time. No secrets.
Validate it under the existing root lock both at short Stage1 admission and at
Stage2 reconcile, with adapter row locked against concurrent configuration writes,
then Watch/Work locks. A mismatch before admission leaves an inert DRAFT; a
mismatch after admission uses existing STALE_INPUT terminal handling, without
Canonical, removal evidence or Journal mutation. Legacy provenance is unchanged.
Malformed explicit fences fail closed. No caller-controlled wall clock authorizes
freshness: PostgreSQL time checks source observed_at against existing120s bound.

The original topology before/after evidence is immutable Collector output and
retains existing coverage semantics. Commit never performs a second source list.
No source snapshot can prove that the upstream did not change after observation.
Namespace/generation/claim changes are fenced, not turned into synthetic freshness.

## Extension and acceptance constraints

Test-only driver may overlap source reads and serially commit returned receipts.
It must report actual calls, errors, accepted/failed observations and retained
memory, not count SQL-preloaded directories as scanned. Revalidate source errors,
cancel, claim recovery, config ABA, late Hint and Snapshot ordering through real
PG18. Performance is fixture-specific and not a provider SLA. No runtime pool or
real115 concurrency until request budgets,429 backoff, scheduling fairness and F4
debt reporting are reviewed and implemented. No production canary by implication.
