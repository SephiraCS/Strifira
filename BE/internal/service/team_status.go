package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
)

// ErrStatusProjectNotFound means a project referenced by a status request
// does not exist in the caller's workspace.
var ErrStatusProjectNotFound = errors.New("project not found")

type TeamStatusService struct {
	statusRepo     repository.TeamStatusRepo
	visibilityRepo repository.ProjectStatusVisibilityRepo
	teamRepo       repository.TeamRepo
	projectRepo    repository.ProjectRepo
	hub            *realtime.Hub
}

func NewTeamStatusService(statusRepo repository.TeamStatusRepo, visibilityRepo repository.ProjectStatusVisibilityRepo, teamRepo repository.TeamRepo, projectRepo repository.ProjectRepo, hub *realtime.Hub) *TeamStatusService {
	return &TeamStatusService{statusRepo: statusRepo, visibilityRepo: visibilityRepo, teamRepo: teamRepo, projectRepo: projectRepo, hub: hub}
}

// broadcast tells every workspace client a column changed so open boards
// pick up the new name, color, icon, WIP limit or archived flag live.
func (s *TeamStatusService) broadcast(eventType string, workspaceID, teamID, statusID uuid.UUID) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(workspaceID, realtime.Event{
		Type: eventType,
		Payload: map[string]string{
			"team_id":   teamID.String(),
			"status_id": statusID.String(),
		},
	})
}

// requireTeam reports ErrTeamNotFound unless teamID is a team of workspaceID.
func (s *TeamStatusService) requireTeam(ctx context.Context, workspaceID, teamID uuid.UUID) error {
	team, err := s.teamRepo.GetByID(ctx, teamID)
	if err != nil {
		return err
	}
	if team == nil || team.WorkspaceID != workspaceID {
		return ErrTeamNotFound
	}
	return nil
}

// scopedStatus loads a status of a team in workspaceID. When teamID is not
// uuid.Nil the status must also belong to that team. Foreign or missing
// statuses report ErrTeamStatusNotFound.
func (s *TeamStatusService) scopedStatus(ctx context.Context, workspaceID, teamID, id uuid.UUID) (*domain.TeamStatus, error) {
	status, err := s.statusRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if status == nil || (teamID != uuid.Nil && status.TeamID != teamID) {
		return nil, ErrTeamStatusNotFound
	}
	if err := s.requireTeam(ctx, workspaceID, status.TeamID); err != nil {
		if errors.Is(err, ErrTeamNotFound) {
			return nil, ErrTeamStatusNotFound
		}
		return nil, err
	}
	return status, nil
}

// workspaceProjects parses project IDs and verifies every one belongs to
// workspaceID, so visibility rows are never written for foreign projects.
func (s *TeamStatusService) workspaceProjects(ctx context.Context, workspaceID uuid.UUID, raw []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(raw))
	for _, value := range raw {
		pid, err := uuid.Parse(value)
		if err != nil {
			return nil, ErrStatusProjectNotFound
		}
		project, err := s.projectRepo.GetByID(ctx, pid)
		if err != nil {
			return nil, err
		}
		if project == nil || project.WorkspaceID != workspaceID {
			return nil, ErrStatusProjectNotFound
		}
		if !slices.Contains(ids, pid) {
			ids = append(ids, pid)
		}
	}
	return ids, nil
}

func (s *TeamStatusService) List(ctx context.Context, workspaceID, teamID uuid.UUID) ([]domain.TeamStatus, error) {
	if err := s.requireTeam(ctx, workspaceID, teamID); err != nil {
		return nil, err
	}
	return s.statusRepo.ListByTeam(ctx, teamID)
}

func (s *TeamStatusService) Create(ctx context.Context, workspaceID, teamID uuid.UUID, req dto.CreateTeamStatusRequest) (*domain.TeamStatus, error) {
	if err := s.requireTeam(ctx, workspaceID, teamID); err != nil {
		return nil, err
	}
	projectIDs, err := s.workspaceProjects(ctx, workspaceID, req.ProjectIDs)
	if err != nil {
		return nil, err
	}

	pos, err := s.statusRepo.NextPosition(ctx, teamID)
	if err != nil {
		return nil, err
	}

	slug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(req.Name), " ", "_"))

	status := &domain.TeamStatus{
		ID:        uuid.New(),
		TeamID:    teamID,
		Name:      req.Name,
		Slug:      slug,
		Category:  domain.StatusCategory(req.Category),
		Color:     req.Color,
		Icon:      req.Icon,
		Position:  pos,
		WipLimit:  req.WipLimit,
		IsDefault: false,
	}

	// The status and its project visibility are stored in one transaction
	// so a failure leaves neither behind.
	if err := s.visibilityRepo.CreateStatusWithProjects(ctx, status, projectIDs); err != nil {
		return nil, err
	}

	s.broadcast("team_status.created", workspaceID, teamID, status.ID)
	return status, nil
}

// Update changes a status of a team in workspaceID. teamID may be uuid.Nil
// when the caller only knows the workspace (MCP).
func (s *TeamStatusService) Update(ctx context.Context, workspaceID, teamID, id uuid.UUID, req dto.UpdateTeamStatusRequest) (*domain.TeamStatus, error) {
	status, err := s.scopedStatus(ctx, workspaceID, teamID, id)
	if err != nil {
		return nil, err
	}
	var projectIDs []uuid.UUID
	if req.ProjectIDs != nil {
		if projectIDs, err = s.workspaceProjects(ctx, workspaceID, *req.ProjectIDs); err != nil {
			return nil, err
		}
	}

	if req.Name != nil {
		status.Name = *req.Name
	}
	// An empty string clears the value (the codebase's nullable convention).
	if req.Color != nil {
		status.Color = nil
		if *req.Color != "" {
			status.Color = req.Color
		}
	}
	if req.Icon != nil {
		status.Icon = nil
		if *req.Icon != "" {
			status.Icon = req.Icon
		}
	}
	if req.Position != nil {
		status.Position = *req.Position
	}
	if req.WipLimit != nil {
		status.WipLimit = nil
		if *req.WipLimit > 0 {
			status.WipLimit = req.WipLimit
		}
	}
	if req.Archived != nil {
		if *req.Archived {
			now := time.Now()
			status.ArchivedAt = &now
		} else {
			status.ArchivedAt = nil
		}
	}

	// The status edit and the visibility replacement commit together.
	var visibility *[]uuid.UUID
	if req.ProjectIDs != nil {
		visibility = &projectIDs
	}
	if err := s.visibilityRepo.UpdateStatusWithProjects(ctx, status, visibility); err != nil {
		return nil, err
	}

	s.broadcast("team_status.updated", workspaceID, status.TeamID, status.ID)
	return status, nil
}

func (s *TeamStatusService) ListProjectIDsForStatuses(ctx context.Context, statusIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	return s.visibilityRepo.ListProjectIDsByStatuses(ctx, statusIDs)
}

func (s *TeamStatusService) ListProjectsForStatus(ctx context.Context, statusID uuid.UUID) ([]uuid.UUID, error) {
	return s.visibilityRepo.ListProjectsForStatus(ctx, statusID)
}

// Delete removes a non-default status of a team in workspaceID. teamID may
// be uuid.Nil when the caller only knows the workspace (MCP).
func (s *TeamStatusService) Delete(ctx context.Context, workspaceID, teamID, id uuid.UUID) error {
	status, err := s.scopedStatus(ctx, workspaceID, teamID, id)
	if err != nil {
		return err
	}
	if status.IsDefault {
		return errors.New("cannot delete the default status")
	}
	if err := s.statusRepo.Delete(ctx, id); err != nil {
		return err
	}
	s.broadcast("team_status.deleted", workspaceID, status.TeamID, status.ID)
	return nil
}
