-- Operational scheduling metadata only. No canonical, admission or Journal
-- contract changes; explicit migration, inert while rolling remains OFF.
CREATE TABLE index_mirror_budget (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    next_admit_at timestamptz NOT NULL DEFAULT '-infinity',
    root_cursor text NOT NULL DEFAULT '',
    maintenance_root text NOT NULL DEFAULT '',
    maintenance_scope text NOT NULL DEFAULT '',
    emitted bigint NOT NULL DEFAULT 0,
    deferred bigint NOT NULL DEFAULT 0
);
INSERT INTO index_mirror_budget(singleton) VALUES(true);

CREATE TABLE index_mirror_root_budget (
    root_id uuid PRIMARY KEY REFERENCES index_root(root_id),
    scope_cursor text NOT NULL DEFAULT '',
    outstanding_scope text,
    outstanding_seq bigint CHECK (outstanding_seq > 0),
    CHECK ((outstanding_scope IS NULL) = (outstanding_seq IS NULL))
);

CREATE TABLE index_mirror_watch_binding (
    root_id uuid NOT NULL,
    scope_key text NOT NULL,
    resource_id uuid NOT NULL REFERENCES index_canonical_resource(resource_id),
    canonical_path text NOT NULL,
    adapter_digest text NOT NULL,
    adapter_revision text NOT NULL,
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    lifecycle text NOT NULL CHECK (lifecycle IN ('ACTIVE','EXPIRED','RETIRED')),
    lease_expires_at timestamptz NOT NULL,
    freshness_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    last_observed_at timestamptz,
    retired_watch_version bigint,
    retired_event_seq bigint,
    retired_reason text,
    binds bigint NOT NULL DEFAULT 1,
    renewals bigint NOT NULL DEFAULT 0,
    expirations bigint NOT NULL DEFAULT 0,
    retirements bigint NOT NULL DEFAULT 0,
    PRIMARY KEY(root_id,scope_key),
    FOREIGN KEY(root_id,scope_key) REFERENCES index_scope_watch_state(root_id,scope_key),
    CHECK ((lifecycle='RETIRED') = (retired_watch_version IS NOT NULL AND retired_event_seq IS NOT NULL))
);

CREATE INDEX idx_mirror_managed_round ON index_scope_watch_state(root_id,scope_key)
    INCLUDE(next_due_at,version,deferred_until)
    WHERE watch_state='WARM' AND cadence_class='MIRROR_REVISIT'
      AND source_set=ARRAY['BACKSTOP_ENROLL']::text[] AND priority_class='NORMAL';
