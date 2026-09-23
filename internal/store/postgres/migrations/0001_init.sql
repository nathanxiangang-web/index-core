-- Gate 2 PoC initial schema, derived from the FROZEN Gate 1C doc A
-- (GATE1C-POSTGRESQL-STORE.md, T1..T11). Enums are constrained text (CANDIDATE).
-- root_id/resource_id are Kernel-assigned and immutable/never reused (M3, C-R1).

-- T1 index_root
CREATE TABLE index_root (
    root_id              uuid PRIMARY KEY,
    scope_descriptor     jsonb NOT NULL,
    owning_collector_ref text,
    lifecycle_state      text NOT NULL CHECK (lifecycle_state IN ('NEW','ACTIVE','DEPRECATED','DELETED')),
    current_generation   bigint NOT NULL DEFAULT 0 CHECK (current_generation >= 0),
    latest_admission_seq bigint NOT NULL DEFAULT 0 CHECK (latest_admission_seq >= 0),
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- T2 index_generation
CREATE TABLE index_generation (
    root_id                  uuid NOT NULL REFERENCES index_root(root_id),
    generation_number        bigint NOT NULL CHECK (generation_number > 0),
    produced_by_snapshot_id  uuid,
    produced_by_admission_seq bigint,
    produced_at              timestamptz NOT NULL DEFAULT now(),
    summary                  jsonb NOT NULL,
    PRIMARY KEY (root_id, generation_number)
);

-- T3 index_canonical_resource
CREATE TABLE index_canonical_resource (
    resource_id                  uuid PRIMARY KEY,
    root_id                      uuid NOT NULL REFERENCES index_root(root_id),
    introduced_at_generation     bigint NOT NULL,
    last_confirmed_generation    bigint NOT NULL,
    resource_presence            text NOT NULL CHECK (resource_presence IN ('PRESENT','REMOVED')),
    removal_evidence_state       text NOT NULL CHECK (removal_evidence_state IN
                                  ('NONE','MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT','REMOVAL_CANDIDATE')),
    missing_since                timestamptz,
    consecutive_complete_missing integer NOT NULL DEFAULT 0 CHECK (consecutive_complete_missing >= 0),
    canonical_path               text,
    parent_resource_id           uuid,
    name                         text,
    is_dir                       boolean,
    size                         bigint,
    mtime                        timestamptz,
    content_hash                 text,
    hash_algorithm               text,
    content_type                 text,
    current_attributes           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at                   timestamptz NOT NULL DEFAULT now(),
    updated_at                   timestamptz NOT NULL DEFAULT now(),
    -- C-C4: hash and algorithm are present together or absent together.
    CONSTRAINT c_c4_hash_algorithm_pair CHECK ((content_hash IS NULL) = (hash_algorithm IS NULL)),
    -- C-C5: a REMOVED tombstone carries a terminal removal-evidence state and is retained.
    CONSTRAINT c_c5_removed_terminal CHECK (
        resource_presence <> 'REMOVED' OR removal_evidence_state IN
        ('MISSING_CONFIRMED_BY_COMPLETE_SNAPSHOT','REMOVAL_CANDIDATE'))
);
-- C-C3 is REJECTED: there is NO unique constraint on (root_id, canonical_path) (C-C3a);
-- multiple PRESENT rows MAY share a path (R8 path reuse / imposter).
CREATE INDEX i_c1_root_presence            ON index_canonical_resource (root_id, resource_presence);
CREATE INDEX i_c2_root_parent              ON index_canonical_resource (root_id, parent_resource_id);
CREATE INDEX i_c3_root_path                ON index_canonical_resource (root_id, canonical_path);
CREATE INDEX i_c4_root_removal_present     ON index_canonical_resource (root_id, removal_evidence_state)
                                           WHERE resource_presence = 'PRESENT';
CREATE INDEX i_c5_root_removal_missing     ON index_canonical_resource (root_id, removal_evidence_state, missing_since);

-- T4 index_identity_evidence_observation (append-only, authoritative)
CREATE TABLE index_identity_evidence_observation (
    observation_id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    resource_id                 uuid NOT NULL REFERENCES index_canonical_resource(resource_id),
    snapshot_id                 uuid NOT NULL,
    source_ref                  text,
    observed_at                 timestamptz NOT NULL,
    provider_object_id          text,
    provider_object_id_scope    text,
    provider_identity_assurance text NOT NULL CHECK (provider_identity_assurance IN
                                  ('STABLE_WITHIN_SCOPE','UNVERIFIED','UNSTABLE','UNAVAILABLE')),
    content_hash                text,
    hash_algorithm              text,
    observed_path               text,
    observed_parent_ref         text,
    size                        bigint,
    mtime                       timestamptz,
    is_dir                      boolean NOT NULL,
    extra_evidence              jsonb,
    -- C-E4: scope is present iff the provider object id is present.
    CONSTRAINT c_e4_id_scope_pair CHECK ((provider_object_id IS NULL) = (provider_object_id_scope IS NULL))
);
CREATE INDEX c_e3_resource_observed_at ON index_identity_evidence_observation (resource_id, observed_at);
CREATE INDEX c_e3_resource_snapshot    ON index_identity_evidence_observation (resource_id, snapshot_id);

-- T4b index_identity_evidence_current (projection/cache; NEVER authoritative)
CREATE TABLE index_identity_evidence_current (
    resource_id                 uuid PRIMARY KEY REFERENCES index_canonical_resource(resource_id),
    provider_object_id          text,
    provider_object_id_scope    text,
    provider_identity_assurance text,
    content_hash                text,
    hash_algorithm              text,
    observed_path               text,
    observed_parent_ref         text,
    size                        bigint,
    mtime                       timestamptz,
    is_dir                      boolean,
    derived_from_observation_id bigint,
    rebuilt_at                  timestamptz NOT NULL DEFAULT now()
);

-- T5 index_snapshot
CREATE TABLE index_snapshot (
    snapshot_id                    uuid PRIMARY KEY,
    root_id                        uuid NOT NULL REFERENCES index_root(root_id),
    provenance                     jsonb NOT NULL,
    observed_at                    timestamptz NOT NULL,
    started_at                     timestamptz,
    finished_at                    timestamptz,
    traversal_status               text NOT NULL CHECK (traversal_status IN ('SUCCESS','PARTIAL','FAILED','INTERRUPTED')),
    error_summary                  jsonb,
    skipped_scopes                 jsonb,
    skipped_scopes_known_empty     boolean NOT NULL DEFAULT false,
    freshness_evidence             text CHECK (freshness_evidence IS NULL OR freshness_evidence IN
                                     ('FRESH_DIRECT','FRESH_REFRESHED','CACHED_FRESH','STALE','UNKNOWN')),
    collector_completeness_assurance text CHECK (collector_completeness_assurance IS NULL OR collector_completeness_assurance IN
                                     ('STRONG_FAILURE_VISIBILITY','WEAK_FAILURE_VISIBILITY','UNKNOWN_FAILURE_VISIBILITY')),
    scope_shrink_corroboration     text CHECK (scope_shrink_corroboration IS NULL OR scope_shrink_corroboration IN
                                     ('NONE','CORROBORATED','CONTRADICTED')),
    completeness_flag              text NOT NULL CHECK (completeness_flag IN ('COMPLETE','PARTIAL')),
    acceptance_state               text CHECK (acceptance_state IS NULL OR acceptance_state IN
                                     ('COMPLETE','PARTIAL','FAILED','STALE','SUSPICIOUS')),
    lifecycle_state                text NOT NULL CHECK (lifecycle_state IN
                                     ('DRAFT','SUBMITTED','EVALUATED','RECONCILED','REJECTED','RETIRED')),
    entry_count                    bigint,
    byte_count                     bigint,
    generation_hint                text,
    created_at                     timestamptz NOT NULL DEFAULT now(),
    -- skipped_scopes: a confirmed-empty value must be [] and flagged; UNKNOWN is NULL.
    CONSTRAINT c_skipped_empty_flag CHECK (skipped_scopes_known_empty = false OR skipped_scopes = '[]'::jsonb)
);
CREATE INDEX i_s1_root_observed ON index_snapshot (root_id, observed_at);
CREATE INDEX i_s2_root_lifecycle ON index_snapshot (root_id, lifecycle_state);

-- T6 index_snapshot_entry (audit/replay; CANDIDATE persistence)
CREATE TABLE index_snapshot_entry (
    snapshot_id               uuid NOT NULL REFERENCES index_snapshot(snapshot_id),
    entry_local_id            text NOT NULL,
    name                      text NOT NULL,
    parent_ref                text NOT NULL,
    is_dir                    boolean NOT NULL,
    size                      bigint,
    mtime                     timestamptz,
    content_hash              text,
    hash_algorithm            text,
    provider_object_id        text,
    provider_object_id_scope  text,
    content_type              text,
    extra_evidence            jsonb,
    PRIMARY KEY (snapshot_id, entry_local_id)
);

-- T7 index_admission
CREATE TABLE index_admission (
    root_id            uuid NOT NULL REFERENCES index_root(root_id),
    admission_seq      bigint NOT NULL,
    snapshot_id        uuid NOT NULL,
    admitted_at        timestamptz NOT NULL DEFAULT now(),
    status             text NOT NULL CHECK (status IN ('PENDING','APPLIED','NOOP','REJECTED','STALE_INPUT','FAILED')),
    applied_generation bigint,
    claimed_by         text,
    claimed_at         timestamptz,
    lease_expires_at   timestamptz,
    PRIMARY KEY (root_id, admission_seq)
);
-- Absolute per-root FIFO: only the minimum PENDING admission_seq (head_seq) may be
-- claimed/processed/committed; higher sequences must never leapfrog (C-A5, RC1/RC4).
CREATE INDEX i_a1_root_pending ON index_admission (root_id, admission_seq) WHERE status = 'PENDING';

-- T8 index_applied_snapshot (append-only application history)
CREATE TABLE index_applied_snapshot (
    root_id                    uuid NOT NULL REFERENCES index_root(root_id),
    snapshot_identity_kind     text NOT NULL CHECK (snapshot_identity_kind IN ('REVISION_TOKEN','DETERMINISTIC_DIGEST')),
    snapshot_identity_namespace text NOT NULL,
    snapshot_identity_version  text NOT NULL,
    snapshot_identity_value    text NOT NULL,
    snapshot_id                uuid NOT NULL,
    applied_generation         bigint NOT NULL,
    applied_admission_seq      bigint NOT NULL,
    applied_at                 timestamptz NOT NULL DEFAULT now(),
    -- C-AS1: one row per logical identity per post-application generation.
    PRIMARY KEY (root_id, snapshot_identity_kind, snapshot_identity_namespace,
                 snapshot_identity_version, snapshot_identity_value, applied_generation)
);

-- T9 index_journal_event (append-only)
CREATE TABLE index_journal_event (
    root_id              uuid NOT NULL REFERENCES index_root(root_id),
    event_seq            bigint NOT NULL CHECK (event_seq > 0),
    event_id             bigint GENERATED ALWAYS AS IDENTITY,
    generation_number    bigint NOT NULL,
    intra_generation_seq integer NOT NULL CHECK (intra_generation_seq > 0),
    event_type           text NOT NULL CHECK (event_type IN
                           ('resource-added','resource-updated','resource-renamed','resource-moved',
                            'resource-removed','root-deprecated','root-deleted')),
    resource_id          uuid,
    payload              jsonb NOT NULL,
    committed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (root_id, event_seq),
    CONSTRAINT c_j2_intra_unique UNIQUE (root_id, generation_number, intra_generation_seq)
);
CREATE INDEX i_j3_root_generation ON index_journal_event (root_id, generation_number);

-- Append-only enforcement (doc D Sec 7.2). Role revocation is the primary mechanism in
-- a real deployment; this trigger is the CANDIDATE defense-in-depth that makes the
-- property verifiable in the PoC.
CREATE OR REPLACE FUNCTION index_reject_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'append-only table % rejects %', TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER index_journal_event_append_only
    BEFORE UPDATE OR DELETE ON index_journal_event
    FOR EACH ROW EXECUTE FUNCTION index_reject_mutation();

CREATE TRIGGER index_identity_evidence_observation_append_only
    BEFORE UPDATE OR DELETE ON index_identity_evidence_observation
    FOR EACH ROW EXECUTE FUNCTION index_reject_mutation();

-- T10 index_root_config (per-root policy)
CREATE TABLE index_root_config (
    root_id                         uuid PRIMARY KEY REFERENCES index_root(root_id),
    removal_grace_period            interval NOT NULL,
    move_recognition_horizon        interval NOT NULL,
    min_consecutive_complete_missing integer NOT NULL DEFAULT 1 CHECK (min_consecutive_complete_missing >= 1),
    min_independent_confirmations   integer NOT NULL DEFAULT 1 CHECK (min_independent_confirmations >= 1),
    -- C-RC1: frozen relationship from Gate 1B Domain Sec 2.4.
    CONSTRAINT c_rc1_grace_ge_horizon CHECK (removal_grace_period >= move_recognition_horizon)
);

-- T11 index_reconcile_result
CREATE TABLE index_reconcile_result (
    reconcile_id      uuid PRIMARY KEY,
    root_id           uuid NOT NULL,
    admission_seq     bigint NOT NULL,
    generation_number bigint,
    outcome           text NOT NULL CHECK (outcome IN ('RECONCILED','NOOP','REJECTED','STALE_INPUT','FAILED')),
    counts            jsonb NOT NULL DEFAULT '{}'::jsonb,
    conflicts         jsonb,
    rejection_reason  text,
    created_at        timestamptz NOT NULL DEFAULT now()
);