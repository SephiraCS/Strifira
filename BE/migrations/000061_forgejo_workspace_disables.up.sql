-- A row pauses the Forgejo integration for a workspace entirely: it wins over
-- both the per-workspace connection and the global env configuration. The
-- connection row itself is kept; enabling removes this marker again.
CREATE TABLE forgejo_workspace_disables (
    workspace_id     UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    disabled_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
