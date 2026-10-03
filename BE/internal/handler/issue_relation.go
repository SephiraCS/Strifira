package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/service"
	"github.com/ku/ku-backend/pkg/response"
	"github.com/ku/ku-backend/pkg/validate"
	"github.com/labstack/echo/v4"
	log "github.com/sirupsen/logrus"
)

type IssueRelationHandler struct {
	relationSvc *service.IssueRelationService
}

func NewIssueRelationHandler(relationSvc *service.IssueRelationService) *IssueRelationHandler {
	return &IssueRelationHandler{relationSvc: relationSvc}
}

func (h *IssueRelationHandler) Create(c echo.Context) error {
	var req dto.CreateIssueRelationRequest
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
	identifier := c.Param("identifier")

	rel, err := h.relationSvc.Create(c.Request().Context(), ws.ID, identifier, req)
	if err != nil {
		if errors.Is(err, service.ErrInvalidRelation) {
			return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		}
		if errors.Is(err, service.ErrRelationExists) {
			return response.Error(c, http.StatusConflict, "CONFLICT", "This relation already exists")
		}
		log.WithError(err).WithField("workspace_id", ws.ID).Error("issue relation creation failed")
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusCreated, toIssueRelationResponse(*rel))
}

func (h *IssueRelationHandler) List(c echo.Context) error {
	ws := c.Get("workspace").(*domain.Workspace)
	identifier := c.Param("identifier")

	relations, err := h.relationSvc.ListByIssue(c.Request().Context(), ws.ID, identifier)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	}

	resp := make([]dto.IssueRelationResponse, len(relations))
	for i, rel := range relations {
		resp[i] = toIssueRelationResponse(rel)
	}

	return response.Success(c, http.StatusOK, resp)
}

func (h *IssueRelationHandler) Delete(c echo.Context) error {
	id, err := uuid.Parse(c.Param("relationId"))
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid relation ID")
	}

	ws := c.Get("workspace").(*domain.Workspace)
	if err := h.relationSvc.Delete(c.Request().Context(), ws.ID, id); err != nil {
		if errors.Is(err, service.ErrRelationNotFound) {
			return response.NotFound(c, "Relation")
		}
		return response.InternalError(c)
	}

	return response.Success(c, http.StatusOK, map[string]string{"status": "deleted"})
}

func toIssueRelationResponse(rel domain.IssueRelation) dto.IssueRelationResponse {
	resp := dto.IssueRelationResponse{
		ID:             rel.ID.String(),
		IssueID:        rel.IssueID.String(),
		RelatedIssueID: rel.RelatedIssueID.String(),
		Type:           string(rel.Type),
		CreatedAt:      rel.CreatedAt,
	}
	if rel.RelatedIssue != nil {
		relatedIssue := toIssueResponse(*rel.RelatedIssue)
		resp.RelatedIssue = &relatedIssue
	}
	return resp
}
