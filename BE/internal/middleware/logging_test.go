package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func runLogged(t *testing.T, method, path string, status int, prepare func(echo.Context)) log.Entry {
	t.Helper()
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, path, nil), rec)
	if prepare != nil {
		prepare(c)
	}
	handler := Logging()(func(c echo.Context) error {
		return c.NoContent(status)
	})
	require.NoError(t, handler(c))
	return lastEntry(t, hook)
}

func TestLoggingLevelFollowsStatus(t *testing.T) {
	require.Equal(t, log.ErrorLevel, runLogged(t, http.MethodGet, "/api/workspaces/acme/issues", 500, nil).Level)
	require.Equal(t, log.WarnLevel, runLogged(t, http.MethodGet, "/api/workspaces/acme/issues", 404, nil).Level)
	require.Equal(t, log.InfoLevel, runLogged(t, http.MethodGet, "/api/workspaces/acme/issues", 200, nil).Level)
}

func TestLoggingQuietsSuccessfulProbes(t *testing.T) {
	for _, path := range []string{"/health", "/ready", "/metrics"} {
		require.Equal(t, log.DebugLevel, runLogged(t, http.MethodGet, path, 200, nil).Level)
	}
	// A failing probe is still a signal, not noise.
	require.Equal(t, log.ErrorLevel, runLogged(t, http.MethodGet, "/health", 500, nil).Level)
}

func TestLoggingFields(t *testing.T) {
	user := uuid.New()
	entry := runLogged(t, http.MethodGet, "/api/notifications", 200, func(c echo.Context) {
		c.Set(string(UserIDKey), user)
	})

	require.Equal(t, "request", entry.Message)
	require.Equal(t, "/api/notifications", entry.Data["path"])
	require.Equal(t, 200, entry.Data["status"])
	// Stored as the uuid value; the JSON formatter renders its string form.
	require.Equal(t, user, entry.Data["user_id"])
}
