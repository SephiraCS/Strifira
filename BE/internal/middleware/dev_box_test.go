package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestDevBoxWorkerAuthEstablishesBoxAndCreatorIdentity(t *testing.T) {
	box := &domain.DevBox{ID: uuid.New(), WorkspaceID: uuid.New(), CreatedByUserID: uuid.New(), Enabled: true}
	authenticator := &devBoxAuthenticatorFake{box: box}
	req := httptest.NewRequest(http.MethodPost, "/api/dev-box-worker/heartbeat?box_id="+uuid.NewString(), nil).WithContext(t.Context())
	req.Header.Set(echo.HeaderAuthorization, "bearer strifira_test-token")
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(req, recorder)
	c.Set(string(UserIDKey), uuid.New())
	called := false
	handler := DevBoxWorkerAuth(authenticator)(func(c echo.Context) error {
		called = true
		require.Same(t, box, GetDevBox(c))
		require.Equal(t, box.CreatedByUserID, GetUserID(c))
		return c.NoContent(http.StatusNoContent)
	})

	require.NoError(t, handler(c))
	require.True(t, called)
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, 1, authenticator.calls)
	require.Equal(t, "strifira_test-token", authenticator.raw)
	require.Equal(t, t.Context(), authenticator.ctx)
}

func TestDevBoxWorkerAuthRejectsOtherCredentialsBeforeAuthentication(t *testing.T) {
	for _, header := range []string{
		"", "strifira_test-token", "Basic strifira_test-token", "Bearer strifira_personal-key",
		"Bearer jwt-token", "Bearer", "Bearer ", "Bearer strifira_", "Bearer  strifira_test-token",
		"Bearer strifira_test-token extra", "Bearer strifira_test-token\t",
	} {
		t.Run(header, func(t *testing.T) {
			authenticator := &devBoxAuthenticatorFake{}
			req := httptest.NewRequest(http.MethodPost, "/api/dev-box-worker/claim", nil)
			req.Header.Set(echo.HeaderAuthorization, header)
			req.AddCookie(&http.Cookie{Name: "access_token", Value: "strifira_cookie-token"})
			recorder := httptest.NewRecorder()
			c := echo.New().NewContext(req, recorder)
			handler := DevBoxWorkerAuth(authenticator)(func(echo.Context) error {
				t.Fatal("unauthorized request reached worker handler")
				return nil
			})

			require.NoError(t, handler(c))
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			require.Zero(t, authenticator.calls)
			require.Nil(t, GetDevBox(c))
			require.Equal(t, uuid.Nil, GetUserID(c))
			require.NotContains(t, recorder.Body.String(), "strifira_")
		})
	}
}

func TestDevBoxWorkerAuthFailsClosedForInvalidRecords(t *testing.T) {
	valid := domain.DevBox{ID: uuid.New(), WorkspaceID: uuid.New(), CreatedByUserID: uuid.New(), Enabled: true}
	disabled, revoked, noBoxID, noWorkspace, noCreator := valid, valid, valid, valid, valid
	disabled.Enabled = false
	revoked.RevokedAt = new(time.Now())
	noBoxID.ID = uuid.Nil
	noWorkspace.WorkspaceID = uuid.Nil
	noCreator.CreatedByUserID = uuid.Nil
	for _, test := range []struct {
		name string
		box  *domain.DevBox
		err  error
	}{
		{name: "missing record"},
		{name: "authentication error", box: &valid, err: errors.New("database password strifira_sensitive-token")},
		{name: "disabled", box: &disabled},
		{name: "revoked", box: &revoked},
		{name: "missing box identity", box: &noBoxID},
		{name: "missing workspace identity", box: &noWorkspace},
		{name: "missing creator identity", box: &noCreator},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &devBoxAuthenticatorFake{box: test.box, err: test.err}
			req := httptest.NewRequest(http.MethodPost, "/api/dev-box-worker/claim", nil)
			req.Header.Set(echo.HeaderAuthorization, "Bearer strifira_test-token")
			recorder := httptest.NewRecorder()
			c := echo.New().NewContext(req, recorder)
			handler := DevBoxWorkerAuth(authenticator)(func(echo.Context) error {
				t.Fatal("invalid worker reached handler")
				return nil
			})

			require.NoError(t, handler(c))
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
			require.Equal(t, 1, authenticator.calls)
			require.Nil(t, GetDevBox(c))
			require.Equal(t, uuid.Nil, GetUserID(c))
			require.NotContains(t, recorder.Body.String(), "database password")
			require.NotContains(t, recorder.Body.String(), "strifira_")
		})
	}
}

func TestDevBoxWorkerAuthRejectsMissingAuthenticator(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/dev-box-worker/claim", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer strifira_test-token")
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(req, recorder)
	handler := DevBoxWorkerAuth(nil)(func(echo.Context) error {
		t.Fatal("missing authenticator reached handler")
		return nil
	})

	require.NoError(t, handler(c))
	require.Equal(t, http.StatusUnauthorized, recorder.Code)
}

type devBoxAuthenticatorFake struct {
	box   *domain.DevBox
	err   error
	calls int
	raw   string
	ctx   context.Context
}

func (f *devBoxAuthenticatorFake) AuthenticateWorker(ctx context.Context, raw string) (*domain.DevBox, error) {
	f.calls++
	f.ctx = ctx
	f.raw = raw
	return f.box, f.err
}
