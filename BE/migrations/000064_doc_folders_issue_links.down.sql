DROP TABLE IF EXISTS document_issue_links;

ALTER TABLE documents DROP COLUMN IF EXISTS is_folder;
