package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

// captureLogs redirects the global logrus logger (the middleware logs
// through it) into a test hook for the duration of one test.
func captureLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	hook := logtest.NewGlobal()
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.StandardLogger().ReplaceHooks(log.LevelHooks{})
		log.SetLevel(log.InfoLevel)
	})
	return hook
}

func lastEntry(t *testing.T, hook *logtest.Hook) log.Entry {
	t.Helper()
	require.NotEmpty(t, hook.Entries, "expected at least one log entry")
	return hook.Entries[len(hook.Entries)-1]
}

func TestHTTPErrorHandlerLogsRawErrorAs500(t *testing.T) {
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/issues", nil), rec)

	HTTPErrorHandler(errors.New("db exploded"), c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "Internal Server Error", body["message"])

	entry := lastEntry(t, hook)
	require.Equal(t, log.ErrorLevel, entry.Level)
	require.Equal(t, "request handler failed", entry.Message)
	require.EqualError(t, entry.Data["error"].(error), "db exploded")
	require.Equal(t, "/api/issues", entry.Data["path"])
}

func TestHTTPErrorHandlerKeepsHTTPErrorResponses(t *testing.T) {
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/api/missing", nil), rec)

	HTTPErrorHandler(echo.NewHTTPError(http.StatusNotFound, "Issue not found"), c)

	require.Equal(t, http.StatusNotFound, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "Issue not found", body["message"])
	// 4xx is already visible on the request log line at warn; the error
	// handler must not duplicate it at error level.
	for _, entry := range hook.Entries {
		require.NotEqual(t, log.ErrorLevel, entry.Level, "4xx must not log at error level")
	}
}

func TestHTTPErrorHandlerUnwrapsInternalHTTPError(t *testing.T) {
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", nil), rec)

	outer := echo.NewHTTPError(http.StatusInternalServerError).SetInternal(echo.NewHTTPError(http.StatusBadGateway, "upstream broke"))
	HTTPErrorHandler(outer, c)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, log.ErrorLevel, lastEntry(t, hook).Level)
}

func TestHTTPErrorHandlerClientCanceled(t *testing.T) {
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/mcp", nil), rec)

	HTTPErrorHandler(context.Canceled, c)

	require.Equal(t, 499, rec.Code)
	require.Empty(t, rec.Body.Bytes())
	entry := lastEntry(t, hook)
	require.Equal(t, log.DebugLevel, entry.Level, "client cancels are routine, not errors")
}

func TestHTTPErrorHandlerCommittedResponse(t *testing.T) {
	hook := captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/x", nil), rec)

	HTTPErrorHandler(errors.New("late failure"), c)

	require.Equal(t, http.StatusOK, rec.Code, "must not rewrite a committed response")
	require.Equal(t, log.ErrorLevel, lastEntry(t, hook).Level)
}

func TestHTTPErrorHandlerHeadRequest(t *testing.T) {
	captureLogs(t)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodHead, "/x", nil), rec)

	HTTPErrorHandler(errors.New("boom"), c)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, rec.Body.Bytes())
}
