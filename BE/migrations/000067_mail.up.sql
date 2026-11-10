-- Mail: native e-mail client (STR-15).
--
-- mail_accounts holds per-workspace e-mail accounts with IMAP/SMTP access
-- data entered by the user. Passwords are user data: they are stored
-- encrypted at rest (AES-256-GCM, key derived at boot) and never leave the
-- API unencrypted.
--
-- mail_messages is the local cache of received and sent mail. Messages are
-- threaded by thread_key (the root Message-ID of the References chain, or
-- the message's own Message-ID). inbox/sent/archive/spam states live on the
-- row so the UI can filter without touching the remote server.
--
-- mail_attachments keeps attachment bytes (TOAST-compressed BYTEA) tied to
-- either a message (received from IMAP or sent via SMTP) or a draft.

CREATE TABLE mail_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id),
    email_address TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    imap_host TEXT NOT NULL,
    imap_port INT NOT NULL,
    -- 'none' | 'starttls' | 'tls'
    imap_security TEXT NOT NULL DEFAULT 'tls',
    smtp_host TEXT NOT NULL,
    smtp_port INT NOT NULL,
    smtp_security TEXT NOT NULL DEFAULT 'starttls',
    username TEXT NOT NULL,
    password_encrypted TEXT NOT NULL,
    signature TEXT NOT NULL DEFAULT '',
    -- Remote INBOX UIDVALIDITY and highest synced UID; the syncer resumes
    -- from these instead of re-fetching the whole mailbox.
    uid_validity BIGINT NOT NULL DEFAULT 0,
    last_uid BIGINT NOT NULL DEFAULT 0,
    last_synced_at TIMESTAMPTZ,
    last_sync_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_mail_accounts_address ON mail_accounts(workspace_id, lower(email_address));
CREATE INDEX idx_mail_accounts_workspace ON mail_accounts(workspace_id);

CREATE TABLE mail_messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    account_id UUID NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
    -- 'inbox' (fetched from IMAP) | 'sent' (composed in the app)
    folder TEXT NOT NULL DEFAULT 'inbox',
    message_id TEXT NOT NULL DEFAULT '',
    in_reply_to TEXT NOT NULL DEFAULT '',
    -- Root Message-ID of the References chain (normalized); groups a thread.
    thread_key TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    from_name TEXT NOT NULL DEFAULT '',
    from_address TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    to_addresses JSONB NOT NULL DEFAULT '[]',
    cc_addresses JSONB NOT NULL DEFAULT '[]',
    bcc_addresses JSONB NOT NULL DEFAULT '[]',
    body_text TEXT NOT NULL DEFAULT '',
    body_html TEXT NOT NULL DEFAULT '',
    snippet TEXT NOT NULL DEFAULT '',
    unread BOOLEAN NOT NULL DEFAULT TRUE,
    starred BOOLEAN NOT NULL DEFAULT FALSE,
    archived BOOLEAN NOT NULL DEFAULT FALSE,
    spam BOOLEAN NOT NULL DEFAULT FALSE,
    -- IMAP UID within the account INBOX; 0 for locally composed messages.
    uid BIGINT NOT NULL DEFAULT 0,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_mail_messages_account_message_id ON mail_messages(account_id, message_id)
    WHERE folder = 'inbox' AND message_id <> '';
CREATE INDEX idx_mail_messages_thread ON mail_messages(workspace_id, thread_key, sent_at);
CREATE INDEX idx_mail_messages_list ON mail_messages(account_id, folder, sent_at DESC);
CREATE INDEX idx_mail_messages_sync ON mail_messages(account_id, uid);

CREATE TABLE mail_drafts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    account_id UUID REFERENCES mail_accounts(id) ON DELETE CASCADE,
    -- Drafts belong to their author; other members never see them.
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    thread_key TEXT NOT NULL DEFAULT '',
    in_reply_to_message_id UUID REFERENCES mail_messages(id) ON DELETE SET NULL,
    forward_from_message_id UUID REFERENCES mail_messages(id) ON DELETE SET NULL,
    to_addresses JSONB NOT NULL DEFAULT '[]',
    cc_addresses JSONB NOT NULL DEFAULT '[]',
    bcc_addresses JSONB NOT NULL DEFAULT '[]',
    subject TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_mail_drafts_user ON mail_drafts(workspace_id, user_id, updated_at DESC);

CREATE TABLE mail_attachments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id UUID REFERENCES mail_messages(id) ON DELETE CASCADE,
    draft_id UUID REFERENCES mail_drafts(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    content BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Exactly one owner: a message or a draft, never both, never neither.
ALTER TABLE mail_attachments ADD CONSTRAINT chk_mail_attachments_owner
    CHECK ((message_id IS NULL) <> (draft_id IS NULL));
CREATE INDEX idx_mail_attachments_message ON mail_attachments(message_id);
CREATE INDEX idx_mail_attachments_draft ON mail_attachments(draft_id);
