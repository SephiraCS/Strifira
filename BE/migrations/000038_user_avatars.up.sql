-- Phase 38: user avatars uploaded through the app (profile settings).
-- One avatar per user; users.avatar_url points at /api/avatars/<user_id>
-- which streams from the storage key recorded here. The version query
-- parameter in the URL busts caches when the avatar is replaced.
CREATE TABLE user_avatars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    storage_key TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
