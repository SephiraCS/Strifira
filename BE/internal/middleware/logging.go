package middleware

import (
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

// probePaths are polled by deploy checks and monitors rather than driven by
// users. At info they crowd out real traffic in the small console log the
// alpha instance keeps, so successful probes log at debug.
var probePaths = map[string]bool{
	"/health":  true,
	"/ready":   true,
	"/metrics": true,
}

// Logging records one line per request with a status-derived level so
// failures stand out: 5xx at error, 4xx at warn, everything else at info
// (successful probe paths at debug). The body-level cause of a 5xx is logged
// where it happens — the pkg/response helpers or the HTTPErrorHandler —
// this line only ties it to the request.
func Logging() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			latency := time.Since(start)

			fields := log.Fields{
				"method":     c.Request().Method,
				"path":       c.Request().URL.Path,
				"route":      c.Path(),
				"status":     c.Response().Status,
				"latency":    latency.String(),
				"ip":         c.RealIP(),
				"request_id": c.Response().Header().Get(echo.HeaderXRequestID),
			}
			if userID := GetUserID(c); userID != uuid.Nil {
				fields["user_id"] = userID
			}

			level := log.InfoLevel
			switch status := c.Response().Status; {
			case status >= 500:
				level = log.ErrorLevel
			case status >= 400:
				level = log.WarnLevel
			case probePaths[c.Request().URL.Path]:
				level = log.DebugLevel
			}
			log.WithFields(fields).Log(level, "request")

			return err
		}
	}
}
