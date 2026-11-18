-- READ ONLY. Execute as the actual runtime LOGIN, not the migration owner.
SELECT current_user AS runtime_role, session_user AS authenticated_role,
       current_database() AS database,
       has_schema_privilege(current_user,'public','USAGE') AS schema_usage,
       has_table_privilege(current_user,'public.index_root','INSERT') AS root_insert,
       has_table_privilege(current_user,'public.index_mirror_root_budget','INSERT') AS budget_insert,
       has_table_privilege(current_user,'public.index_mirror_root_budget','SELECT') AS budget_select,
       has_table_privilege(current_user,'public.index_mirror_root_budget','UPDATE') AS budget_update;
SELECT c.relname, pg_get_userbyid(c.relowner) AS table_owner
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relname IN ('index_root','index_mirror_root_budget');
SELECT p.prosecdef AS security_definer
  FROM pg_proc p WHERE p.oid='public.index_register_mirror_root()'::regprocedure;
