-- Cycle burndown looks up cycle membership changes with
--   WHERE field = 'cycle' AND (old_value = $1 OR new_value = $1)
-- Partial indexes on each side let Postgres answer that with a BitmapOr
-- instead of scanning the whole issue history.
CREATE INDEX IF NOT EXISTS idx_issue_history_cycle_new_value
    ON issue_history (new_value) WHERE field = 'cycle';
CREATE INDEX IF NOT EXISTS idx_issue_history_cycle_old_value
    ON issue_history (old_value) WHERE field = 'cycle';
