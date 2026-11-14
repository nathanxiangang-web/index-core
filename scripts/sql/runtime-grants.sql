-- EXPLICIT operator action, after schema8 migration, using the table owner.
-- psql -v ON_ERROR_STOP=1 -v runtime_role=indexcore_runtime \
--   -v runtime_database=indexcore -f runtime-grants.sql
-- Substitute only the reviewed role identifier; this creates no role/password.
BEGIN;
GRANT CONNECT ON DATABASE :"runtime_database" TO :"runtime_role";
GRANT USAGE ON SCHEMA public TO :"runtime_role";
GRANT SELECT ON TABLE public.schema_migrations TO :"runtime_role";
GRANT SELECT, INSERT ON TABLE
    public.index_generation, public.index_snapshot_entry,
    public.index_identity_evidence_observation, public.index_applied_snapshot,
    public.index_journal_event, public.index_reconcile_result
    TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON TABLE
    public.index_root, public.index_canonical_resource,
    public.index_identity_evidence_current, public.index_snapshot,
    public.index_admission, public.index_root_config,
    public.index_root_adapter_config, public.index_scope_watch_state,
    public.index_dirty_scope_work, public.index_mirror_root_budget
    TO :"runtime_role";
COMMIT;
-- No ownership, CREATE, DELETE, TRUNCATE, PUBLIC grants or blanket future grants.
-- PostgreSQL identity columns use the table INSERT privilege; no direct nextval.
-- CONNECT on the chosen database must also be granted by its owner if absent.
