package middleware

import (
	"bytes"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	gbytes "github.com/labstack/gommon/bytes"
)

// DefaultBodyLimit is the global request body cap. It sits above the largest
// regular payloads (dev box completions at 24 MiB, 10 MiB attachment and
// 5 MiB avatar uploads plus multipart overhead); handlers keep their own
// tighter http.MaxBytesReader limits.
const DefaultBodyLimit = "32M"

// defaultBodyLimitBytes is DefaultBodyLimit in bytes, parsed exactly as
// Echo's limiter parses it so both paths enforce the same cap.
var defaultBodyLimitBytes = func() int64 {
	limit, err := gbytes.Parse(DefaultBodyLimit)
	if err != nil {
		panic(err)
	}
	return limit
}()

// bodyLimitExempt lists routes that enforce a larger limit themselves:
// workspace imports accept archives up to 1 GiB.
var bodyLimitExempt = map[string]bool{
	"/api/workspaces/import":         true,
	"/api/workspaces/import/preview": true,
}

// BodyLimit rejects request bodies above DefaultBodyLimit with 413.
//
// A declared Content-Length above the limit is refused up front by Echo's
// limiter. A body of unknown length (chunked) would only overflow while a
// handler reads it, and handlers turn read/bind errors into their own 400,
// so such bodies are read here first (up to the limit): an overflow is a
// 413 before any handler runs, otherwise the handler gets the buffered body
// with its now-known length.
func BodyLimit() echo.MiddlewareFunc {
	skip := func(c echo.Context) bool { return bodyLimitExempt[c.Request().URL.Path] }
	limiter := echomw.BodyLimitWithConfig(echomw.BodyLimitConfig{Limit: DefaultBodyLimit, Skipper: skip})
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		limited := limiter(next)
		return func(c echo.Context) error {
			req := c.Request()
			if skip(c) || req.ContentLength >= 0 || req.Body == nil || req.Body == http.NoBody {
				return limited(c)
			}
			body, err := io.ReadAll(io.LimitReader(req.Body, defaultBodyLimitBytes+1))
			_ = req.Body.Close()
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
			}
			if int64(len(body)) > defaultBodyLimitBytes {
				return echo.ErrStatusRequestEntityTooLarge
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
			req.ContentLength = int64(len(body))
			req.TransferEncoding = nil
			return limited(c)
		}
	}
}
