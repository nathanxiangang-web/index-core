-- Register once per Root, before any Watch can reference it. This deliberately
-- includes Roots without managed Watches: O(Roots) metadata avoids a Watch scan
-- or a missed first enrollment after an actor has finished startup.
-- The trigger performs operational bookkeeping only, no provider/Kernel work.
-- Every relation reference is schema-qualified and the function pins a
-- caller-proof search_path: a session-local TEMP table with the same unqualified
-- name must never shadow the persisted budget relation (SECURITY INVOKER).
CREATE FUNCTION public.index_register_mirror_root() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, public
AS $$
BEGIN
    INSERT INTO public.index_mirror_root_budget(root_id) VALUES(NEW.root_id)
        ON CONFLICT DO NOTHING;
    RETURN NEW;
END
$$;
CREATE TRIGGER index_register_mirror_root AFTER INSERT ON public.index_root
    FOR EACH ROW EXECUTE FUNCTION public.index_register_mirror_root();

-- Migrations run with the writer stopped. Installing the trigger and backfill
-- in the same migration transaction also closes the concurrent-insert gap.
-- Never overwrite existing cursors/reservations during upgrade or retry.
INSERT INTO public.index_mirror_root_budget(root_id)
    SELECT root_id FROM public.index_root ORDER BY root_id ON CONFLICT DO NOTHING;