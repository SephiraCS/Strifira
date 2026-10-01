package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type shareScopeRepo struct {
	repository.ScreenShareRepo
	share *domain.ScreenShare
	ended bool
}

func (r *shareScopeRepo) GetByID(context.Context, uuid.UUID) (*domain.ScreenShare, error) {
	return r.share, nil
}

func (r *shareScopeRepo) MarkEnded(context.Context, uuid.UUID) error {
	r.ended = true
	return nil
}

func TestScreenShareHandlerRejectsForeignWorkspace(t *testing.T) {
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			repo := &shareScopeRepo{share: share}
			h := NewScreenShareHandler(service.NewScreenShareService(repo, nil, realtime.NewHub()), nil)
			req := httptest.NewRequest(method, "/api/workspaces/other/shares/"+share.ID.String(), strings.NewReader(`{"force":true}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			recorder := httptest.NewRecorder()
			c := echo.New().NewContext(req, recorder)
			c.SetParamNames("id")
			c.SetParamValues(share.ID.String())
			c.Set("workspace", &domain.Workspace{ID: uuid.New()})
			c.Set(string(middleware.UserIDKey), share.StartedBy)
			c.Set("workspace_role", domain.RoleOwner)
			var err error
			if method == http.MethodGet {
				err = h.Get(c)
			} else {
				err = h.End(c)
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.False(t, repo.ended, "foreign shares must never be mutated")
		})
	}
}

func TestScreenShareHandlerAdminNeedsExplicitForce(t *testing.T) {
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive}
	repo := &shareScopeRepo{share: share}
	h := NewScreenShareHandler(service.NewScreenShareService(repo, nil, realtime.NewHub()), nil)
	req := httptest.NewRequest(http.MethodPost, "/shares/"+share.ID.String()+"/end", strings.NewReader(`{}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	c := echo.New().NewContext(req, recorder)
	c.SetParamNames("id")
	c.SetParamValues(share.ID.String())
	c.Set("workspace", &domain.Workspace{ID: share.WorkspaceID})
	c.Set(string(middleware.UserIDKey), share.StartedBy)
	c.Set("workspace_role", domain.RoleOwner)
	require.NoError(t, h.End(c))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.False(t, repo.ended, "another admin tab must not implicitly claim presenter ownership")
}
