package handler

import (
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/labstack/echo/v4"
)

func (h *MailHandler) StartOAuth(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	var req dto.StartMailOAuthRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, 400, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	result, err := h.mailSvc.StartOAuth(c.Request().Context(), ws.ID, middleware.GetUserID(c), req)
	if err != nil {
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusOK, result)
}

func (h *MailHandler) CompleteOAuth(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	var req dto.CompleteMailOAuthRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, 400, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		return response.ValidationError(c, validationDetails(err))
	}
	ws := c.Get("workspace").(*domain.Workspace)
	account, err := h.mailSvc.CompleteOAuth(c.Request().Context(), ws.ID, middleware.GetUserID(c), req)
	if err != nil {
		return mailAccountError(c, err)
	}
	return response.Success(c, http.StatusCreated, mailAccountResponse(*account))
}
