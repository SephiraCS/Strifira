package handler

import (
	"encoding/base64"
	"encoding/json"
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

func TestMailImagesHTTPAuthenticationWorkspaceAndMessageScope(t *testing.T) {
	env := newIntegrationEnv(t)
	owner, outsider := env.seedOwner("images"), env.seedOwner("images-other")
	s := service.NewMailService(repository.NewMailAccountRepository(env.db), repository.NewMailMessageRepository(env.db), repository.NewMailDraftRepository(env.db), realtime.NewHub(), []byte(strings.Repeat("i", 32)))
	h := NewMailHandler(s)
	api := env.server.Group("/api", mw.Auth(integrationJWTSecret))
	ws := api.Group("/workspaces/:slug", mw.WorkspaceMembership(repository.NewWorkspaceRepository(env.db)))
	ws.POST("/mail/messages/:messageId/images", h.GetMessageImage)
	account, id, other, attachment := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := env.db.Exec(`INSERT INTO mail_accounts(id,workspace_id,created_by,email_address,imap_host,imap_port,smtp_host,smtp_port,username,password_encrypted) VALUES($1,$2,$3,'mail@example.test','imap.example.test',993,'smtp.example.test',587,'test','ciphertext')`, account, owner.WorkspaceID, owner.ID)
	require.NoError(t, err)
	body := `<img data-mail-image="attachment:` + attachment.String() + `">`
	for _, mid := range []uuid.UUID{id, other} {
		_, err = env.db.Exec(`INSERT INTO mail_messages(id,workspace_id,account_id,thread_key,body_html) VALUES($1,$2,$3,'images',$4)`, mid, owner.WorkspaceID, account, body)
		require.NoError(t, err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/lJkAAAAASUVORK5CYII=")
	require.NoError(t, err)
	_, err = env.db.Exec(`INSERT INTO mail_attachments(id,message_id,filename,content_type,size_bytes,content) VALUES($1,$2,'image.png','image/png',$3,$4)`, attachment, id, len(png), png)
	require.NoError(t, err)
	path := owner.path("/mail/messages/" + id.String() + "/images")
	request := map[string]string{"source": "attachment:" + attachment.String()}
	require.Equal(t, 401, env.do(http.MethodPost, path, "", request).Code)
	require.Equal(t, 403, env.do(http.MethodPost, path, outsider.Token, request).Code)
	require.Equal(t, 404, env.do(http.MethodPost, outsider.path("/mail/messages/"+id.String()+"/images"), outsider.Token, request).Code)
	require.Equal(t, 404, env.do(http.MethodPost, owner.path("/mail/messages/"+other.String()+"/images"), owner.Token, request).Code)
	require.Equal(t, 422, env.do(http.MethodPost, path, owner.Token, map[string]string{"source": "https://not-in-message.example.test/x"}).Code)
	response := env.do(http.MethodPost, path, owner.Token, request)
	require.Equal(t, 200, response.Code)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	var got map[string]string
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png), got["data_url"])
}
