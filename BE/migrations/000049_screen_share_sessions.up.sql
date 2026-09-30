ALTER TABLE screen_shares ADD COLUMN presenter_token UUID NOT NULL DEFAULT gen_random_uuid();

-- Retire duplicate historical sessions before enforcing one capture per user.
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY workspace_id, started_by ORDER BY created_at DESC, id) AS position
    FROM screen_shares WHERE status = 'active'
)
UPDATE screen_shares SET status = 'ended', ended_at = NOW()
WHERE id IN (SELECT id FROM ranked WHERE position > 1);

CREATE UNIQUE INDEX idx_screen_shares_active_presenter
    ON screen_shares(workspace_id, started_by) WHERE status = 'active';
