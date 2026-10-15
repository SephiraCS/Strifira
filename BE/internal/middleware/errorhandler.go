package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

// HTTPErrorHandler replaces Echo's default so errors that escape a handler
// as a return value (instead of going through the pkg/response helpers,
// which already log) reach the log in the app's JSON format with the request
// identity attached. Echo's default logged them through its own logger:
// plain text, no request ID, invisible to anything parsing the log stream.
// Rendering matches Echo's ({"message": ...}) so existing 4xx consumers are
// unaffected.
func HTTPErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		log.WithError(err).WithFields(response.LogFields(c)).Error("handler returned an error after the response was committed")
		return
	}

	// A canceled context means the client is already gone (or shutdown
	// started); long-poll endpoints such as MCP subscriptions/listen
	// produce these routinely. Nothing useful can be sent back — record
	// the odd status (nginx's convention for it) and stay off the error
	// stream.
	if errors.Is(err, context.Canceled) {
		log.WithError(err).WithFields(response.LogFields(c)).Debug("client canceled the request")
		c.Response().WriteHeader(499)
		return
	}

	var he *echo.HTTPError
	if !errors.As(err, &he) {
		he = echo.NewHTTPError(http.StatusInternalServerError)
	} else if internal, ok := he.Internal.(*echo.HTTPError); ok {
		he = internal // mirror Echo: prefer the wrapped HTTPError
	}

	if he.Code >= 500 {
		log.WithFields(response.LogFields(c)).
			WithField("status", he.Code).
			WithError(err).
			Error("request handler failed")
	}

	code, message := he.Code, he.Message
	if code >= 500 {
		// The handler's message may carry infrastructure detail; the
		// cause is in the log above, the client gets the status text.
		message = http.StatusText(code)
	}
	switch m := message.(type) {
	case string:
		message = echo.Map{"message": m}
	case error:
		message = echo.Map{"message": m.Error()}
	}
	if c.Request().Method == http.MethodHead {
		_ = c.NoContent(code)
		return
	}
	_ = c.JSON(code, message)
}
