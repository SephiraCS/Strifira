-- STR-12: docs folders and document ↔ issue links.
--
-- Folders are documents that only group pages: no content of their own,
-- unlimited nesting through the existing parent_id, renamed by title and
-- moved by the existing move endpoint.
--
-- document_issue_links ties a document to a card so both sides show the
-- link: the card lists its documents and the document lists its cards.

ALTER TABLE documents ADD COLUMN is_folder BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE document_issue_links (
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    issue_id UUID NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    created_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (document_id, issue_id)
);

CREATE INDEX idx_document_issue_links_issue ON document_issue_links(issue_id);
