# PR148 + PR149: complete-chain review and runtime permission closure

State: REVIEW, not independent architecture acceptance, merge or deployment.
Reviewed main `9a33d0113ea8d6ddd1f66834ba7a781e5007fe3e` through PR148
`9841cca43ecd10d675b0985449783b0ab9b3bbe8` and PR149
`c419459e0a098cff172ce1ab6460d96a7fab4129`, plus this follow-up commit.
Both original heads had successful main/F4 CI. Both PRs remain unmerged.
PR146 operating documents were read from19ba65f, not assumed present on main
and not incorporated. One executor, no second writer or production connection.

## Finding and correction

FIX: all previous database fixtures were table-owner connections. Successful
Root registration there did not prove an invoker-rights trigger would work under
a separate runtime LOGIN. Doctor/serve checked Ping and schema only, allowing a
schema8 deployment to appear ready despite missing budget INSERT/UPDATE grants.

`store/postgres/runtime_privileges.go` now provides a bounded, read-only catalog
preflight. Doctor and serve check the configured writer account before startup;
serve has not acquired the writer lock, started actors, bound listeners or issued
HTTP when this check fails. No automatic GRANT, role switch or migration occurs.
The failure lists only fixed relation/privilege names, no DSN/password/arguments.

The base writer profile covers the existing Kernel/Store operations and schema8
Root registration/completion. Even with F4 OFF, Root creation needs budget INSERT
and successful completion can need budget SELECT/UPDATE. The ON profile adds
global ledger SELECT/UPDATE and binding SELECT/INSERT/UPDATE. OFF does not demand
unused binding/global-ledger grants. These are table-level deployment profiles;
column-limited ACLs require a separate reviewed profile, not a false doctor PASS.

The exact transactional recipes are
[`runtime-grants.sql`](../../scripts/sql/runtime-grants.sql) and
[`mirror-rolling-grants.sql`](../../scripts/sql/mirror-rolling-grants.sql).
The test fixture executes these same recipes, replacing psql identifier variables
with pgx-quoted test role/database identifiers. No PUBLIC, blanket future-table,
ownership, schema CREATE, DELETE, TRUNCATE or SECURITY DEFINER grants are added.
Identity-column inserts succeed without direct sequence USAGE/UPDATE grants.

## Functional evidence and minimum account

`testutil.Issue138RestrictedLogin` creates a unique real LOGIN only after the
existing exact-loopback/PG18/purpose-marker/owner guard, with NOSUPERUSER,
NOCREATEDB, NOCREATEROLE, NOINHERIT, NOREPLICATION and NOBYPASSRLS. It opens a new
connection with that identity: current_user=session_user differs from the table
owner. It never SET ROLEs an owner connection. Credentials are generated only for
the disposable fixture and never logged; grants/role are cleaned up afterward.

| Predicate | Actual regression |
| --- | --- |
| Only Root INSERT, no budget INSERT | SQLSTATE42501; Root and budget both absent |
| Add only budget INSERT | Same connection/retry creates Root+budget with F4 OFF; no budget SELECT/UPDATE needed by the trigger |
| Duplicate and concurrent registrations | Eight additional concurrent Roots; one budget each; duplicate Root rejected, no duplicate budget |
| State protection | Registration-only role cannot UPDATE/DELETE/TRUNCATE budgets; full writer recipe has no DELETE/TRUNCATE |
| Migrator/runtime separation | Actual non-owner LOGIN executes F4-off Fake HTTP→Snapshot→serial Kernel→Q5/Q8, then F4 admission/binding/verification |
| Missing grant before service | Doctor and serve reject budget INSERT revocation; no source request |
| OFF/ON grant profiles | OFF succeeds without binding/global grants, ON fails until explicit optional grants are supplied |
| Trigger security | pg_proc.prosecdef=false; caller rights retained |

`TestIssue134RollingRestrictedRootRegistration`,
`TestIssue134RollingRuntimeGrantProfiles`, and
`TestIssue134RollingRuntimeAccountPreflight` exercise these paths on real PG18.

## Whole-chain architecture audit

| Boundary | Review and retained evidence |
| --- | --- |
| 0007 install/backfill | One migration transaction; CREATE TRIGGER's relation lock blocks concurrent Root INSERT until commit. A new deterministic pg_stat_activity lock barrier proves the window is closed; old and new Roots both have budgets afterward. |
| Partial upgrade | Inject an0008 index-name conflict after committed0007. Only0008 remains missing; correction and migrate retry preserve both budget records. No historical migration edits. |
| Root lock order | New Root row→operational budget in its creation transaction. Normal F4 paths retain Root→adapter→Watch→binding→Work, with global ledger last; maintenance releases Root transactions before its global cursor update. |
| Durable state | ON CONFLICT DO NOTHING never resets cursor/reservation; no budget DELETE or wake-time backfill. Existing restart/registration-failure/global-atomicity/multi-Root tests retained. Manual owner tampering is outside the automatic recovery contract. |
| OFF→drain→ON | Success releases only same Root/scope and outstanding_seq<=verified. Claim/version guards run first. Newer Hint survives; failed/cancelled/timed-out/lost claims retain intent. Global next_admit_at is never reset. |
| Watch lifecycle | Renewal/rebind/maintenance require rolling policy. OFF completion acknowledges old reservation only. Retirement still requires identity/config/version and accepted Snapshot/Journal anchors, with no unresolved intent or path replacement. |
| HTTP and readers | Existing bounded read pool reused; background governor adds a class ceiling within total global/Root/provider limits.429 rules and serial Canonical commit lane remain. No default or Reader change. |
| Whole-Root Prior | SQL/context unchanged; existing Stage2 slice is reused.0008 indexes resource/latest observation_id, not observed_at/current projection. Evidence-only visibility and exact before/after values remain tested; no generation-only cache. |
| Observability | Bounded redacted SQL fingerprints; no raw parameters. Client elapsed/rows are not server CPU or HTTP counts. HTTP observer is not yet exported as live production aggregates. |

No Kernel/Canonical/Snapshot/Journal authority change or new writer is proposed.
Root candidates still scale with Root count, leases are not physical garbage
collection, and growing offered freshness debt cannot be hidden by bounded Work.
Supported bounded correctness cases pass; arbitrary churn or permanently blocked
business work is not claimed starvation-free by a finite test.

## Deployment unit and residual risks

Schema5→8 upgrade, owner-schema8 backup/restore and compatible schema5 whole-DB
rollback were exercised in the preceding isolated receipts. This follow-up
preserves migration0001..0008 bytes. No new schema version is introduced.
0008 is ordinary transactional CREATE INDEX, requiring a writer-stopped window,
disk/WAL and lock-timeout planning. Real build cost remains unmeasured.

Apply ACL recipes as the correct table/database owner or grant-option role after
migration. Run doctor with the **actual runtime DSN and planned flags**, not the
migration DSN. Serve repeats the check, but this is a point-in-time ACL preflight:
later revocation, RLS policy changes, unavailable credentials or operator schema
tampering can still fail operations. The supported schema has no RLS policies.
Do not grant owner membership or bypass guards to make preflight green.

Production account identity/ACL/role membership/connectivity are **NOT_VERIFIED**:
this task explicitly prohibits connecting to production. Isolated non-owner
evidence plus runtime-account preflight closes the development mechanism, not
the future operator's account-verification step. Whole-DB backups do not replace
cluster-role/credential/database-ACL recovery; restore those through the approved
secret/role procedure before restoring object ACLs and running runtime doctor.

Original PR148 and PR149 plus this fix form the reviewed source unit; the final
PR149 head and CI receipt identify it precisely. No PR is self-merged or stamped
independently accepted. Separate authorization is required for deployment/F4.
20K, long stability, real HTTP throughput, resource costs and freshness-debt
convergence remain NOT_RUN_POST_DEPLOYMENT and are not current functional gates.

## Final isolated verification

All final checks passed on the same Go/SQL hashes, source manifest
`6ee1f2e8bb04c29003f4d894e7431ed59996c4532b8620f727d3472d56eb389d`:
full PG18:777 pass /129 disclosed skips; Runtime/sourcebudget Race:111/51;
F4 purpose138 Race count3:129 pass /3 optional replay skips; related Issue134
Race:55/17; Vet/Build PASS. Counts are test/subtest events, not unique methods.
No DATA RACE report. The purpose-gated role tests run in the dedicated F4 job.
The new doctor regression fails on the unchanged c419 parent, then passes here.

An additional owned schema8 custom-format dump/restore exercised the actual psql
identifier-variable recipes and restored object ACLs. Business-table fingerprints
and effective runtime table privileges matched; the restored non-owner LOGIN
created a Root/budget and passed doctor. Temporary test LOGIN/databases were
removed. Database/cluster role recovery remains a separate real deployment step.
[Raw checks and hashes](evidence/issue134-review148149-manifest-20261115.json).

After this isolated review, the Owner separately authorized conditional public
server replacement when the exact-head review/CI has no blocking defect. This
is authorization, not evidence of cutover. The deployment must have its own
actual-instance, backup/restore, migration, binary, flags and post-start receipt;
no merge or F4 enablement is implied. Real capacity/soak remain separate stages.
