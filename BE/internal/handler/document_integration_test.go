package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (env *integrationEnv) createDocument(u integrationUser, body map[string]any) map[string]any {
	env.t.Helper()
	return env.mustJSON(http.MethodPost, u.path("/docs"), u.Token, body, http.StatusCreated)
}

// mustList performs a request, asserts the status and decodes a top-level
// JSON array body. Document list endpoints return bare arrays, not the
// paginated {"data": ...} envelope.
func (env *integrationEnv) mustList(method, path, token string, body any, wantStatus int) []map[string]any {
	env.t.Helper()
	rec := env.do(method, path, token, body)
	require.Equalf(env.t, wantStatus, rec.Code, "%s %s: %s", method, path, rec.Body.String())
	out := []map[string]any{}
	if rec.Body.Len() > 0 {
		require.NoError(env.t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return out
}

func TestDocumentsIntegration_CRUDTreeTrashBacklinks(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("docsowner")

	// Create a home page and a child.
	home := env.createDocument(owner, map[string]any{"title": "Home", "icon": "🏠"})
	child := env.createDocument(owner, map[string]any{"title": "Meeting notes", "parent_id": home["id"]})
	assert.Equal(t, "🏠", home["icon"])
	require.NotEmpty(t, child["parent_id"])

	// Markdown import path converts to editor HTML.
	imported := env.createDocument(owner, map[string]any{"title": "Imported", "content_md": "# Title\n\nBody"})
	assert.Contains(t, imported["content_html"], "<h1>")

	// Tree list contains all three, ordered with parents before children.
	items := env.mustList(http.MethodGet, owner.path("/docs"), owner.Token, nil, http.StatusOK)
	require.Len(t, items, 3)

	// Search hits title and body text.
	results := env.mustList(http.MethodGet, owner.path("/docs/search?q=meeting"), owner.Token, nil, http.StatusOK)
	assert.Len(t, results, 1)

	// Update with a wiki link to "Home" records backlinks on the target.
	html := `<p>See <a class="wiki-link" data-wiki-title="Home" href="#">Home</a></p>`
	text := "See Home"
	updated := env.mustJSON(http.MethodPatch, owner.path("/docs/"+child["id"].(string)), owner.Token,
		map[string]any{"content_html": html, "content_text": text, "wiki_titles": []string{"Home"}}, http.StatusOK)
	assert.Contains(t, updated["content_html"], "wiki-link")

	backlinks := env.mustList(http.MethodGet, owner.path("/docs/"+home["id"].(string)+"/backlinks"), owner.Token, nil, http.StatusOK)
	require.Len(t, backlinks, 1)
	assert.Equal(t, "Meeting notes", backlinks[0]["title"])

	graph := env.mustJSON(http.MethodGet, owner.path("/docs/graph"), owner.Token, nil, http.StatusOK)
	assert.Len(t, graph["edges"], 1)

	// Tags: set and read back through the tree payload.
	tags := env.mustList(http.MethodPut, owner.path("/docs/"+child["id"].(string)+"/tags"), owner.Token,
		map[string]any{"tags": []string{"meetings", "notes"}}, http.StatusOK)
	assert.Len(t, tags, 2)

	// Revisions: the content change above recorded at most one snapshot; a
	// restore of any revision must respond 200 even when history is sparse.
	revs := env.mustList(http.MethodGet, owner.path("/docs/"+child["id"].(string)+"/revisions"), owner.Token, nil, http.StatusOK)
	require.Len(t, revs, 1)

	// Delete moves the subtree (parent + child) to trash.
	require.Equal(t, http.StatusOK, env.do(http.MethodDelete, owner.path("/docs/"+home["id"].(string)), owner.Token, nil).Code)
	trash := env.mustList(http.MethodGet, owner.path("/docs/trash"), owner.Token, nil, http.StatusOK)
	assert.Len(t, trash, 2)

	// Restore the child; its deleted parent forces it to the root.
	restored := env.mustJSON(http.MethodPost, owner.path("/docs/"+child["id"].(string)+"/restore"), owner.Token, nil, http.StatusOK)
	assert.Empty(t, restored["parent_id"])

	// Purge requires trash: trash the restored child again, then purge it.
	require.Equal(t, http.StatusOK, env.do(http.MethodDelete, owner.path("/docs/"+child["id"].(string)), owner.Token, nil).Code)
	require.Equal(t, http.StatusOK, env.do(http.MethodDelete, owner.path("/docs/"+child["id"].(string)+"/purge"), owner.Token, nil).Code)
	gone := env.do(http.MethodGet, owner.path("/docs/"+child["id"].(string)), owner.Token, nil)
	assert.Equal(t, http.StatusNotFound, gone.Code)
}

func TestDocumentsIntegration_GuestIsReadOnly(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("docsguestowner")
	doc := env.createDocument(owner, map[string]any{"title": "Plan"})

	// A guest member can read but not create or edit documents.
	guest := env.seedOwner("docsguest")
	_, err := env.db.Exec(`INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,$3)`,
		owner.WorkspaceID, guest.ID, "guest")
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, env.do(http.MethodPost, owner.path("/docs"), guest.Token, map[string]any{"title": "nope"}).Code)
	assert.Equal(t, http.StatusForbidden, env.do(http.MethodPatch, owner.path("/docs/"+doc["id"].(string)), guest.Token, map[string]any{"title": "nope"}).Code)
	got := env.mustJSON(http.MethodGet, owner.path("/docs/"+doc["id"].(string)), guest.Token, nil, http.StatusOK)
	assert.Equal(t, "Plan", got["title"])
}

func TestDocumentsIntegration_CommentsThreadAndResolve(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("docscomments")
	doc := env.createDocument(owner, map[string]any{"title": "Spec"})

	top := env.mustJSON(http.MethodPost, owner.path("/docs/"+doc["id"].(string)+"/comments"), owner.Token,
		map[string]any{"body": "<p>First <span data-type=\"mention\" data-id=\"" + owner.ID.String() + "\"></span></p>"}, http.StatusCreated)
	reply := env.mustJSON(http.MethodPost, owner.path("/docs/"+doc["id"].(string)+"/comments"), owner.Token,
		map[string]any{"body": "Reply", "parent_id": top["id"]}, http.StatusCreated)
	require.NotEmpty(t, reply["parent_id"])

	require.Equal(t, http.StatusOK, env.do(http.MethodPost,
		owner.path("/docs/"+doc["id"].(string)+"/comments/"+top["id"].(string)+"/resolve"), owner.Token, nil).Code)

	comments := env.mustList(http.MethodGet, owner.path("/docs/"+doc["id"].(string)+"/comments"), owner.Token, nil, http.StatusOK)
	require.Len(t, comments, 1)
	withReplies := comments[0]
	require.Len(t, withReplies["replies"], 1)
	assert.False(t, withReplies["resolved_at"] == nil)
}

func TestDocumentsIntegration_RestoreRevisionClearsUpdateLog(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("docsrestore")
	doc := env.createDocument(owner, map[string]any{"title": "Current", "content_md": "current body"})

	// Seed an older revision (bypassing the revision rate limit) and a
	// populated Yjs update log, as the doc-sync hub would leave behind.
	revID := uuid.New()
	_, err := env.db.Exec(
		`INSERT INTO document_revisions (id, document_id, title, content_html, content_text, edited_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		revID, doc["id"], "Old title", "<p>old body</p>", "old body", owner.ID)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err := env.db.Exec(`INSERT INTO document_updates (document_id, update) VALUES ($1, $2)`,
			doc["id"], []byte{byte(i)})
		require.NoError(t, err)
	}
	require.Equal(t, 3, env.count(`SELECT COUNT(*) FROM document_updates WHERE document_id = $1`, doc["id"]))

	restored := env.mustJSON(http.MethodPost,
		owner.path("/docs/"+doc["id"].(string)+"/revisions/"+revID.String()+"/restore"), owner.Token, nil, http.StatusOK)
	assert.Equal(t, "Old title", restored["title"])
	assert.Equal(t, "<p>old body</p>", restored["content_html"])
	// The stale Yjs state must be gone: editors re-seed from the restored
	// HTML instead of replaying (and autosaving) the pre-restore log.
	assert.Equal(t, 0, env.count(`SELECT COUNT(*) FROM document_updates WHERE document_id = $1`, doc["id"]))

	got := env.mustJSON(http.MethodGet, owner.path("/docs/"+doc["id"].(string)), owner.Token, nil, http.StatusOK)
	assert.Equal(t, "Old title", got["title"])
	assert.Equal(t, "<p>old body</p>", got["content_html"])
}

func TestDocumentsIntegration_DuplicateCopiesYjsLog(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("docsdup")
	doc := env.createDocument(owner, map[string]any{"title": "Original", "content_md": "Body"})

	// Simulate one persisted Yjs update row (normally written by the
	// doc-sync hub).
	_, err := env.db.Exec(`INSERT INTO document_updates (document_id, update) VALUES ($1, $2)`,
		doc["id"], []byte{1, 2, 3})
	require.NoError(t, err)

	dup := env.mustJSON(http.MethodPost, owner.path("/docs/"+doc["id"].(string)+"/duplicate"), owner.Token, nil, http.StatusCreated)
	require.Equal(t, "Original (copy)", dup["title"])
	assert.Equal(t, env.count(`SELECT COUNT(*) FROM document_updates WHERE document_id = $1`, dup["id"]), 1)
}
