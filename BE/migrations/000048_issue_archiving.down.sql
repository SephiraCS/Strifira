DROP TRIGGER IF EXISTS issue_unarchive_when_reopened ON issues;
DROP FUNCTION IF EXISTS issue_unarchive_when_reopened();
DROP INDEX IF EXISTS idx_issues_auto_archive;
ALTER TABLE issues DROP COLUMN IF EXISTS archived_at;
ALTER TABLE issues DROP COLUMN IF EXISTS auto_archive_exempt;
ALTER TABLE workspaces DROP COLUMN IF EXISTS auto_archive_done_issues;
