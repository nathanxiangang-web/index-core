-- Preserve the full authoritative prior and observation_id ordering. A single
-- ordered probe avoids filtered global-PK walks or per-resource sorting. No INCLUDE of
-- potentially large provider values; the latest row is fetched from the heap.
CREATE INDEX idx_identity_resource_latest
    ON index_identity_evidence_observation(resource_id, observation_id DESC);
