-- Gate 3 P3: provider-neutral root adapter binding. rclone-specific remote/path
-- data lives in the runtime adapter configuration, never in Domain semantics.
CREATE TABLE index_root_adapter_config (
    root_id        uuid PRIMARY KEY REFERENCES index_root(root_id),
    collector_kind text NOT NULL,
    config         jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now()
);