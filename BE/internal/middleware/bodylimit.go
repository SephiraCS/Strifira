package middleware

import (
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// DefaultBodyLimit is the global request body cap. It sits above the largest
// regular payloads (dev box completions at 24 MiB, 10 MiB attachment and
// 5 MiB avatar uploads plus multipart overhead); handlers keep their own
// tighter http.MaxBytesReader limits.
const DefaultBodyLimit = "32M"

// bodyLimitExempt lists routes that enforce a larger limit themselves:
// workspace imports accept archives up to 1 GiB.
var bodyLimitExempt = map[string]bool{
	"/api/workspaces/import":         true,
	"/api/workspaces/import/preview": true,
}

// BodyLimit rejects request bodies above DefaultBodyLimit with 413.
func BodyLimit() echo.MiddlewareFunc {
	return echomw.BodyLimitWithConfig(echomw.BodyLimitConfig{
		Limit: DefaultBodyLimit,
		Skipper: func(c echo.Context) bool {
			return bodyLimitExempt[c.Request().URL.Path]
		},
	})
}
