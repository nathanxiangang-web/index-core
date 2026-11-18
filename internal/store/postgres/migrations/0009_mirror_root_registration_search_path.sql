-- Forward-only repair for databases that already applied 0007 (production
-- schema8). Migration 0007 is frozen at the content production executed:
-- PostgreSQL records applied migrations by name and never re-runs an edited
-- file. This replaces only the function body/attributes; the trigger created
-- by 0007 remains in place. SECURITY INVOKER is preserved (no DEFINER, no
-- PUBLIC grant, no ownership change).
--
-- Every relation reference is schema-qualified and the function pins a
-- caller-proof search_path: a session-local TEMP table with the same unqualified
-- name must never shadow the persisted budget relation.
CREATE OR REPLACE FUNCTION public.index_register_mirror_root() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO public.index_mirror_root_budget(root_id) VALUES(NEW.root_id)
        ON CONFLICT DO NOTHING;
    RETURN NEW;
END
$$;
