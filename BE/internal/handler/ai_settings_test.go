package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/aiegress"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type aiHandlerSettingsRepo struct {
	repository.AISettingsRepo
	settings *domain.AISettings
}

func (r *aiHandlerSettingsRepo) GetByWorkspaceID(context.Context, uuid.UUID) (*domain.AISettings, error) {
	return r.settings, nil
}

type aiHandlerWorkspaceRepo struct {
	repository.WorkspaceRepo
	workspace *domain.Workspace
}

func (r *aiHandlerWorkspaceRepo) GetByID(context.Context, uuid.UUID) (*domain.Workspace, error) {
	return r.workspace, nil
}

func TestAISettingsHandlerConnectionTest(t *testing.T) {
	for _, test := range []struct {
		name       string
		owner      bool
		configured bool
		upstream   int
		status     int
		errorCode  string
	}{
		{name: "saved provider", owner: true, configured: true, upstream: http.StatusOK, status: http.StatusOK},
		{name: "owner only", configured: true, upstream: http.StatusOK, status: http.StatusForbidden, errorCode: "FORBIDDEN"},
		{name: "no configuration", owner: true, status: http.StatusBadRequest, errorCode: "AI_NOT_CONFIGURED"},
		{name: "upstream failed", owner: true, configured: true, upstream: http.StatusUnauthorized, status: http.StatusBadGateway, errorCode: "AI_PROVIDER_ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				providerCalls.Add(1)
				assert.Equal(t, "Bearer saved-secret", r.Header.Get("Authorization"))
				var request struct {
					Model string `json:"model"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				assert.Equal(t, "saved-model", request.Model)
				w.WriteHeader(test.upstream)
				if test.upstream == http.StatusOK {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"<p>Connection successful.</p>"}}]}`)
				} else {
					_, _ = io.WriteString(w, `{"error":{"message":"saved-secret http://private.example/key"}}`)
				}
			}))
			t.Cleanup(provider.Close)
			workspace := &domain.Workspace{ID: uuid.New(), OwnerID: uuid.New()}
			userID := uuid.New()
			if test.owner {
				userID = workspace.OwnerID
			}
			key := crypto.DeriveKey("test-ai-handler")
			encrypted, err := crypto.Encrypt("saved-secret", key)
			require.NoError(t, err)
			settingsRepo := &aiHandlerSettingsRepo{}
			if test.configured {
				settingsRepo.settings = &domain.AISettings{
					Provider:        domain.AIProviderOpenAICompatible,
					BaseURL:         provider.URL,
					Model:           "saved-model",
					APIKeyEncrypted: new(encrypted),
				}
			}
			policy, err := aiegress.NewPolicy(provider.URL)
			require.NoError(t, err)
			svc := service.NewAISettingsService(settingsRepo, &aiHandlerWorkspaceRepo{workspace: workspace}, nil, key, policy)
			handler := NewAISettingsHandler(svc)
			e := echo.New()
			e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
				return func(c echo.Context) error {
					c.Set("workspace", workspace)
					c.Set(string(middleware.UserIDKey), userID)
					return next(c)
				}
			})
			e.POST("/api/workspaces/:slug/ai-settings/test", handler.TestConnection, middleware.RequireOwner())
			// Even a submitted configuration cannot override the saved provider.
			request := httptest.NewRequest(http.MethodPost, "/api/workspaces/test/ai-settings/test", strings.NewReader(`{"model":"unsaved-model","base_url":"http://private.example","api_key":"unsaved-secret"}`))
			request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, request)
			require.Equal(t, test.status, recorder.Code, recorder.Body.String())
			if test.errorCode != "" {
				var result dto.ErrorResponse
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
				assert.Equal(t, test.errorCode, result.Error.Code)
			} else {
				var result dto.AIConnectionTestResponse
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
				assert.Equal(t, "saved-model", result.Model)
				assert.GreaterOrEqual(t, result.LatencyMS, int64(0))
			}
			assert.NotContains(t, recorder.Body.String(), "saved-secret")
			assert.NotContains(t, recorder.Body.String(), "private.example")
			assert.NotContains(t, recorder.Body.String(), provider.URL)
			if test.owner && test.configured {
				assert.Equal(t, int32(1), providerCalls.Load())
			} else {
				assert.Zero(t, providerCalls.Load())
			}
		})
	}
}

func TestAISettingsHandlerConnectionTestRejectsNonownerWithoutMiddleware(t *testing.T) {
	workspace := &domain.Workspace{ID: uuid.New(), OwnerID: uuid.New()}
	svc := service.NewAISettingsService(nil, &aiHandlerWorkspaceRepo{workspace: workspace}, nil, nil, aiegress.Policy{})
	handler := NewAISettingsHandler(svc)
	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/ai-settings/test", nil), recorder)
	c.Set("workspace", workspace)
	c.Set(string(middleware.UserIDKey), uuid.New())
	require.NoError(t, handler.TestConnection(c))
	assert.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestAISettingsHandlerReworkRejectsBlankInput(t *testing.T) {
	handler := NewAISettingsHandler(service.NewAISettingsService(nil, nil, nil, nil, aiegress.Policy{}))
	e := echo.New()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/ai/rework", strings.NewReader(`{"text":" \n\t "}`))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(request, recorder)
	c.Set("workspace", &domain.Workspace{ID: uuid.New()})
	require.NoError(t, handler.Rework(c))
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "BAD_REQUEST")
}
