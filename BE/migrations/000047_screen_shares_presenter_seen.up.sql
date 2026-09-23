-- GC clock for screen shares: a share is source-less only once its
-- presenter has been unreachable for the grace period, measured from the
-- last presenter contact (not the share's creation time).
ALTER TABLE screen_shares ADD COLUMN last_presenter_seen_at timestamptz NOT NULL DEFAULT now();
