-- OPTIONAL profile, in addition to runtime-grants.sql, only for a reviewed F4
-- configuration. Applying privileges does not enable F4 or alter its defaults.
BEGIN;
GRANT SELECT, UPDATE ON TABLE public.index_mirror_budget TO :"runtime_role";
GRANT SELECT, INSERT, UPDATE ON TABLE public.index_mirror_watch_binding TO :"runtime_role";
COMMIT;
