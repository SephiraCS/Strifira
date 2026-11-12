package handler

import (
	"net/http"
	"strings"
	"testing"

	mw "github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMailRemovalHTTPAuthenticationAndWorkspaceIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	owner, outsider := env.seedOwner("mail-removal"), env.seedOwner("mail-outsider")
	s := service.NewMailService(repository.NewMailAccountRepository(env.db), repository.NewMailMessageRepository(env.db), repository.NewMailDraftRepository(env.db), realtime.NewHub(), []byte(strings.Repeat("h", 32)))
	h := NewMailHandler(s)
	api := env.server.Group("/api", mw.Auth(integrationJWTSecret))
	ws := api.Group("/workspaces/:slug", mw.WorkspaceMembership(repository.NewWorkspaceRepository(env.db)))
	// Match the existing production account routes and membership policy.
	ws.DELETE("/mail/accounts/:accountId", h.DeleteAccount)
	ws.GET("/mail/accounts", h.ListAccounts)
	id := uuid.New()
	_, err := env.db.Exec(`INSERT INTO mail_accounts(id,workspace_id,created_by,email_address,imap_host,imap_port,smtp_host,smtp_port,username,password_encrypted) VALUES($1,$2,$3,'mail@example.test','imap.example.test',993,'smtp.example.test',587,'mail','ciphertext')`, id, owner.WorkspaceID, owner.ID)
	require.NoError(t, err)
	path := owner.path("/mail/accounts/" + id.String())
	require.Equal(t, http.StatusUnauthorized, env.do(http.MethodDelete, path, "", nil).Code)
	require.Equal(t, http.StatusForbidden, env.do(http.MethodDelete, path, outsider.Token, nil).Code)
	require.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, outsider.path("/mail/accounts/"+id.String()), outsider.Token, nil).Code)
	var count int
	require.NoError(t, env.db.Get(&count, `SELECT count(*) FROM mail_accounts WHERE id=$1`, id))
	require.Equal(t, 1, count)
	require.Equal(t, http.StatusOK, env.do(http.MethodDelete, path, owner.Token, nil).Code)
	require.NoError(t, env.db.Get(&count, `SELECT count(*) FROM mail_accounts WHERE id=$1`, id))
	require.Zero(t, count)
	require.Equal(t, http.StatusNotFound, env.do(http.MethodDelete, path, owner.Token, nil).Code)
}
