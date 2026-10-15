package handler

import (
	"net/http"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/middleware"
	"github.com/SephiraCS/strifira-backend/internal/service"
	"github.com/SephiraCS/strifira-backend/pkg/response"
	"github.com/SephiraCS/strifira-backend/pkg/validate"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type APIKeyHandler struct {
	apiKeySvc *service.APIKeyService
}

func NewAPIKeyHandler(apiKeySvc *service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{apiKeySvc: apiKeySvc}
}

func (h *APIKeyHandler) List(c echo.Context) error {
	keys, err := h.apiKeySvc.List(c.Request().Context(), middleware.GetUserID(c))
	if err != nil {
		return response.InternalErrorErr(c, err)
	}
	resp := make([]dto.APIKeyResponse, len(keys))
	for i, k := range keys {
		resp[i] = toAPIKeyResponse(k)
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *APIKeyHandler) Create(c echo.Context) error {
	var req dto.CreateAPIKeyRequest
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

	key, raw, err := h.apiKeySvc.Create(c.Request().Context(), middleware.GetUserID(c), req)
	if err != nil {
		return response.ServiceError(c, err)
	}
	return response.Success(c, http.StatusCreated, dto.CreateAPIKeyResponse{
		APIKeyResponse: toAPIKeyResponse(*key),
		Token:          raw,
	})
}

func (h *APIKeyHandler) Revoke(c echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid API key ID")
	}
	if err := h.apiKeySvc.Revoke(c.Request().Context(), id, middleware.GetUserID(c)); err != nil {
		return response.ServiceError(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "revoked"})
}

func toAPIKeyResponse(k domain.APIKey) dto.APIKeyResponse {
	return dto.APIKeyResponse{
		ID:         k.ID.String(),
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Scopes:     k.Scopes,
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
		ExpiresAt:  k.ExpiresAt,
		RevokedAt:  k.RevokedAt,
	}
}
