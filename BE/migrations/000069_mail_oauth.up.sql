ALTER TABLE mail_accounts ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'password'
    CHECK (auth_method IN ('password', 'oauth2'));
ALTER TABLE mail_accounts ADD COLUMN oauth_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE mail_accounts ADD COLUMN credentials_revision BIGINT NOT NULL DEFAULT 0;

CREATE TABLE mail_oauth_sessions (
    state_hash TEXT PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    payload_encrypted TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX mail_oauth_sessions_expiry ON mail_oauth_sessions(expires_at);

CREATE TABLE mail_oauth_tokens (
    account_id UUID PRIMARY KEY REFERENCES mail_accounts(id) ON DELETE CASCADE,
    token_encrypted TEXT NOT NULL
);
