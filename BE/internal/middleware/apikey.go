package middleware

import (
	"context"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// apiAuthCtxKey namespaces the API-key identity inside the plain
// http.Request context. The MCP SDK handler is a wrapped http.Handler and
// never sees Echo's context store, so the identity must travel on the
// request itself for MCP tool handlers to read.
type apiAuthCtxKey string

const (
	apiUserIDKey apiAuthCtxKey = "api_key_user_id"
	apiScopesKey apiAuthCtxKey = "api_key_scopes"
)

// APIKeyAuth authenticates the /mcp endpoint with a personal API key
// passed as `Authorization: Bearer strifira_...`. On success the request runs
// with the key owner's identity — the same authority as their web session.
func APIKeyAuth(apiKeySvc *service.APIKeyService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var raw string
			if auth := c.Request().Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				raw = strings.TrimPrefix(auth, "Bearer ")
			}
			if raw == "" {
				return response.Unauthorized(c)
			}

			user, key, err := apiKeySvc.Authenticate(c.Request().Context(), raw)
			if err != nil || user == nil {
				return response.Unauthorized(c)
			}

			c.Set(string(UserIDKey), user.ID)
			ctx := context.WithValue(c.Request().Context(), apiUserIDKey, user.ID)
			// Convert pq.StringArray so the stored dynamic type is []string.
			ctx = context.WithValue(ctx, apiScopesKey, []string(key.Scopes))
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// APIKeyIdentity extracts the authenticated user and key scopes from a
// request context. It returns false when the request was not authenticated
// by APIKeyAuth.
func APIKeyIdentity(ctx context.Context) (userID uuid.UUID, scopes []string, ok bool) {
	id, idOK := ctx.Value(apiUserIDKey).(uuid.UUID)
	sc, scOK := ctx.Value(apiScopesKey).([]string)
	if !idOK || !scOK || id == uuid.Nil {
		return uuid.Nil, nil, false
	}
	return id, sc, true
}
