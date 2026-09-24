-- P3 State Persistence Prototype: durable operational state.
--
-- Additive only. Creates exactly two operational tables; no Canonical table is
-- altered, and no trigger performs provider work. Applying this migration is
-- inert until a later authorized runtime component uses the Store methods.
--
-- Scope identity is (root_id, scope_key) with scope_key a normalized
-- root-relative directory path (root scope is exactly '/').

-- T13 index_scope_watch_state: recurring watch policy / cadence / next due.
CREATE TABLE index_scope_watch_state (
    root_id                    uuid NOT NULL REFERENCES index_root(root_id),
    scope_key                  text NOT NULL,

    watch_state                text NOT NULL CHECK (watch_state IN ('HOT','WARM','COLD','DISABLED')),
    cadence_class              text NOT NULL,
    effective_interval_seconds bigint NULL CHECK (effective_interval_seconds IS NULL OR effective_interval_seconds > 0),

    source_set                 text[] NOT NULL DEFAULT '{}'::text[],
    priority_class             text NOT NULL CHECK (priority_class IN ('URGENT','HIGH','NORMAL','LOW')),

    last_due_at                timestamptz NULL,
    last_attempt_started_at    timestamptz NULL,
    last_attempt_finished_at   timestamptz NULL,
    last_success_at            timestamptz NULL,
    next_due_at                timestamptz NULL,

    consecutive_failures       bigint NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_error_class           text NULL,
    deferred_until             timestamptz NULL,

    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    version                    bigint NOT NULL DEFAULT 1 CHECK (version >= 1),

    PRIMARY KEY (root_id, scope_key),

    -- P3 Sec 5 scope-key storage contract (defense in depth; Go validation is primary).
    CONSTRAINT c_sws_scope_key CHECK (
        scope_key = '/'
        OR (
            scope_key LIKE '/%'
            AND scope_key NOT LIKE '%/'
            AND scope_key NOT LIKE '%//%'
            AND scope_key NOT LIKE '%/./%'
            AND scope_key NOT LIKE '%/.'
            AND scope_key NOT LIKE '%/../%'
            AND scope_key NOT LIKE '%/..'
            AND scope_key !~ E'\\\\'
        )
    ),

    -- HOT/WARM are schedule classes; COLD/DISABLED carry no schedule truth.
    CONSTRAINT c_sws_schedule CHECK (
        (watch_state IN ('HOT','WARM')
            AND effective_interval_seconds IS NOT NULL
            AND next_due_at IS NOT NULL)
        OR
        (watch_state IN ('COLD','DISABLED')
            AND effective_interval_seconds IS NULL
            AND next_due_at IS NULL)
    ),

    -- Watch-policy provenance only (never POLL_SCHEDULE; that is dirty-work provenance).
    CONSTRAINT c_sws_source_set CHECK (
        source_set <@ ARRAY['OPERATOR_POLICY','ADAPTIVE_POLICY','MIGRATED','BACKSTOP_ENROLL']::text[]
    )
);

-- Deterministic due-watch selection for ACTIVE + HOT/WARM.
CREATE INDEX idx_scope_watch_due
    ON index_scope_watch_state (watch_state, next_due_at, root_id, scope_key)
    WHERE watch_state IN ('HOT','WARM') AND next_due_at IS NOT NULL;

-- T14 index_dirty_scope_work: durable, coalesced verification intent.
CREATE TABLE index_dirty_scope_work (
    root_id                    uuid NOT NULL REFERENCES index_root(root_id),
    scope_key                  text NOT NULL,

    work_state                 text NOT NULL CHECK (work_state IN
        ('PENDING','IN_FLIGHT','VERIFIED','RETRY_WAIT','BLOCKED','SUSPENDED')),
    signal_seq                 bigint NOT NULL CHECK (signal_seq >= 1),

    claimed_signal_seq         bigint NULL CHECK (claimed_signal_seq IS NULL OR claimed_signal_seq >= 1),
    claimed_source_set         text[] NULL,
    claimed_reason_set         text[] NULL,
    claimed_priority           text NULL CHECK (claimed_priority IS NULL OR claimed_priority IN ('URGENT','HIGH','NORMAL','LOW')),
    claimed_first_seen_at      timestamptz NULL,

    pending_source_set         text[] NOT NULL DEFAULT '{}'::text[],
    pending_reason_set         text[] NOT NULL DEFAULT '{}'::text[],
    pending_priority           text NULL CHECK (pending_priority IS NULL OR pending_priority IN ('URGENT','HIGH','NORMAL','LOW')),
    pending_first_seen_at      timestamptz NULL,
    pending_not_before         timestamptz NULL,

    last_seen_at               timestamptz NOT NULL,
    attempt_count              bigint NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    consecutive_failures       bigint NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    last_attempt_started_at    timestamptz NULL,
    last_attempt_finished_at   timestamptz NULL,
    last_error_class           text NULL,
    last_verified_at           timestamptz NULL,
    last_verified_signal_seq   bigint NULL,

    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    version                    bigint NOT NULL DEFAULT 1 CHECK (version >= 1),

    PRIMARY KEY (root_id, scope_key),

    CONSTRAINT c_dsw_scope_key CHECK (
        scope_key = '/'
        OR (
            scope_key LIKE '/%'
            AND scope_key NOT LIKE '%/'
            AND scope_key NOT LIKE '%//%'
            AND scope_key NOT LIKE '%/./%'
            AND scope_key NOT LIKE '%/.'
            AND scope_key NOT LIKE '%/../%'
            AND scope_key NOT LIKE '%/..'
            AND scope_key !~ E'\\\\'
        )
    ),

    CONSTRAINT c_dsw_claimed_le_signal CHECK (
        claimed_signal_seq IS NULL OR claimed_signal_seq <= signal_seq
    ),
    CONSTRAINT c_dsw_verified_le_signal CHECK (
        last_verified_signal_seq IS NULL OR last_verified_signal_seq <= signal_seq
    ),

    -- P2 bucket invariant: claimed_* present as one group iff IN_FLIGHT.
    CONSTRAINT c_dsw_claim_group CHECK (
        (work_state = 'IN_FLIGHT'
            AND claimed_signal_seq IS NOT NULL
            AND claimed_source_set IS NOT NULL AND cardinality(claimed_source_set) > 0
            AND claimed_reason_set IS NOT NULL
            AND claimed_priority IS NOT NULL
            AND claimed_first_seen_at IS NOT NULL)
        OR
        (work_state <> 'IN_FLIGHT'
            AND claimed_signal_seq IS NULL
            AND claimed_source_set IS NULL
            AND claimed_reason_set IS NULL
            AND claimed_priority IS NULL
            AND claimed_first_seen_at IS NULL)
    ),

    -- P2 bucket invariant: empty pending bucket => empty pending metadata.
    CONSTRAINT c_dsw_pending_empty CHECK (
        cardinality(pending_source_set) > 0
        OR (cardinality(pending_reason_set) = 0
            AND pending_priority IS NULL
            AND pending_first_seen_at IS NULL
            AND pending_not_before IS NULL)
    ),
    -- P2 bucket invariant: non-empty pending bucket => timestamped.
    CONSTRAINT c_dsw_pending_timestamped CHECK (
        cardinality(pending_source_set) = 0 OR pending_first_seen_at IS NOT NULL
    ),
    -- P2 bucket invariant: state-specific pending shape.
    CONSTRAINT c_dsw_state_shape CHECK (
        (work_state = 'VERIFIED' AND cardinality(pending_source_set) = 0)
        OR (work_state IN ('PENDING','RETRY_WAIT','BLOCKED','SUSPENDED') AND cardinality(pending_source_set) > 0)
        OR (work_state = 'IN_FLIGHT')
    ),
    -- P2: RETRY_WAIT requires an eligibility instant.
    CONSTRAINT c_dsw_retry_notbefore CHECK (
        work_state <> 'RETRY_WAIT' OR pending_not_before IS NOT NULL
    ),

    CONSTRAINT c_dsw_source_set CHECK (
        pending_source_set <@ ARRAY['POLL_SCHEDULE','MUTATION_HINT','MANUAL_OPERATOR','PROVIDER_EVENT','RECOVERY','FULL_VERIFY_BACKSTOP']::text[]
        AND (claimed_source_set IS NULL
             OR claimed_source_set <@ ARRAY['POLL_SCHEDULE','MUTATION_HINT','MANUAL_OPERATOR','PROVIDER_EVENT','RECOVERY','FULL_VERIFY_BACKSTOP']::text[])
    ),
    CONSTRAINT c_dsw_reason_set CHECK (
        pending_reason_set <@ ARRAY['POSSIBLE_CHANGE','DELETE_HINT','MOVE_UNCERTAIN','METADATA_UNCERTAIN','MANUAL_VERIFY','DRIFT_VERIFY','RETRY']::text[]
        AND (claimed_reason_set IS NULL
             OR claimed_reason_set <@ ARRAY['POSSIBLE_CHANGE','DELETE_HINT','MOVE_UNCERTAIN','METADATA_UNCERTAIN','MANUAL_VERIFY','DRIFT_VERIFY','RETRY']::text[])
    )
);

-- Deterministic eligible-work selection using the accepted pending fields.
CREATE INDEX idx_dirty_work_eligible
    ON index_dirty_scope_work (work_state, pending_not_before, pending_priority, pending_first_seen_at, root_id, scope_key)
    WHERE work_state IN ('PENDING','RETRY_WAIT');

-- Restart recovery scans persisted IN_FLIGHT rows.
CREATE INDEX idx_dirty_work_inflight
    ON index_dirty_scope_work (root_id, scope_key)
    WHERE work_state = 'IN_FLIGHT';