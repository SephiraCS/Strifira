-- Phase 39: workspace logo files uploaded through workspace settings,
-- mirroring user_avatars (000038). workspaces.logo_url points at
-- /api/workspace-avatars/<workspace_id>; the ?v= query busts caches on
-- replacement.
CREATE TABLE workspace_avatars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL UNIQUE REFERENCES workspaces(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
