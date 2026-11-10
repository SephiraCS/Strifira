-- Chat: Discord/Slack-style realtime channels and messages.
--
-- chat_channels holds public channels (open to every workspace member),
-- private channels (membership in chat_channel_members) and direct messages
-- (type 'dm', exactly two members, deduplicated by dm_key).
--
-- chat_messages is append-mostly: edits rewrite body/edited_at in place and
-- deletes keep a tombstone row (deleted_at) so the timeline of other clients
-- can be reconciled by the realtime events.

CREATE TABLE chat_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- 'public' | 'private' | 'dm'
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    topic TEXT NOT NULL DEFAULT '',
    -- Sorted "<userA>:<userB>" pair identifying a direct message; NULL for
    -- channels. Unique so a DM between two users is a single row.
    dm_key TEXT UNIQUE,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_chat_channels_workspace ON chat_channels(workspace_id, name);
-- Channel names are unique per workspace (DMs are named by their dm_key
-- pair instead). Auto-provisioning the default #general channel races
-- across requests; the unique index turns the loser into a clean no-op.
CREATE UNIQUE INDEX idx_chat_channels_name ON chat_channels(workspace_id, name)
    WHERE type <> 'dm';

CREATE TABLE chat_channel_members (
    channel_id UUID NOT NULL REFERENCES chat_channels(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX idx_chat_channel_members_user ON chat_channel_members(user_id);

CREATE TABLE chat_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id UUID NOT NULL REFERENCES chat_channels(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id),
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    edited_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_chat_messages_channel ON chat_messages(channel_id, created_at DESC);
