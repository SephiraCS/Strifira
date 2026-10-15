package handler

import (
	"errors"
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type ScreenShareHandler struct {
	shareSvc *service.ScreenShareService
	userRepo repository.UserRepo
}

func NewScreenShareHandler(shareSvc *service.ScreenShareService, userRepo repository.UserRepo) *ScreenShareHandler {
	return &ScreenShareHandler{shareSvc: shareSvc, userRepo: userRepo}
}

type screenShareResponse struct {
	Share          dto.ScreenShareResponse `json:"share"`
	PresenterToken string                  `json:"presenter_token"`
}

func (h *ScreenShareHandler) List(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)

	list, err := h.shareSvc.List(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	return response.Success(c, http.StatusOK, list)
}

func (h *ScreenShareHandler) Create(c echo.Context) error {
	var req dto.CreateScreenShareRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	if err := validate.Struct(&req); err != nil {
		details := make([]dto.ErrorDetail, 0)
		for _, e := range validate.FormatErrors(err) {
			details = append(details, dto.ErrorDetail{Field: e["field"], Message: e["message"]})
		}
		return response.ValidationError(c, details)
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	share, err := h.shareSvc.Create(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		return h.mapError(c, err)
	}

	decorated, err := h.decorate(c, *share)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	return response.Success(c, http.StatusCreated, screenShareResponse{
		Share:          decorated,
		PresenterToken: share.PresenterToken.String(),
	})
}

func (h *ScreenShareHandler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid share ID")
	}

	share, err := h.shareSvc.Get(c.Request().Context(), id)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	ws := c.Get("workspace").(*domain.Workspace)
	if share == nil || share.WorkspaceID != ws.ID {
		return response.NotFound(c, "Screen share")
	}

	decorated, err := h.decorate(c, *share)
	if err != nil {
		return response.InternalErrorErr(c, err)
	}

	return response.Success(c, http.StatusOK, decorated)
}

func (h *ScreenShareHandler) End(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid share ID")
	}

	userID := middleware.GetUserID(c)
	role := middleware.GetWorkspaceRole(c)
	var req dto.EndScreenShareRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}
	force := req.Force && (role == domain.RoleOwner || role == domain.RoleAdmin)
	if req.Force && !force {
		return response.Forbidden(c)
	}
	token, _ := uuid.Parse(req.PresenterToken)
	ws := c.Get("workspace").(*domain.Workspace)

	if _, err := h.shareSvc.End(c.Request().Context(), ws.ID, id, userID, token, force); err != nil {
		return h.mapError(c, err)
	}

	return response.Success(c, http.StatusOK, map[string]string{"status": "ended"})
}

// decorate fills in the presenter summary for single-share responses; the
// share service owns the heavier list decoration (viewer counts, GC).
func (h *ScreenShareHandler) decorate(c echo.Context, share domain.ScreenShare) (dto.ScreenShareResponse, error) {
	resp := dto.ScreenShareResponse{
		ID:          share.ID.String(),
		WorkspaceID: share.WorkspaceID.String(),
		StartedBy:   share.StartedBy.String(),
		Status:      share.Status,
		CreatedAt:   share.CreatedAt,
		EndedAt:     share.EndedAt,
	}
	if user, err := h.userRepo.GetByID(c.Request().Context(), share.StartedBy); err == nil && user != nil {
		resp.StartedByUser = &dto.ScreenShareUser{
			ID:          user.ID.String(),
			Name:        user.Name,
			DisplayName: user.DisplayName,
			AvatarURL:   user.AvatarURL,
		}
	}
	return resp, nil
}

func (h *ScreenShareHandler) mapError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrScreenShareNotFound):
		return response.NotFound(c, "Screen share")
	case errors.Is(err, service.ErrScreenShareForbidden):
		return response.Forbidden(c)
	case errors.Is(err, service.ErrScreenShareAlreadyActive):
		return response.Error(c, http.StatusConflict, "SHARE_ALREADY_ACTIVE", "You are already sharing in another tab. Stop that share before starting a new one.")
	default:
		return response.InternalErrorErr(c, err)
	}
}
