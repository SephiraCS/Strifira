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
)

type TeamStatusHandler struct {
	statusSvc *service.TeamStatusService
}

func NewTeamStatusHandler(statusSvc *service.TeamStatusService) *TeamStatusHandler {
	return &TeamStatusHandler{statusSvc: statusSvc}
}

// teamStatusError maps scoped lookup failures to 404; storage failures
// become a logged 500 and remaining domain errors a 400.
func teamStatusError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrTeamNotFound):
		return response.NotFound(c, "Team")
	case errors.Is(err, service.ErrTeamStatusNotFound):
		return response.NotFound(c, "Status")
	case errors.Is(err, service.ErrStatusProjectNotFound):
		return response.NotFound(c, "Project")
	}
	return response.ServiceError(c, err)
}

func (h *TeamStatusHandler) List(c echo.Context) error {
	teamIDStr := c.Param("teamId")
	teamID, err := uuid.Parse(teamIDStr)
	// uuid.Nil is the service's "any team" wildcard (MCP only); a REST
	// path always names a real team.
	if err != nil || teamID == uuid.Nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid team ID")
	}

	ctx := c.Request().Context()
	ws := c.Get("workspace").(*domain.Workspace)
	statuses, err := h.statusSvc.List(ctx, ws.ID, teamID)
	if errors.Is(err, service.ErrTeamNotFound) {
		return response.NotFound(c, "Team")
	}
	if err != nil {
		return response.InternalError(c)
	}

	// Batch load project IDs for all statuses
	statusIDs := make([]uuid.UUID, len(statuses))
	for i, s := range statuses {
		statusIDs[i] = s.ID
	}
	projectIDsMap, _ := h.statusSvc.ListProjectIDsForStatuses(ctx, statusIDs)

	resp := make([]dto.TeamStatusResponse, len(statuses))
	for i, s := range statuses {
		resp[i] = toTeamStatusResponse(s)
		if pids, ok := projectIDsMap[s.ID]; ok && len(pids) > 0 {
			pidStrs := make([]string, len(pids))
			for j, pid := range pids {
				pidStrs[j] = pid.String()
			}
			resp[i].ProjectIDs = pidStrs
		}
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *TeamStatusHandler) Create(c echo.Context) error {
	var req dto.CreateTeamStatusRequest
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

	teamIDStr := c.Param("teamId")
	teamID, err := uuid.Parse(teamIDStr)
	// uuid.Nil is the service's "any team" wildcard (MCP only); a REST
	// path always names a real team.
	if err != nil || teamID == uuid.Nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid team ID")
	}

	ctx := c.Request().Context()
	ws := c.Get("workspace").(*domain.Workspace)
	status, err := h.statusSvc.Create(ctx, ws.ID, teamID, req)
	if err != nil {
		return teamStatusError(c, err)
	}
	resp := toTeamStatusResponse(*status)
	if pids, _ := h.statusSvc.ListProjectsForStatus(ctx, status.ID); len(pids) > 0 {
		pidStrs := make([]string, len(pids))
		for i, pid := range pids {
			pidStrs[i] = pid.String()
		}
		resp.ProjectIDs = pidStrs
	}
	return response.Success(c, http.StatusCreated, resp)
}

func (h *TeamStatusHandler) Update(c echo.Context) error {
	var req dto.UpdateTeamStatusRequest
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
	}

	idStr := c.Param("statusId")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid status ID")
	}

	teamID, err := uuid.Parse(c.Param("teamId"))
	// uuid.Nil is the service's "any team" wildcard (MCP only); a REST
	// path always names a real team.
	if err != nil || teamID == uuid.Nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid team ID")
	}

	ctx := c.Request().Context()
	ws := c.Get("workspace").(*domain.Workspace)
	status, err := h.statusSvc.Update(ctx, ws.ID, teamID, id, req)
	if err != nil {
		return teamStatusError(c, err)
	}
	resp := toTeamStatusResponse(*status)
	if pids, _ := h.statusSvc.ListProjectsForStatus(ctx, status.ID); len(pids) > 0 {
		pidStrs := make([]string, len(pids))
		for i, pid := range pids {
			pidStrs[i] = pid.String()
		}
		resp.ProjectIDs = pidStrs
	}
	return response.Success(c, http.StatusOK, resp)
}

func (h *TeamStatusHandler) Delete(c echo.Context) error {
	idStr := c.Param("statusId")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid status ID")
	}

	teamID, err := uuid.Parse(c.Param("teamId"))
	// uuid.Nil is the service's "any team" wildcard (MCP only); a REST
	// path always names a real team.
	if err != nil || teamID == uuid.Nil {
		return response.Error(c, http.StatusBadRequest, "BAD_REQUEST", "Invalid team ID")
	}

	ws := c.Get("workspace").(*domain.Workspace)
	if err := h.statusSvc.Delete(c.Request().Context(), ws.ID, teamID, id); err != nil {
		return teamStatusError(c, err)
	}
	return response.Success(c, http.StatusOK, map[string]string{"status": "deleted"})
}

func toTeamStatusResponse(s domain.TeamStatus) dto.TeamStatusResponse {
	return dto.TeamStatusResponse{
		ID:        s.ID.String(),
		TeamID:    s.TeamID.String(),
		Name:      s.Name,
		Slug:      s.Slug,
		Category:  string(s.Category),
		Color:     s.Color,
		Position:  s.Position,
		IsDefault: s.IsDefault,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}
