package handler

import (
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/bus"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// Version is the git commit the server was built from. Release builds set it
// with -ldflags "-X github.com/SephiraCS/strifira-backend/internal/handler.Version=<sha>"
// (see the Dockerfiles); CI compares it with the deployed commit through
// /health after every roll. Local `go run` builds report "dev".
var Version = "dev"

type HealthHandler struct {
	db  *sqlx.DB
	bus *bus.Bus
}

func NewHealthHandler(db *sqlx.DB, clusterBus *bus.Bus) *HealthHandler {
	return &HealthHandler{db: db, bus: clusterBus}
}

func (h *HealthHandler) Health(c echo.Context) error {
	return c.JSON(http.StatusOK, echo.Map{
		"status":  "ok",
		"version": Version,
		"cluster": h.clusterInfo(),
	})
}

func (h *HealthHandler) Ready(c echo.Context) error {
	if err := h.db.Ping(); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
	}
	if !h.bus.Ready() {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "bus not ready"})
	}
	return c.JSON(http.StatusOK, echo.Map{
		"status":  "ready",
		"version": Version,
		"cluster": h.clusterInfo(),
	})
}

// clusterInfo reports this node's identity and cluster size: the
// operator-facing view of the NATS cluster the node joined.
func (h *HealthHandler) clusterInfo() echo.Map {
	return echo.Map{
		"node":  h.bus.NodeID(),
		"name":  h.bus.ClusterName(),
		"peers": h.bus.ClusterPeers(),
	}
}
