-- Kanban column editing (STR-9): per-status icon, WIP limit and archiving.
ALTER TABLE team_statuses ADD COLUMN IF NOT EXISTS icon VARCHAR(50);
ALTER TABLE team_statuses ADD COLUMN IF NOT EXISTS wip_limit INT CHECK (wip_limit IS NULL OR wip_limit > 0);
ALTER TABLE team_statuses ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;
