package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/SephiraCS/strifira-backend/pkg/sanitize"
	"github.com/google/uuid"
)

type ProjectService struct {
	projectRepo repository.ProjectRepo
	hub         *realtime.Hub
}

var ErrInvalidProjectName = errors.New("project name must contain between 1 and 200 characters")

func normalizeProjectName(name string) (string, error) {
	name = strings.TrimSpace(sanitize.StripHTML(name))
	if length := utf8.RuneCountInString(name); length == 0 || length > 200 {
		return "", ErrInvalidProjectName
	}
	return name, nil
}

// NewProjectService builds the project service. hub may be nil; when set,
// successful updates are broadcast workspace-wide as project.updated (which
// also feeds outgoing webhooks subscribed to that event).
func NewProjectService(projectRepo repository.ProjectRepo, hub *realtime.Hub) *ProjectService {
	return &ProjectService{projectRepo: projectRepo, hub: hub}
}

func (s *ProjectService) Create(ctx context.Context, workspaceID uuid.UUID, req dto.CreateProjectRequest) (*domain.Project, error) {
	name, err := normalizeProjectName(req.Name)
	if err != nil {
		return nil, err
	}
	req.Name = name
	if req.Description != nil {
		clean := sanitize.SanitizeHTML(*req.Description)
		req.Description = &clean
	}

	project := &domain.Project{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Name:        req.Name,
		Description: req.Description,
		Status:      domain.ProjectStatusPlanned,
	}
	if req.TeamID != nil {
		tid, _ := uuid.Parse(*req.TeamID)
		project.TeamID = &tid
	}
	if req.LeadID != nil {
		lid, _ := uuid.Parse(*req.LeadID)
		project.LeadID = &lid
	}
	if err := s.projectRepo.Create(ctx, project); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *ProjectService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error) {
	return s.projectRepo.GetByID(ctx, id)
}

func (s *ProjectService) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.Project, error) {
	return s.projectRepo.ListByWorkspace(ctx, workspaceID)
}

func (s *ProjectService) ListByTeam(ctx context.Context, teamID uuid.UUID) ([]domain.Project, error) {
	return s.projectRepo.ListByTeam(ctx, teamID)
}

func (s *ProjectService) Update(ctx context.Context, workspaceID, id uuid.UUID, req dto.UpdateProjectRequest) (*domain.Project, error) {
	project, err := s.projectRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if project == nil || project.WorkspaceID != workspaceID {
		return nil, sql.ErrNoRows
	}

	if req.Name != nil {
		clean, err := normalizeProjectName(*req.Name)
		if err != nil {
			return nil, err
		}
		project.Name = clean
	}
	if req.Description != nil {
		clean := sanitize.SanitizeHTML(*req.Description)
		req.Description = &clean
		project.Description = req.Description
	}
	if req.Status != nil {
		project.Status = domain.ProjectStatus(*req.Status)
	}
	if req.TeamID != nil {
		tid, _ := uuid.Parse(*req.TeamID)
		project.TeamID = &tid
	}
	if req.LeadID != nil {
		lid, _ := uuid.Parse(*req.LeadID)
		project.LeadID = &lid
	}
	if req.SortOrder != nil {
		project.SortOrder = *req.SortOrder
	}

	if err := s.projectRepo.Update(ctx, project); err != nil {
		return nil, err
	}
	// Broadcast from the service so every caller (HTTP and MCP) emits it.
	if s.hub != nil {
		s.hub.Broadcast(workspaceID, realtime.Event{Type: "project.updated", Payload: project})
	}
	return project, nil
}

func (s *ProjectService) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	project, err := s.projectRepo.GetByID(ctx, id)
	if err != nil || project == nil {
		return sql.ErrNoRows
	}
	if project.WorkspaceID != workspaceID {
		return sql.ErrNoRows
	}
	return s.projectRepo.Delete(ctx, id)
}

func (s *ProjectService) GetStats(ctx context.Context, projectID uuid.UUID) (*dto.ProjectProgressResponse, error) {
	total, completed, cancelled, err := s.projectRepo.IssueStats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &dto.ProjectProgressResponse{
		Total:     total,
		Completed: completed,
		Cancelled: cancelled,
	}, nil
}
