ALTER TABLE shared_links DROP CONSTRAINT IF EXISTS shared_links_scope_check;
ALTER TABLE shared_links ADD CONSTRAINT shared_links_scope_check
    CHECK (scope IN ('workspace', 'team', 'project', 'view'));

ALTER TABLE favorites DROP CONSTRAINT IF EXISTS favorites_entity_type_check;
ALTER TABLE favorites ADD CONSTRAINT favorites_entity_type_check
    CHECK (entity_type IN ('project', 'view', 'team', 'cycle', 'label'));

DROP TABLE IF EXISTS document_comments;
DROP TABLE IF EXISTS document_links;
DROP TABLE IF EXISTS document_tag_links;
DROP TABLE IF EXISTS document_tags;
DROP TABLE IF EXISTS document_revisions;
DROP TABLE IF EXISTS document_updates;
DROP TABLE IF EXISTS documents;
