-- Phase 34: Personal API keys for MCP access.
-- Tokens are random secrets shown once at creation; only their SHA-256
-- hash is stored. Scopes: 'read' (query tools) and 'write' (mutations).
CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    token_hash CHAR(64) NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    scopes TEXT[] NOT NULL DEFAULT '{read}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT api_keys_scopes_check CHECK (scopes <@ ARRAY['read','write']::text[] AND 'read' = ANY (scopes))
);

CREATE INDEX idx_api_keys_user ON api_keys(user_id);
