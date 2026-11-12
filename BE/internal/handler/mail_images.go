package handler

import (
	"errors"
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// POST keeps sender-controlled URLs, which may contain tracking tokens, out of
// access-log query strings. Authentication and workspace membership are required.
func (h *MailHandler) GetMessageImage(c echo.Context) error {
	id, err := uuid.Parse(c.Param("messageId"))
	if err != nil {
		return response.NotFound(c, "Message")
	}
	var request struct {
		Source string `json:"source"`
	}
	if err := c.Bind(&request); err != nil || len(request.Source) == 0 || len(request.Source) > 8192 {
		return response.Error(c, http.StatusBadRequest, "MAIL_IMAGE_INVALID", "Image reference is invalid or missing")
	}
	ws := c.Get("workspace").(*domain.Workspace)
	data, err := h.mailSvc.GetMessageImage(c.Request().Context(), ws.ID, id, request.Source)
	c.Response().Header().Set("Cache-Control", "private, no-store")
	switch {
	case errors.Is(err, repository.ErrMailMessageNotFound):
		return response.NotFound(c, "Image")
	case errors.Is(err, service.ErrMailImageInvalid):
		return response.Error(c, 422, "MAIL_IMAGE_INVALID", service.ErrMailImageInvalid.Error())
	case errors.Is(err, service.ErrMailImageBlocked):
		return response.Error(c, 422, "MAIL_IMAGE_BLOCKED", service.ErrMailImageBlocked.Error())
	case err != nil:
		// Do not log sender URLs, remote errors or token-bearing redirect locations.
		return response.Error(c, 502, "MAIL_IMAGE_UNAVAILABLE", service.ErrMailImageUnavailable.Error())
	}
	return response.Success(c, http.StatusOK, map[string]string{"data_url": data})
}
