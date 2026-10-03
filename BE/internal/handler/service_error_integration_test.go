package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A storage call that dies with the request context (a timeout or a
// cancelled client) is a server-side failure: it must be a logged, generic
// 500, never a 400 echoing the driver's message.
func TestServiceError_ContextFailuresAreServerErrors(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("ctxerr")
	teamID := env.createTeam(owner, "Ctx", "CTX")

	// Issue inserts stall in the database long enough for the request
	// deadline to pass mid-query.
	_, err := env.db.Exec(`
		CREATE FUNCTION stall_issue_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN PERFORM pg_sleep(3); RETURN NEW; END $$;
		CREATE TRIGGER stall_issue_insert BEFORE INSERT ON issues
			FOR EACH ROW EXECUTE FUNCTION stall_issue_insert();`)
	require.NoError(t, err)

	raw, err := json.Marshal(map[string]any{"title": "slow", "team_id": teamID})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, owner.path("/issues"), bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+owner.Token)
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "deadline")
	assert.NotContains(t, rec.Body.String(), "timeout")
}

// A failed shared-link lookup is a storage failure, not invalid input, and
// its driver message must not reach the caller.
func TestSharedLinkUpdate_LookupFailureIsServerError(t *testing.T) {
	env := newIntegrationEnv(t)
	owner := env.seedOwner("linkerr")
	link := env.mustJSON(http.MethodPost, owner.path("/shared-links"), owner.Token, map[string]any{"scope": "workspace"}, http.StatusCreated)
	path := owner.path("/shared-links/" + link["id"].(string))

	rec := env.do(http.MethodPatch, path, owner.Token, map[string]any{"expires_at": "tomorrow"})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "validation errors stay client errors")
	assert.Contains(t, rec.Body.String(), "invalid expires_at format")

	_, err := env.db.Exec(`ALTER TABLE shared_links RENAME TO shared_links_away`)
	require.NoError(t, err)
	rec = env.do(http.MethodPatch, path, owner.Token, map[string]any{"is_active": false})
	_, err = env.db.Exec(`ALTER TABLE shared_links_away RENAME TO shared_links`)
	require.NoError(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "shared_links")
}
