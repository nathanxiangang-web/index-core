-- Gate 2 rework R2-7: count DISTINCT independent observations. Record the most
-- recent accepted Snapshot already counted toward the removal-confirmation
-- count, so a replay/re-evaluation of one observation is never counted twice.
ALTER TABLE index_canonical_resource
    ADD COLUMN missing_last_snapshot_id uuid;