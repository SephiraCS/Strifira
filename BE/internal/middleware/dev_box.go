package middleware

import (
	"context"
	"strings"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const devBoxContextKey = "dev_box"

type devBoxAuthenticator interface {
	AuthenticateWorker(context.Context, string) (*domain.DevBox, error)
}

// DevBoxWorkerAuth authenticates an outbound worker using its dedicated box
// token. The authenticated record supplies the box and its creator's identity.
func DevBoxWorkerAuth(authenticator devBoxAuthenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			scheme, raw, ok := strings.Cut(c.Request().Header.Get(echo.HeaderAuthorization), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(raw, "strifira_") || len(raw) <= len("strifira_") || strings.ContainsAny(raw, " \t\r\n") || authenticator == nil {
				return response.Unauthorized(c)
			}

			box, err := authenticator.AuthenticateWorker(c.Request().Context(), raw)
			if err != nil || box == nil || box.ID == uuid.Nil || box.WorkspaceID == uuid.Nil || box.CreatedByUserID == uuid.Nil || !box.Enabled || box.RevokedAt != nil {
				return response.Unauthorized(c)
			}

			c.Set(devBoxContextKey, box)
			c.Set(string(UserIDKey), box.CreatedByUserID)
			return next(c)
		}
	}
}

// GetDevBox returns the worker identity established by DevBoxWorkerAuth.
func GetDevBox(c echo.Context) *domain.DevBox {
	box, _ := c.Get(devBoxContextKey).(*domain.DevBox)
	return box
}
