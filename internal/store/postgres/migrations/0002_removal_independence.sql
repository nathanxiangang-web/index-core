-- Gate 2 rework B2: prove independence of the removal confirmation.
-- Store-internal realization (not an external Domain semantic): record the
-- accepted Snapshot that first produced MISSING evidence, so a later,
-- independently admitted Snapshot can be shown to have confirmed removal (V2c).
ALTER TABLE index_canonical_resource
    ADD COLUMN missing_first_snapshot_id uuid;
-- Gate 2 rework B3: normalized SnapshotEntry may carry an adapter-supplied
-- provider identity assurance capability hint (nullable; absent = UNVERIFIED).
ALTER TABLE index_snapshot_entry
    ADD COLUMN provider_identity_assurance text;
