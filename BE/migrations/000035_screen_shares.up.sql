-- Phase 35: Screen sharing sessions backed by LiveKit rooms.
-- A share is a standalone workspace-scoped live session: one presenter
-- publishes their screen (with captured tab/system audio); viewers join
-- the LiveKit room with subscribe-only tokens. No issue linkage.
CREATE TABLE screen_shares (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    started_by UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    room_name TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_at TIMESTAMPTZ,
    CONSTRAINT screen_shares_status_check CHECK (status IN ('active', 'ended'))
);

CREATE INDEX idx_screen_shares_workspace_status ON screen_shares(workspace_id, status);
CREATE INDEX idx_screen_shares_started_by ON screen_shares(started_by);
