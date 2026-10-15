package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Read the real server registrations so RBAC tests cover the middleware
// configured for production, including missing permissions or omitted routes.
func githubDisableRouter(t *testing.T, calls *[]string, workspaceID uuid.UUID, role string) *echo.Echo {
	t.Helper()
	source, err := os.ReadFile("../../cmd/server/main.go")
	require.NoError(t, err)
	registrations := regexp.MustCompile(`ws\.POST\("(/github/(?:disable|enable))", githubH\.(\w+), mw\.RequirePermission\("([^"]+)"\)\)`).
		FindAllStringSubmatch(string(source), -1)
	require.Len(t, registrations, 2, "both pause routes must have explicit permission middleware")
	handlers := map[string]echo.HandlerFunc{
		"Disable": func(c echo.Context) error {
			*calls = append(*calls, "disable")
			return c.JSON(http.StatusOK, map[string]string{"status": "disabled"})
		},
		"Enable": func(c echo.Context) error {
			*calls = append(*calls, "enable")
			return c.JSON(http.StatusOK, map[string]string{"status": "enabled"})
		},
	}
	e := echo.New()
	ws := e.Group("/api/workspaces/:slug", func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("workspace", &domain.Workspace{ID: workspaceID})
			c.Set("workspace_role", role)
			return next(c)
		}
	})
	for _, registration := range registrations {
		path, name, permission := registration[1], registration[2], registration[3]
		require.Equal(t, "workspace:manage", permission, "pausing the integration manages the workspace")
		require.Contains(t, handlers, name)
		ws.POST(path, handlers[name], middleware.RequirePermission(permission))
	}
	return e
}

func requestGitHubDisable(t *testing.T, calls *[]string, ws uuid.UUID, role, path string) *httptest.ResponseRecorder {
	t.Helper()
	e := githubDisableRouter(t, calls, ws, role)
	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil).WithContext(t.Context()))
	return recorder
}

func TestGitHubDisableEnableRoutesScopeAndPermissions(t *testing.T) {
	workspaceID := uuid.New()
	for _, test := range []struct {
		path      string
		operation string
	}{
		{path: "/api/workspaces/test/github/disable", operation: "disable"},
		{path: "/api/workspaces/test/github/enable", operation: "enable"},
	} {
		for _, role := range []string{domain.RoleOwner, domain.RoleAdmin, domain.RoleMember, domain.RoleGuest, "", "unknown"} {
			t.Run(test.operation+"/"+role, func(t *testing.T) {
				calls := &[]string{}
				recorder := requestGitHubDisable(t, calls, workspaceID, role, test.path)
				if role != domain.RoleOwner {
					require.Equal(t, http.StatusForbidden, recorder.Code)
					require.Empty(t, *calls, "forbidden request must not reach the service")
					return
				}
				require.Equal(t, http.StatusOK, recorder.Code)
				require.Equal(t, []string{test.operation}, *calls)
			})
		}
	}
}

func TestGitHubDisableErrorMapping(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{
			name:    "disabled workspace",
			err:     fmt.Errorf("wrapping upstream detail: %w", service.ErrGitHubDisabled),
			status:  http.StatusConflict,
			code:    "GITHUB_DISABLED",
			message: "GitHub is disabled for this workspace",
		},
		{
			name:    "plain validation stays 400",
			err:     errors.New("instance URL and token are required"),
			status:  http.StatusBadRequest,
			code:    "BAD_REQUEST",
			message: "instance URL and token are required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := echo.New()
			recorder := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil).WithContext(t.Context()), recorder)

			require.NoError(t, githubConnectionError(c, test.err))

			require.Equal(t, test.status, recorder.Code)
			var result dto.ErrorResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, test.code, result.Error.Code)
			require.Equal(t, test.message, result.Error.Message)
			require.NotContains(t, recorder.Body.String(), "upstream detail", "fixed messages only")
		})
	}
}
