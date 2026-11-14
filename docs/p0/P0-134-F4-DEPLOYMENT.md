# F4 deployment preparation and separate real-data validation

Status: preparation only. No production connection, schema migration, queue
mutation, F4 enablement or provider request is authorized by this document.
Independent review and a separate deployment authorization are still required.

## Migration and compatibility

0001..0006 are unchanged. From main schema5, explicitly apply0006 operational
tables,0007 Root registration/backfill,0008 latest-observation index. From the
PR148 lab schema6, apply only0007/0008. Each migration is transactional and
recorded only after successful commit; failure of0008 may leave committed0007,
so repair the cause and rerun the same binary's migrate before starting serve.

0007 holds the Root-table DDL lock and scans Roots once; no Watch-table scan.
0008 uses ordinary CREATE INDEX inside its migration transaction: it can block
evidence writes and needs index build disk/WAL/time. Schedule a writer-stopped
maintenance window; no CONCURRENTLY inside the existing migration transaction.
Inspect real history/table/index size and free space read-only before authorizing
that window. Real build time/storage are NOT_TRACKED in this development stage.

Startup `doctor` and `serve` Ping the DB and require an exact known schema;
serve never auto-migrates. Schema5/6 is rejected by the schema8 candidate; the
old schema5 binary and PR148 schema6 binary reject schema8 even if F4 is off.
No mixed old/new serving instances. Root creation runs the registration trigger
with invoker privileges; grant the application the required operational table
INSERT privilege through the reviewed deployment role, not SECURITY DEFINER.

## Runtime account and exact GRANT recipes

The migration identity owns/changes schema; the runtime LOGIN executes the
application. Do not use a successful migrator doctor as evidence for a different
runtime account. Schema8's invoker trigger requires budget INSERT even with F4
OFF; successful covered completion also requires its SELECT/UPDATE privileges.
No SECURITY DEFINER or runtime owner membership is needed.

After migration, an authorized owner/grant-option role applies the reviewed
recipes to an existing, deliberately chosen runtime role/database:

```sh
# Credentials come from the approved secret/connection mechanism, never literals.
psql -X -v ON_ERROR_STOP=1 -v runtime_role=indexcore_runtime \
  -v runtime_database=indexcore -f scripts/sql/runtime-grants.sql
# Optional additional rights for a separately reviewed F4 ON configuration:
psql -X -v ON_ERROR_STOP=1 -v runtime_role=indexcore_runtime \
  -f scripts/sql/mirror-rolling-grants.sql
```

The scripts grant CONNECT, schema USAGE and only explicit SELECT/INSERT/UPDATE
operations used by the existing writer. They do not create a role, supply a
password, grant PUBLIC/ownership/CREATE/DELETE/TRUNCATE, alter default privileges,
enable F4 or change Reader/source budgets. No blanket privileges on future
tables. Review inherited/public/default ACLs separately; a GRANT script does not
remove pre-existing excessive privileges. Roles/credentials and database-level
ACLs need their own approved restore procedure; ordinary whole-DB dump is not a
cluster-role backup. A runtime role must not run migrate or own schema objects.

Run the read-only [registration probe](../../scripts/sql/check-runtime-registration.sql)
using the runtime identity. It explicitly reports current_user/session_user,
table owners, has_table_privilege for Root/budget and the trigger's invoker flag.
Then run `indexcore doctor` with the **same injected runtime DSN and planned
configuration**. Doctor checks the full table-level writer profile; ON also
checks the global ledger and binding profile. Serve repeats it before taking the
writer lock or exposing listeners. Missing grants fail before work/source I/O;
there is no automatic GRANT, test Root insert, temporary owner switch or repair.

Real non-owner LOGIN fixtures execute these exact recipes and actual OFF/ON
Runtime/Kernel paths. Actual production account/ACL/connectivity remain
NOT_VERIFIED because this task does not connect to production. Preflight is a
point-in-time catalog check, not immunity to later REVOKE/RLS/schema changes.

## Controlled upgrade, interruption and rollback

For a separately authorized deployment:

1. Record old binary/image SHA, schema versions, flag values and queue/admission/
   generation/Journal anchors. Preserve existing business tasks; do not reset.
2. Stop the sole writer gracefully; verify it exited and the writer lock is free.
   Hold ingress as needed for the approved maintenance window. Do not start a
   second writer while waiting for shutdown.
3. Take a consistent whole-database PostgreSQL18 custom-format backup, protected
   from credential leakage. Include the required roles/privileges separately.
   Hash it; restore to an isolated PG18 database with errors fatal; compare
   schema, Canonical IDs, generations, Journal sequence, Snapshot/admission and
   Work/Watch/budget state. Keep production DSNs out of shell history/logs.
4. With writer stopped and backup verified, run the new binary's explicit migrate
   under bounded lock/statement timeouts suitable for the approved window.
   Run doctor; verify schema8, trigger/index, Root/budget coverage and unchanged
   existing reservation/queue anchors. A failure is a failed cutover, not a reason
   to edit historical migrations or clear operational data.
5. Start the single new binary using reviewed existing settings, F4 default OFF,
   existing Reader count/intervals. Check readiness, Runtime startup/recovery and
   normal task completion. Enable F4 only if separately authorized with explicit
   budgets and rollback/observation criteria.

Prefer rollback by stopping F4/F2 with the **same new binary/schema** and draining
durable work normally. The verified-success path now frees old reservations even
with F4 off. Failure/cancellation retains intent; never manually clear reservations
to manufacture progress. Global admission time survives off/on and restart.
There are no down migrations. Old-binary rollback requires a verified **whole DB
restore** to the old compatible schema, with an explicit plan for writes after
the backup and the corresponding approved recovery-point tradeoff. Dropping0006
tables or deleting migration markers is not rollback.

The isolated functional rehearsal built the exact main9a33d011 binary, created
its real schema5, loaded only fake business data (including eight Journal events
and one pending Hint-shaped Work), and took/restored a whole custom-format
backup. Schema5→8 migration preserved every original business-table fingerprint
and pending Work; the main binary rejected schema8, and accepted the separately
restored schema5 backup. A second schema8 backup/restore preserved all table
fingerprints, including existing budget/binding metadata. Receipts and dump
hashes are in this phase's evidence; no production backup or restore was run.

The development backup/restore exercise uses only an owned disposable fixture;
it is not proof of production backup size, role/extension compatibility, timing
or RPO. An OS crash or migration error leaves PostgreSQL transaction recovery in
control; restart the matching binary only after schema doctor succeeds.

## Flags, Watch lifecycle and existing tasks

`INDEXCORE_MIRROR_ROLLING_PERCENT=0` is the default;1..50 requires hybrid Runtime
and a positive F2 interval. Reader count and total HTTP global/Root/provider
limits are unchanged. Root registration occurs at schema level even with F4 off;
an idle budget row does not create Work, bind a Watch or issue HTTP.

F4 off/F2 on uses legacy revisit scheduling for WARM Watches. It does not maintain
F4 bindings and can reintroduce legacy offered-load amplification. F4 and F2 both
off stop new managed polls; already committed work remains executable under the
normal queue/recovery rules. Neither setting deletes Watch/history/debt or
restores COLD auto-retirements. Re-enable on the same schema retains reservations
and cursors; successful off-mode completion now releases covered reservations.
After retirement, prefer keeping F2 off during rollback rather than blindly
falling back to legacy F2. Operator-owned COLD/DISABLED policy remains protected.

Watch expiry records EXPIRED; it is not deletion evidence and does not stop
service. Retirement still needs accepted Kernel Snapshot/Journal anchors and
identity/adapter/version guards. No canonical or Journal mutation was added.
Legacy POLL backlog is retained, outside the new F4 reservation ledger; observe
its natural drain rather than claiming an instantaneous global one-poll bound.

## Observability and deployment-stage acceptance

Existing redacted Runtime logs cover startup, recovered inflight work, cycle
outcomes, failure classes and shutdown. Optional `--runtime-query-costs` logs
bounded SQL fingerprints/calls/rows/errors/client microseconds on orderly
shutdown. It is not a live dashboard, server CPU accounting, pool-wait metric or
a guarantee of logs after SIGKILL. The HTTP governor's bounded observer reports
actual RoundTrip attempts in tests; production wiring does not currently export
those aggregates. Production client starts/429/timeouts are **NOT_TRACKED** until
a separately authorized bounded client/egress observation is configured. Receiver
access counts differ from exact client wire starts. Never infer HTTP from PENDING.

Use read-only pg_stat_activity/locks/stat_database and container/host sampling
under a documented cost budget. Record observation-query overhead separately.
Whole-Root fingerprint `67536c1fdbc15337` remains unchanged; compare its actual
rows/client time, and use reviewed EXPLAIN during maintenance rather than running
EXPLAIN ANALYZE repeatedly against live writes. Do not log DSNs, SQL parameters,
provider paths, tokens or raw provider errors. Resource counters not captured
must remain NOT_TRACKED.

After explicit deployment/run authorization, conduct independent stages:

| Stage | Required real-data observations | Current verdict |
| --- | --- | --- |
| Basic operation | Preserve tasks/data; startup/claim/completion, Q5/Q8 and actual file/dir discovery, DB/HTTP/SQL costs | NOT_RUN_POST_DEPLOYMENT |
| Gradual20K scale | Discovery→Claim/visible P50/P95/P99, NORMAL waits including censoring, duplicates, Root fairness, actual requests/status/start rate, Watches/retirements and freshness debt, DB CPU/RSS/locks/pool/queries | NOT_RUN_POST_DEPLOYMENT |
| Long stability | Queue/debt convergence, no permanent hang/leak, restart recovery, Watch renewal/expiry/retirement, stable new-file Q5/Q8 and business throughput | NOT_RUN_POST_DEPLOYMENT |

Use existing real directories/tasks; no queue reset, data wipe or artificial
business mutation. Set business acceptance windows/budgets before each stage,
and distinguish uncensored observations from timed-out/censored cohorts.
Growing debt/amplification is a capacity NO-GO for that operating point, not a
PASS hidden by low queue counts. Choose later concurrency/budget changes from
measured data with separate authorization. Historical capacity NO-GO remains
historical; missing deployment-stage data does not block functional repair delivery.
