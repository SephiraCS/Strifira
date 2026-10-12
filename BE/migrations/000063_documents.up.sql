-- Docs: Notion/Obsidian-style pages with Yjs collaborative editing.
--
-- documents stores page metadata plus a sanitized HTML snapshot (render,
-- search, public sharing); the authoritative editing state is the Yjs update
-- log in document_updates, replayed by clients when they join a doc room.

CREATE TABLE documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    parent_id UUID REFERENCES documents(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    icon TEXT,
    content_html TEXT NOT NULL DEFAULT '',
    content_text TEXT NOT NULL DEFAULT '',
    position DOUBLE PRECISION NOT NULL DEFAULT 0,
    is_template BOOLEAN NOT NULL DEFAULT false,
    created_by UUID NOT NULL REFERENCES users(id),
    updated_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    deleted_by UUID
);

CREATE INDEX idx_documents_workspace ON documents(workspace_id, deleted_at);
CREATE INDEX idx_documents_parent ON documents(parent_id);

-- Append-only Yjs update log. Compacted periodically to a single full-state
-- row per document by the doc-sync hub (id order == replay order).
CREATE TABLE document_updates (
    id BIGSERIAL PRIMARY KEY,
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    update BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_document_updates_doc ON document_updates(document_id, id);

-- Version-history snapshots of the sanitized HTML state.
CREATE TABLE document_revisions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    icon TEXT,
    content_html TEXT NOT NULL DEFAULT '',
    content_text TEXT NOT NULL DEFAULT '',
    edited_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_document_revisions_doc ON document_revisions(document_id, created_at DESC);

-- Workspace-scoped tags (#tag) for documents.
CREATE TABLE document_tags (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    color TEXT,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(workspace_id, name)
);

CREATE INDEX idx_document_tags_workspace ON document_tags(workspace_id);

CREATE TABLE document_tag_links (
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    tag_id UUID NOT NULL REFERENCES document_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (document_id, tag_id)
);

-- Resolved [[wiki links]] between pages (source links to target), powering
-- backlinks and the graph view.
CREATE TABLE document_links (
    source_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    target_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_id, target_id),
    CHECK (source_id <> target_id)
);

CREATE INDEX idx_document_links_target ON document_links(target_id);

-- Comments on documents (threaded, resolvable; mirrors comments).
CREATE TABLE document_comments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id),
    body TEXT NOT NULL,
    parent_id UUID REFERENCES document_comments(id),
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_document_comments_doc ON document_comments(document_id, created_at);
CREATE INDEX idx_document_comments_parent ON document_comments(parent_id);

-- Documents become favoritable alongside projects/views/teams/cycles/labels.
ALTER TABLE favorites DROP CONSTRAINT IF EXISTS favorites_entity_type_check;
ALTER TABLE favorites ADD CONSTRAINT favorites_entity_type_check
    CHECK (entity_type IN ('project', 'view', 'team', 'cycle', 'label', 'document'));

-- Shared links can expose a single document read-only.
ALTER TABLE shared_links DROP CONSTRAINT IF EXISTS shared_links_scope_check;
ALTER TABLE shared_links ADD CONSTRAINT shared_links_scope_check
    CHECK (scope IN ('workspace', 'team', 'project', 'view', 'document'));
