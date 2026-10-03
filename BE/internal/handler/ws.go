package handler

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
	"nhooyr.io/websocket"
)

// Heartbeat cadence for live WebSockets: ping every 20s and fail the
// connection when no pong arrives within pongTimeout. Without it, half-open
// connections (NAT timeouts, sleep/resume, backend restarts behind proxies)
// linger as phantom members — presence counts stay wrong and share
// signaling silently black-holes.
const (
	wsPingInterval = 20 * time.Second
	wsPongTimeout  = 30 * time.Second
)

type WSHandler struct {
	hub            *realtime.Hub
	originPatterns []string
}

// NewWSHandler accepts upgrades whose Origin is the request's own host or
// matches one of originPatterns (host[:port] globs, see
// WebSocketOriginPatterns). Authentication rides on the access_token cookie,
// so accepting arbitrary origins would allow cross-site WebSocket hijacking.
func NewWSHandler(hub *realtime.Hub, originPatterns []string) *WSHandler {
	return &WSHandler{hub: hub, originPatterns: originPatterns}
}

// WebSocketOriginPatterns derives the allowed browser origins for the
// realtime socket from the configured frontend URL(s). Development also
// admits any local loopback port so Vite and preview servers keep working.
func WebSocketOriginPatterns(frontendURLs []string, development bool) []string {
	patterns := make([]string, 0, len(frontendURLs)+3)
	for _, raw := range frontendURLs {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			continue
		}
		patterns = append(patterns, escapeOriginGlob(u.Host))
	}
	if development {
		// "[" and "]" are glob metacharacters: the IPv6 loopback brackets
		// must be escaped to match literally.
		patterns = append(patterns, "localhost:*", "127.0.0.1:*", `\[::1\]:*`)
	}
	return patterns
}

// escapeOriginGlob escapes the glob metacharacters of a literal host so the
// OriginPatterns matcher (path.Match) compares it verbatim; IPv6 hosts such
// as "[fd00::1]:8080" would otherwise be read as character classes.
func escapeOriginGlob(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch r {
		case '\\', '*', '?', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (h *WSHandler) Handle(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		if c.Response().Committed {
			// Accept already answered (403 for a rejected Origin).
			log.WithError(err).WithFields(log.Fields{
				"origin": c.Request().Header.Get("Origin"), "workspace_id": ws.ID,
			}).Warn("websocket upgrade rejected")
			return nil
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to upgrade connection"})
	}

	client := h.hub.Register(conn, ws.ID, userID)
	defer h.hub.Unregister(client)

	ctx := c.Request().Context()
	go h.pingLoop(ctx, conn)
	go h.hub.WritePump(ctx, client)
	h.hub.ReadPump(ctx, client)

	return nil
}

// pingLoop keeps the connection honest in both directions: a peer that
// cannot answer a ping within the deadline is closed, which unblocks the
// ReadPump and unregisters the client. Ping writes control frames, which
// the library serializes with data writes, so this is safe next to the
// WritePump.
func (h *WSHandler) pingLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, wsPongTimeout)
			if err := conn.Ping(pingCtx); err != nil {
				cancel()
				conn.Close(websocket.StatusGoingAway, "heartbeat timeout")
				return
			}
			cancel()
		}
	}
}
