package middleware

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
	gbytes "github.com/labstack/gommon/bytes"
)

// DefaultBodyLimit is the global request body cap. It sits above the largest
// regular payloads (dev box completions at 24 MiB, 10 MiB attachment and
// 5 MiB avatar uploads plus multipart overhead); handlers keep their own
// tighter http.MaxBytesReader limits.
const DefaultBodyLimit = "32M"

// DefaultBodyReadTimeout bounds how long a request body may take to arrive.
// The server only sets ReadHeaderTimeout (a global ReadTimeout would also cut
// long-lived WebSockets), so without it a client could hold a connection and
// its partially read body open indefinitely by withholding chunks.
const DefaultBodyReadTimeout = 2 * time.Minute

// defaultBodyLimitBytes is DefaultBodyLimit in bytes, parsed the way Echo
// parses body limits ("32M" is 32,000,000 bytes).
var defaultBodyLimitBytes = func() int64 {
	limit, err := gbytes.Parse(DefaultBodyLimit)
	if err != nil {
		panic(err)
	}
	return limit
}()

// bodyLimitExempt lists routes that enforce a larger limit (and their own
// pacing) themselves: workspace imports accept archives up to 1 GiB.
var bodyLimitExempt = map[string]bool{
	"/api/workspaces/import":         true,
	"/api/workspaces/import/preview": true,
}

// BodyLimit rejects request bodies above DefaultBodyLimit with 413 and
// bounds body reads by DefaultBodyReadTimeout.
func BodyLimit() echo.MiddlewareFunc {
	return BodyLimitWith(defaultBodyLimitBytes, DefaultBodyReadTimeout)
}

// BodyLimitWith is BodyLimit with an explicit byte limit and body read
// timeout (0 disables the timeout).
//
// Enforcement streams; nothing is buffered here:
//   - a declared Content-Length above the limit is refused up front;
//   - otherwise the body is wrapped in http.MaxBytesReader. An overflow only
//     surfaces while the handler reads, and most handlers answer any
//     read/bind error with their own 400, so the response writer is guarded
//     too: once the body has overflowed, whatever the handler (or Echo's
//     error handler) answers is replaced by the 413 response.
func BodyLimitWith(limit int64, readTimeout time.Duration) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			req := c.Request()
			if bodyLimitExempt[req.URL.Path] || req.Body == nil || req.Body == http.NoBody || req.ContentLength == 0 {
				return next(c)
			}
			if req.ContentLength > limit {
				return echo.ErrStatusRequestEntityTooLarge
			}
			if readTimeout > 0 {
				// The deadline only affects reads; net/http resets it before
				// the next request on a kept-alive connection. Recorders
				// (tests) do not support it, which is fine.
				_ = http.NewResponseController(c.Response().Writer).SetReadDeadline(time.Now().Add(readTimeout))
			}
			res := c.Response()
			guard := &overflowGuard{ResponseWriter: res.Writer, response: res}
			// MaxBytesReader gets the real writer so net/http can close the
			// connection after an overflow.
			req.Body = &overflowReader{ReadCloser: http.MaxBytesReader(res.Writer, req.Body, limit), guard: guard}
			res.Writer = guard
			return next(c)
		}
	}
}

// overflowReader records on its guard when the limited body overflowed.
type overflowReader struct {
	io.ReadCloser
	guard *overflowGuard
}

func (r *overflowReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		r.guard.overflowed.Store(true)
	}
	return n, err
}

// overflowGuard turns the first response written after a body overflow into
// the standard 413 error and discards what the handler meant to send.
type overflowGuard struct {
	http.ResponseWriter
	response   *echo.Response
	overflowed atomic.Bool
	replaced   bool
	wrote      bool
}

const tooLargeBody = `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"Request body too large"}}` + "\n"

func (g *overflowGuard) WriteHeader(status int) {
	if g.wrote {
		return
	}
	g.wrote = true
	if g.overflowed.Load() && status != http.StatusRequestEntityTooLarge {
		g.replaced = true
		h := g.ResponseWriter.Header()
		h.Set("Content-Type", "application/json")
		h.Del("Content-Length")
		g.ResponseWriter.WriteHeader(http.StatusRequestEntityTooLarge)
		// Keep Echo's view (metrics, access log) in line with the wire.
		g.response.Status = http.StatusRequestEntityTooLarge
		_, _ = io.WriteString(g.ResponseWriter, tooLargeBody)
		return
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *overflowGuard) Write(b []byte) (int, error) {
	if !g.wrote {
		g.WriteHeader(http.StatusOK)
	}
	if g.replaced {
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

func (g *overflowGuard) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *overflowGuard) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(g.ResponseWriter).Hijack()
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *overflowGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }
