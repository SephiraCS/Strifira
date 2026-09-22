-- Rotation grace for refresh tokens: a rotated token stays refreshable for a
-- short window so browser contexts sharing one cookie jar (tab + installed
-- PWA) can race the same refresh without one of them killing the session.
ALTER TABLE refresh_tokens ADD COLUMN rotated_at TIMESTAMPTZ;
