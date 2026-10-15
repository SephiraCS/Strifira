package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// A browser tab and the installed PWA (or a second tab) share one cookie jar.
// When the 15-minute access token lapses, every context that fires an API
// call around that moment refreshes with the same refresh_token cookie, so
// the server receives the same valid token more than once. The rotation must
// tolerate that overlap instead of killing the session for the whole jar.
func TestAuthHandler_Refresh_RacingRequestsShareRotationGrace(t *testing.T) {
	e := echo.New()
	userRepo := &testUserRepo{}
	refreshRepo := &testRefreshTokenRepo{}
	authSvc := service.NewAuthService(userRepo, refreshRepo, "test-secret")
	h := NewAuthHandler(authSvc, false, nil)

	registerBody := `{"email": "race@example.com", "password": "Password123!", "name": "Race User"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(registerBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	assert.NoError(t, h.Register(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusCreated, rec.Code, "register must succeed to seed a session")

	first := cookieByName(rec.Result().Cookies(), "refresh_token")
	if first == nil {
		t.Fatal("register response did not set a refresh_token cookie")
	}

	// Context A refreshes first and wins the rotation.
	recA := refreshWithToken(h, first.Value)
	assert.Equal(t, http.StatusOK, recA.Code, "first refresh with a valid token must succeed")

	// Context B's refresh left the browser with the same cookie before A's
	// Set-Cookie landed, so the server sees the pre-rotation token again
	// within the grace window. Failing it here (and clearing the jar's
	// cookies) is what logs every context out mid-work with auth toasts.
	recB := refreshWithToken(h, first.Value)
	assert.Equal(t, http.StatusOK, recB.Code, "racing refresh within the rotation grace window must not kill the session")

	// Whichever response landed last, its cookie is in the jar now; that
	// survivor must keep the session alive.
	survivor := cookieByName(recB.Result().Cookies(), "refresh_token")
	if survivor == nil {
		t.Fatal("racing refresh did not return a refresh_token cookie")
	}
	recAfter := refreshWithToken(h, survivor.Value)
	assert.Equal(t, http.StatusOK, recAfter.Code, "session must stay usable after a raced rotation")
}

// A token replayed long after its rotation is still theft/stale-jar replay
// and must be rejected once the grace window has passed.
func TestAuthHandler_Refresh_ReplayBeyondGraceRejected(t *testing.T) {
	e := echo.New()
	userRepo := &testUserRepo{}
	refreshRepo := &testRefreshTokenRepo{backdateRotations: true}
	authSvc := service.NewAuthService(userRepo, refreshRepo, "test-secret")
	h := NewAuthHandler(authSvc, false, nil)

	registerBody := `{"email": "replay@example.com", "password": "Password123!", "name": "Replay User"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(registerBody))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	assert.NoError(t, h.Register(e.NewContext(req, rec)))
	assert.Equal(t, http.StatusCreated, rec.Code)

	first := cookieByName(rec.Result().Cookies(), "refresh_token")
	if first == nil {
		t.Fatal("register response did not set a refresh_token cookie")
	}

	recA := refreshWithToken(h, first.Value)
	assert.Equal(t, http.StatusOK, recA.Code)

	recB := refreshWithToken(h, first.Value)
	assert.Equal(t, http.StatusUnauthorized, recB.Code, "replayed refresh beyond the grace window must be rejected")
}

func refreshWithToken(h *AuthHandler, refreshToken string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: refreshToken})
	rec := httptest.NewRecorder()
	_ = h.Refresh(echo.New().NewContext(req, rec))
	return rec
}

func cookieByName(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
