package response

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

// LogFields returns the request-identifying fields attached to every
// handler-side error log: method, concrete path, matched route template and
// the request ID assigned by the RequestID middleware.
func LogFields(c echo.Context) log.Fields {
	req := c.Request()
	requestID := c.Response().Header().Get(echo.HeaderXRequestID)
	if requestID == "" {
		requestID = req.Header.Get(echo.HeaderXRequestID)
	}
	return log.Fields{
		"method":     req.Method,
		"path":       req.URL.Path,
		"route":      c.Path(),
		"request_id": requestID,
	}
}

// InternalErrorErr logs the cause of an internal failure together with the
// request context and replies with the generic 500 body. The cause never
// reaches the client.
func InternalErrorErr(c echo.Context, err error) error {
	entry := log.WithFields(LogFields(c))
	if err != nil {
		entry = entry.WithError(err)
	}
	entry.Error("internal error")
	return InternalError(c)
}

// ServiceError maps an error returned by a service to a client response
// without leaking storage details: missing rows become 404, recognised
// constraint violations become 4xx with a generic message, any other database
// or infrastructure failure (see isInfrastructureError) is logged and
// answered with 500, and remaining (domain/validation) errors keep their
// message as a 400 BAD_REQUEST. Services still report many validation
// failures as plain errors.New/fmt.Errorf values, so unknown errors keep the
// 400 default; storage and transport failures are recognised by type.
func ServiceError(c echo.Context, err error) error {
	return ServiceErrorStatus(c, http.StatusBadRequest, "BAD_REQUEST", err)
}

// ServiceErrorStatus is ServiceError with a custom status and code for
// domain errors that are not storage failures.
func ServiceErrorStatus(c echo.Context, status int, code string, err error) error {
	if err == nil {
		return InternalErrorErr(c, errors.New("nil service error"))
	}
	if isNotFound(err) {
		return NotFound(c, "Resource")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErrorResponse(c, pgErr.Code, err)
	}
	if code, ok := sqlStateFromMessage(err.Error()); ok {
		return pgErrorResponse(c, code, err)
	}
	if isInfrastructureError(err) {
		return InternalErrorErr(c, err)
	}
	return Error(c, status, code, err.Error())
}

func pgErrorResponse(c echo.Context, sqlState string, err error) error {
	switch sqlState {
	case "23505":
		return Error(c, http.StatusConflict, "CONFLICT", "A conflicting resource already exists")
	case "23503":
		return Error(c, http.StatusBadRequest, "BAD_REQUEST", "A referenced resource does not exist")
	case "22P02", "22007", "22008", "22001", "23502", "23514":
		return Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid input")
	}
	return InternalErrorErr(c, err)
}

func isNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), sql.ErrNoRows.Error())
}

// sqlStateFromMessage recovers the SQLSTATE of a pgx error that a service
// flattened with %v instead of wrapping it with %w.
func sqlStateFromMessage(message string) (string, bool) {
	const marker = "(SQLSTATE "
	idx := strings.LastIndex(message, marker)
	if idx < 0 {
		return "", false
	}
	rest := message[idx+len(marker):]
	end := strings.IndexByte(rest, ')')
	if end != 5 {
		return "", false
	}
	return rest[:end], true
}

// isInfrastructureError recognises database driver and transport failures:
// connection loss, truncated reads (io.EOF / io.ErrUnexpectedEOF), network
// errors and timeouts (net.Error) and cancelled or expired contexts. None of
// these are the caller's fault and their messages can carry addresses, so
// they become a logged 500. Upstream clients that should surface an
// actionable message (e.g. the Forgejo client's ErrUnavailable) translate
// their transport errors before they reach this point.
func isInfrastructureError(err error) bool {
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) || errors.Is(err, driver.ErrBadConn) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	message := err.Error()
	for _, marker := range []string{"sql: ", "pq: ", "failed to connect to `", "conn closed", "conn busy"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
