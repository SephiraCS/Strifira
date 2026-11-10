-- OAuth accounts have no password to fall back to after rollback.
DELETE FROM mail_accounts WHERE auth_method = 'oauth2';
DROP TABLE mail_oauth_tokens;
DROP TABLE mail_oauth_sessions;
ALTER TABLE mail_accounts DROP COLUMN oauth_provider;
ALTER TABLE mail_accounts DROP COLUMN auth_method;
ALTER TABLE mail_accounts DROP COLUMN credentials_revision;
