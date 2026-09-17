package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/validate"
	"github.com/labstack/echo/v4"
)

type ScreenShareHandler struct {
	shareSvc  *service.ScreenShareService
	userRepo  repository.UserRepo
	serverURL string
}

func NewScreenShareHandler(shareSvc *service.ScreenShareService, userRepo repository.UserRepo, serverURL string) *ScreenShareHandler {
	return &ScreenShareHandler{shareSvc: shareSvc, userRepo: userRepo, serverURL: serverURL}
}

type screenShareWithTokenResponse struct {
	Share     dto.ScreenShareResponse `json:"share"`
	Token     string                  `json:"token"`
	ServerURL string                  `json:"server_url"`
}

func (h *ScreenShareHandler) List(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)

	list, err := h.shareSvc.List(c.Request().Context(), ws.ID)
	if err != nil {
		return response.InternalError(c)
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

	share, token, err := h.shareSvc.Create(c.Request().Context(), ws.ID, userID, req)
	if err != nil {
		return h.mapError(c, err)
	}

	decorated, err := h.decorate(c, *share)
	if err != nil {
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusCreated, screenShareWithTokenResponse{
		Share:     decorated,
		Token:     token,
		ServerURL: h.serverURL,
	})
}

func (h *ScreenShareHandler) Get(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid share ID")
	}

	share, err := h.shareSvc.Get(c.Request().Context(), id)
	if err != nil {
		return response.InternalError(c)
	}
	if share == nil {
		return response.NotFound(c, "Screen share")
	}

	decorated, err := h.decorate(c, *share)
	if err != nil {
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusOK, decorated)
}

func (h *ScreenShareHandler) Join(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid share ID")
	}

	ws := c.Get("workspace").(*domain.Workspace)
	userID := middleware.GetUserID(c)

	share, token, err := h.shareSvc.JoinToken(c.Request().Context(), ws.ID, id, userID)
	if err != nil {
		return h.mapError(c, err)
	}

	decorated, err := h.decorate(c, *share)
	if err != nil {
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusOK, screenShareWithTokenResponse{
		Share:     decorated,
		Token:     token,
		ServerURL: h.serverURL,
	})
}

func (h *ScreenShareHandler) End(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid share ID")
	}

	userID := middleware.GetUserID(c)
	role := middleware.GetWorkspaceRole(c)
	force := role == domain.RoleOwner || role == domain.RoleAdmin

	if _, err := h.shareSvc.End(c.Request().Context(), id, userID, force); err != nil {
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
		RoomName:    share.RoomName,
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
	case errors.Is(err, service.ErrScreenSharingDisabled):
		return response.Error(c, http.StatusNotImplemented, "SCREEN_SHARING_DISABLED", "Screen sharing is not configured on this instance")
	case errors.Is(err, service.ErrScreenShareNotFound):
		return response.NotFound(c, "Screen share")
	case errors.Is(err, service.ErrScreenShareForbidden):
		return response.Forbidden(c)
	default:
		return response.InternalError(c)
	}
}
