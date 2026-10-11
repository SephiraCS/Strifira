// Package mcpserver exposes ku's ticketing surface over the Model Context
// Protocol. It serves a stateless streamable-HTTP endpoint at /mcp whose
// requests authenticate with a personal API key; tools then run with the
// key owner's identity and the same workspace RBAC as the web UI.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/middleware"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// UserSource is the user repository surface the MCP tools need beyond the
// base interface: batched lookups for assignee display names.
type UserSource interface {
	repository.UserRepo
	ListByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.User, error)
}

type Deps struct {
	Version          string
	Users            UserSource
	Workspaces       repository.WorkspaceRepo
	Teams            repository.TeamRepo
	TeamStatuses     repository.TeamStatusRepo
	Projects         repository.ProjectRepo
	Labels           repository.LabelRepo
	Cycles           repository.CycleRepo
	Templates        repository.IssueTemplateRepo
	Analytics        *repository.AnalyticsRepository
	IssueSvc         *service.IssueService
	CommentSvc       *service.CommentService
	RelationSvc      *service.IssueRelationService
	WorkspaceSvc     *service.WorkspaceService
	TeamSvc          *service.TeamService
	StatusSvc        *service.TeamStatusService
	ProjectSvc       *service.ProjectService
	LabelSvc         *service.LabelService
	NotifSvc         *service.NotificationService
	GitHubSvc        *service.GitHubService
	GitHubActionsSvc GitHubActionsToolsService
	DevBoxSvc        DevBoxToolsService
}

// NewHTTPHandler builds the /mcp endpoint. Stateless mode keeps every POST
// self-contained (no session storage, curl-friendly) and JSONResponse keeps
// replies plain JSON instead of SSE frames.
func NewHTTPHandler(deps Deps) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "ku", Version: deps.Version}, nil)
	registerTools(srv, deps)
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
			// The API listens on loopback behind the in-container Caddy and
			// the edge proxy; requests legitimately carry the public Host
			// (ku.mariogk.com). The SDK's DNS-rebinding guard would reject
			// that Host as non-loopback, but it targets browser-local
			// servers — this endpoint is remote-facing and bearer-auth'd.
			DisableLocalhostProtection: true,
		},
	)
}

type identity struct {
	userID uuid.UUID
	scopes []string
}

func authenticate(ctx context.Context) (identity, error) {
	userID, scopes, ok := middleware.APIKeyIdentity(ctx)
	if !ok {
		return identity{}, errors.New("request is not authenticated with an API key")
	}
	return identity{userID: userID, scopes: scopes}, nil
}

func (id identity) requireWriteScope() error {
	for _, s := range id.scopes {
		if s == domain.APIKeyScopeWrite {
			return nil
		}
	}
	return errors.New("this API key is read-only; create a key with write access to mutate issues")
}

type wsContext struct {
	workspace *domain.Workspace
	role      string
}

// resolveWorkspace mirrors the REST WorkspaceMembership middleware: the
// caller must be a member of the workspace addressed by slug.
func (d Deps) resolveWorkspace(ctx context.Context, slug string, userID uuid.UUID) (*wsContext, error) {
	if slug == "" {
		return nil, errors.New("workspace (slug) is required")
	}
	ws, err := d.Workspaces.GetBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("failed to load workspace %q", slug)
	}
	if ws == nil {
		return nil, fmt.Errorf("workspace %q not found", slug)
	}
	member, err := d.Workspaces.GetMember(ctx, ws.ID, userID)
	if err != nil || member == nil {
		return nil, fmt.Errorf("you are not a member of workspace %q", slug)
	}
	return &wsContext{workspace: ws, role: member.Role}, nil
}

func (d Deps) requirePermission(ctx context.Context, w *wsContext, userID uuid.UUID, perm string) error {
	if !domain.HasPermission(w.role, perm) {
		return fmt.Errorf("your role %q in workspace %q lacks permission %s", w.role, w.workspace.Slug, perm)
	}
	return nil
}

// --- output shapes (kept deliberately compact for model consumption) ---

type userOut struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

func toUserOut(u domain.User) userOut {
	return userOut{ID: u.ID.String(), Email: u.Email, Name: u.Name, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL}
}

type workspaceOut struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	Role    string `json:"role,omitempty"`
	OwnerID string `json:"owner_id,omitempty"`
}

type memberOut struct {
	userOut
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

type teamOut struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type teamStateOut struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Category  string `json:"category"`
	IsDefault bool   `json:"is_default"`
	Position  int    `json:"position"`
}

type projectOut struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	TeamID      *string    `json:"team_id,omitempty"`
	Description *string    `json:"description,omitempty"`
	TargetDate  *time.Time `json:"target_date,omitempty"`
}

type labelOut struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description *string `json:"description,omitempty"`
}

type issueOut struct {
	ID             string     `json:"id"`
	Identifier     string     `json:"identifier"`
	Title          string     `json:"title"`
	Description    *string    `json:"description,omitempty"`
	Status         string     `json:"status"`
	StatusName     string     `json:"status_name,omitempty"`
	StatusCategory string     `json:"status_category,omitempty"`
	StatusID       *string    `json:"status_id,omitempty"`
	Priority       int        `json:"priority"`
	TeamID         string     `json:"team_id"`
	TeamKey        string     `json:"team_key,omitempty"`
	ProjectID      *string    `json:"project_id,omitempty"`
	CycleID        *string    `json:"cycle_id,omitempty"`
	CreatorID      string     `json:"creator_id,omitempty"`
	ParentID       *string    `json:"parent_id,omitempty"`
	AssigneeIDs    []string   `json:"assignee_ids,omitempty"`
	Assignees      []userOut  `json:"assignees,omitempty"`
	Labels         []labelOut `json:"labels,omitempty"`
	SubIssueCount  int        `json:"sub_issue_count,omitempty"`
	SubIssueDone   int        `json:"sub_issue_done,omitempty"`
	DueDate        *time.Time `json:"due_date,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type commentOut struct {
	ID         string       `json:"id"`
	User       userOut      `json:"user,omitempty"`
	Body       string       `json:"body"`
	ParentID   *string      `json:"parent_id,omitempty"`
	ResolvedAt *time.Time   `json:"resolved_at,omitempty"`
	Replies    []commentOut `json:"replies,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
}

func toLabelOut(l domain.Label) labelOut {
	return labelOut{ID: l.ID.String(), Name: l.Name, Color: l.Color, Description: l.Description}
}

func toIssueOut(i domain.Issue, teamKey string) issueOut {
	out := issueOut{
		ID:          i.ID.String(),
		Identifier:  i.Identifier,
		Title:       i.Title,
		Description: i.Description,
		Status:      string(i.Status),
		Priority:    int(i.Priority),
		TeamID:      i.TeamID.String(),
		TeamKey:     teamKey,
		CreatorID:   i.CreatorID.String(),
		DueDate:     i.DueDate,
		CreatedAt:   i.CreatedAt,
		UpdatedAt:   i.UpdatedAt,
	}
	if i.StatusID != nil {
		out.StatusID = ptrString(i.StatusID.String())
	}
	if i.ProjectID != nil {
		out.ProjectID = ptrString(i.ProjectID.String())
	}
	if i.CycleID != nil {
		out.CycleID = ptrString(i.CycleID.String())
	}
	if i.ParentID != nil {
		out.ParentID = ptrString(i.ParentID.String())
	}
	return out
}

func ptrString(s string) *string { return &s }
