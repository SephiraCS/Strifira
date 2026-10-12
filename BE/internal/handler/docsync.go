package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
	"nhooyr.io/websocket"
)

// docSyncReadLimit bounds one incoming frame. The largest legitimate frame
// is a compaction snapshot reply: the service caps snapshots at 12MB binary,
// which base64-encodes to 12MB * 4/3 = 16MB, plus the JSON envelope
// (~100 bytes). 32MB clears that worst case with 2x headroom; every other
// client frame is a small incremental update or cursor position.
const docSyncReadLimit = 32 << 20

// DocSyncHandler serves the per-document collaboration WebSocket: a dumb,
// ordered relay of Yjs updates plus cursor presence, backed by the DocSync
// service rooms. Authorization reuses the workspace membership middleware and
// the document lookup; write access requires the doc:update permission so
// guests can watch but not edit.
type DocSyncHandler struct {
	svc            *service.DocSyncService
	docSvc         *service.DocumentService
	originPatterns []string
}

func NewDocSyncHandler(svc *service.DocSyncService, docSvc *service.DocumentService, originPatterns []string) *DocSyncHandler {
	return &DocSyncHandler{svc: svc, docSvc: docSvc, originPatterns: originPatterns}
}

func (h *DocSyncHandler) Handle(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	role, _ := c.Get("workspace_role").(string)
	userID := middleware.GetUserID(c)

	docID, err := uuid.Parse(c.Param("docId"))
	if err != nil {
		return response.NotFound(c, "Document")
	}
	doc, err := h.docSvc.Get(c.Request().Context(), ws.ID, docID)
	if err != nil || doc.DeletedAt != nil {
		return response.NotFound(c, "Document")
	}

	conn, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{
		OriginPatterns: h.originPatterns,
	})
	if err != nil {
		if !c.Response().Committed {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to upgrade connection"})
		}
		return nil
	}
	conn.SetReadLimit(docSyncReadLimit)

	ctx := c.Request().Context()
	go h.pingLoop(ctx, conn)

	// The first frame must be "join" and carries the display identity used
	// for cursors. Until it arrives the client is not part of any room.
	joinCtx, cancelJoin := context.WithTimeout(ctx, service.DocSyncJoinTimeout)
	defer cancelJoin()
	_, data, err := conn.Read(joinCtx)
	if err != nil {
		return nil
	}
	name, color, ok := parseDocSyncJoin(data)
	if !ok {
		conn.Close(websocket.StatusPolicyViolation, "expected join message")
		return nil
	}

	canWrite := domain.HasPermission(role, domain.PermDocUpdate)
	client, _, _, err := h.svc.Join(ctx, docID, userID, name, color, canWrite, func() {
		conn.Close(websocket.StatusGoingAway, "server shutting down")
	})
	if err != nil {
		log.WithError(err).WithField("document_id", docID).Warn("doc sync join failed")
		return nil
	}
	defer h.svc.Leave(client)

	go h.writePump(ctx, conn, client)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return nil
		}
		h.svc.HandleClientMessage(ctx, client, data)
	}
}

func (h *DocSyncHandler) writePump(ctx context.Context, conn *websocket.Conn, client *service.DocSyncClient) {
	for {
		select {
		case msg, ok := <-client.Outbound():
			if !ok {
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func parseDocSyncJoin(data []byte) (name, color string, ok bool) {
	var msg struct {
		Type    string `json:"type"`
		Payload struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Type != "join" {
		return "", "", false
	}
	return msg.Payload.Name, msg.Payload.Color, true
}

func (h *DocSyncHandler) pingLoop(ctx context.Context, conn *websocket.Conn) {
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
