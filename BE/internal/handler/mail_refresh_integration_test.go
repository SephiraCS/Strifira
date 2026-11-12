package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	mw "github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMailRefreshHTTPUnreadCountIsScopedAndIndependentOfSearch(t *testing.T) {
	env := newIntegrationEnv(t)
	owner, outsider := env.seedOwner("mail-refresh"), env.seedOwner("mail-other")
	s := service.NewMailService(repository.NewMailAccountRepository(env.db), repository.NewMailMessageRepository(env.db), repository.NewMailDraftRepository(env.db), realtime.NewHub(), []byte(strings.Repeat("h", 32)))
	h := NewMailHandler(s)
	api := env.server.Group("/api", mw.Auth(integrationJWTSecret))
	ws := api.Group("/workspaces/:slug", mw.WorkspaceMembership(repository.NewWorkspaceRepository(env.db)))
	ws.GET("/mail/messages", h.ListMessages)
	id := uuid.New()
	_, err := env.db.Exec(`INSERT INTO mail_accounts(id,workspace_id,created_by,email_address,imap_host,imap_port,smtp_host,smtp_port,username,password_encrypted) VALUES($1,$2,$3,'mail@example.test','imap.example.test',993,'smtp.example.test',587,'mail','ciphertext')`, id, owner.WorkspaceID, owner.ID)
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO mail_messages(workspace_id,account_id,thread_key,subject,unread) VALUES($1,$2,'test','Incoming message',true)`, owner.WorkspaceID, id)
	require.NoError(t, err)
	path := owner.path("/mail/messages?folder=sent&q=missing&accountId=" + id.String())
	require.Equal(t, http.StatusUnauthorized, env.do(http.MethodGet, path, "", nil).Code)
	require.Equal(t, http.StatusForbidden, env.do(http.MethodGet, path, outsider.Token, nil).Code)
	for _, scenario := range []struct {
		path, token string
		count       int
	}{
		{path, owner.Token, 1},
		{outsider.path("/mail/messages?accountId=" + id.String()), outsider.Token, 0},
	} {
		response := env.do(http.MethodGet, scenario.path, scenario.token, nil)
		require.Equal(t, http.StatusOK, response.Code)
		var body dto.MailMessageListResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
		require.Empty(t, body.Messages)
		require.Equal(t, scenario.count, body.UnreadInboxCount)
	}
}
