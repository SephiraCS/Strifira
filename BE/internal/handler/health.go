package handler

import (
	"net/http"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// Version is the git commit the server was built from. Release builds set it
// with -ldflags "-X github.com/ku/ku-backend/internal/handler.Version=<sha>"
// (see the Dockerfiles); CI compares it with the deployed commit through
// /health after every roll. Local `go run` builds report "dev".
var Version = "dev"

type HealthHandler struct {
	db *sqlx.DB
}

func NewHealthHandler(db *sqlx.DB) *HealthHandler {
	return &HealthHandler{db: db}
}

func (h *HealthHandler) Health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok", "version": Version})
}

func (h *HealthHandler) Ready(c echo.Context) error {
	if err := h.db.Ping(); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ready", "version": Version})
}
