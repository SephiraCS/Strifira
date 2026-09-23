package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/labstack/echo/v4"
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
	hub *realtime.Hub
}

func NewWSHandler(hub *realtime.Hub) *WSHandler {
	return &WSHandler{hub: hub}
}

func (h *WSHandler) Handle(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	conn, err := websocket.Accept(c.Response().Writer, c.Request(), &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
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
