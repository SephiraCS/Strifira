ALTER TABLE workspaces
    ADD COLUMN auto_archive_done_issues BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE issues
    ADD COLUMN archived_at TIMESTAMPTZ,
    ADD COLUMN auto_archive_exempt BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_issues_auto_archive ON issues (completed_at)
    WHERE archived_at IS NULL AND completed_at IS NOT NULL AND NOT auto_archive_exempt;

CREATE OR REPLACE FUNCTION issue_unarchive_when_reopened()
RETURNS TRIGGER AS $$
DECLARE
    category TEXT;
BEGIN
    IF OLD.status IS DISTINCT FROM NEW.status OR OLD.status_id IS DISTINCT FROM NEW.status_id THEN
        NEW.auto_archive_exempt := FALSE;
    END IF;
    IF NEW.status_id IS NOT NULL THEN
        SELECT ts.category INTO category FROM team_statuses ts WHERE ts.id = NEW.status_id;
    END IF;
    category := COALESCE(category, CASE NEW.status
        WHEN 'done' THEN 'completed'
        ELSE NEW.status
    END);
    IF category <> 'completed' THEN
        NEW.archived_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER issue_unarchive_when_reopened
    BEFORE UPDATE OF status, status_id ON issues
    FOR EACH ROW EXECUTE FUNCTION issue_unarchive_when_reopened();
