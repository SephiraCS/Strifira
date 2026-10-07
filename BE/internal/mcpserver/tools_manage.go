package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerManageTools covers everything else the web UI can do: workspace
// and member administration, team/workflow/project/label management,
// issue extras (relations, history, duplicate), comment moderation,
// cycles, templates, notifications, and analytics. Mutations require the
// key's write scope plus the same role permissions as the REST routes.
func registerManageTools(s *mcp.Server, deps Deps) {
	registerWorkspaceTools(s, deps)
	registerEntityTools(s, deps)
	registerIssueExtraTools(s, deps)
	registerForgejoTools(s, deps)
	registerForgejoActionsTools(s, deps)
	registerDevBoxTools(s, deps)
	registerMiscTools(s, deps)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	slug := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(slug, "-")
}

// writeGuard bundles the shared preamble of every mutating tool.
func writeGuard(ctx context.Context, deps Deps, inWorkspace string, perm string) (identity, *wsContext, error) {
	id, err := authenticate(ctx)
	if err != nil {
		return id, nil, err
	}
	if err := id.requireWriteScope(); err != nil {
		return id, nil, err
	}
	w, err := deps.resolveWorkspace(ctx, inWorkspace, id.userID)
	if err != nil {
		return id, nil, err
	}
	if perm != "" {
		if err := deps.requirePermission(ctx, w, id.userID, perm); err != nil {
			return id, w, err
		}
	}
	return id, w, nil
}

func registerWorkspaceTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_workspace",
		Title:       "Create workspace",
		Description: "Create a new workspace; you become its owner. `slug` is generated from the name when omitted. Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Name string `json:"name"`
		Slug string `json:"slug,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := id.requireWriteScope(); err != nil {
			return nil, nil, err
		}
		slug := in.Slug
		if slug == "" {
			slug = slugify(in.Name)
		}
		if slug == "" {
			return nil, nil, fmt.Errorf("cannot derive a slug from name %q; pass slug explicitly", in.Name)
		}
		ws, err := deps.WorkspaceSvc.Create(ctx, id.userID, dto.CreateWorkspaceRequest{Name: in.Name, Slug: slug})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create workspace: %w", err)
		}
		return nil, workspaceOut{Slug: ws.Slug, Name: ws.Name, ID: ws.ID.String(), Role: domain.RoleOwner, OwnerID: ws.OwnerID.String()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_workspace",
		Title:       "Update workspace",
		Description: "Rename a workspace or change its logo URL / share-link minimum role. Owner only. Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace        string  `json:"workspace"`
		Name             *string `json:"name,omitempty"`
		LogoURL          *string `json:"logo_url,omitempty"`
		ShareLinkMinRole *string `json:"share_link_min_role,omitempty" jsonschema:"owner, admin, or member"`
	}) (*mcp.CallToolResult, any, error) {
		caller, w, err := writeGuard(ctx, deps, in.Workspace, "")
		if err != nil {
			return nil, nil, err
		}
		if w.role != domain.RoleOwner {
			return nil, nil, fmt.Errorf("only the workspace owner can update workspace settings")
		}
		dtoReq := dto.UpdateWorkspaceRequest{Name: in.Name, ShareLinkMinRole: in.ShareLinkMinRole}
		if in.LogoURL != nil {
			dtoReq.LogoURL = dto.OptionalString{Value: in.LogoURL, Set: true}
		}
		ws, err := deps.WorkspaceSvc.Update(ctx, w.workspace.Slug, caller.userID, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update workspace: %w", err)
		}
		return nil, workspaceOut{Slug: ws.Slug, Name: ws.Name, ID: ws.ID.String(), Role: w.role, OwnerID: ws.OwnerID.String()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "invite_member",
		Title:       "Invite member",
		Description: "Invite a user by email to a workspace with a role (admin, member, or guest). Requires an API key with write scope and the member:invite permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Email     string `json:"email"`
		Role      string `json:"role" jsonschema:"admin, member, or guest"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermMemberInvite)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.WorkspaceSvc.InviteMember(ctx, w.workspace.ID, dto.InviteMemberRequest{Email: in.Email, Role: in.Role}); err != nil {
			return nil, nil, fmt.Errorf("failed to invite member: %w", err)
		}
		return nil, map[string]string{"status": "invited", "email": in.Email, "role": in.Role}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_member_role",
		Title:       "Update member role",
		Description: "Change a member's role (owner, admin, member, or guest) by email, name, or id. Requires an API key with write scope and the member:invite permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		User      string `json:"user" jsonschema:"member email, name, or id"`
		Role      string `json:"role" jsonschema:"owner, admin, member, or guest"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermMemberInvite)
		if err != nil {
			return nil, nil, err
		}
		member, err := deps.resolveMemberRef(ctx, w.workspace.ID, in.User)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.WorkspaceSvc.UpdateMemberRole(ctx, w.workspace.ID, member.UserID, in.Role); err != nil {
			return nil, nil, fmt.Errorf("failed to update role: %w", err)
		}
		return nil, map[string]string{"status": "updated", "user": member.Email, "role": in.Role}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "remove_member",
		Title:       "Remove member",
		Description: "Remove a member from a workspace by email, name, or id. Requires an API key with write scope and the member:invite permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		User      string `json:"user" jsonschema:"member email, name, or id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermMemberInvite)
		if err != nil {
			return nil, nil, err
		}
		member, err := deps.resolveMemberRef(ctx, w.workspace.ID, in.User)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.WorkspaceSvc.RemoveMember(ctx, w.workspace.ID, member.UserID); err != nil {
			return nil, nil, fmt.Errorf("failed to remove member: %w", err)
		}
		return nil, map[string]string{"status": "removed", "user": member.Email}, nil
	})
}

func registerEntityTools(s *mcp.Server, deps Deps) {
	// --- teams ---
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_team",
		Title:       "Create team",
		Description: "Create a team with a short uppercase KEY (drives issue identifiers like KEY-1). Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace   string  `json:"workspace"`
		Key         string  `json:"key" jsonschema:"short uppercase key, e.g. ENG"`
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.TeamSvc.Create(ctx, w.workspace.ID, id.userID, dto.CreateTeamRequest{
			Name: in.Name, Key: strings.ToUpper(in.Key), Description: in.Description,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create team: %w", err)
		}
		return nil, teamOut{ID: team.ID.String(), Key: team.Key, Name: team.Name, Description: team.Description}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_team",
		Title:       "Update team",
		Description: "Rename a team or toggle its triage/auto-close behaviours. `team` accepts key, name, or id. Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace     string  `json:"workspace"`
		Team          string  `json:"team" jsonschema:"team key, name, or id"`
		Name          *string `json:"name,omitempty"`
		Key           *string `json:"key,omitempty" jsonschema:"new team key (1-10 uppercase letters); every issue identifier is rewritten to the new key"`
		Description   *string `json:"description,omitempty"`
		TriageEnabled *bool   `json:"triage_enabled,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
		if err != nil {
			return nil, nil, err
		}
		team, err = deps.TeamSvc.Update(ctx, team.ID, dto.UpdateTeamRequest{
			Name: in.Name, Key: in.Key, Description: in.Description, TriageEnabled: in.TriageEnabled,
		})
		if err != nil {
			if errors.Is(err, repository.ErrTeamKeyCollision) {
				return nil, nil, fmt.Errorf("another team already uses identifiers under key %s; nothing was changed", strings.ToUpper(strings.TrimSpace(*in.Key)))
			}
			return nil, nil, fmt.Errorf("failed to update team: %w", err)
		}
		return nil, teamOut{ID: team.ID.String(), Key: team.Key, Name: team.Name, Description: team.Description}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_team",
		Title:       "Delete team",
		Description: "Delete a team (its issues go with it). `team` accepts key, name, or id. Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Team      string `json:"team" jsonschema:"team key, name, or id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.TeamSvc.Delete(ctx, w.workspace.ID, team.ID); err != nil {
			return nil, nil, fmt.Errorf("failed to delete team: %w", err)
		}
		return nil, map[string]string{"status": "deleted", "team": team.Key}, nil
	})

	// --- workflow states ---
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_team_state",
		Title:       "Create workflow state",
		Description: "Add a workflow state to a team (category: backlog, unstarted, started, completed, or cancelled). Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string  `json:"workspace"`
		Team      string  `json:"team" jsonschema:"team key, name, or id"`
		Name      string  `json:"name"`
		Category  string  `json:"category" jsonschema:"backlog, unstarted, started, completed, or cancelled"`
		Color     *string `json:"color,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
		if err != nil {
			return nil, nil, err
		}
		state, err := deps.StatusSvc.Create(ctx, w.workspace.ID, team.ID, dto.CreateTeamStatusRequest{Name: in.Name, Category: in.Category, Color: in.Color})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create state: %w", err)
		}
		return nil, teamStateOut{ID: state.ID.String(), Name: state.Name, Slug: state.Slug, Category: string(state.Category), IsDefault: state.IsDefault, Position: state.Position}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_team_state",
		Title:       "Update workflow state",
		Description: "Rename a workflow state, recolor it, or move its position. Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string  `json:"workspace"`
		StateID   string  `json:"state_id"`
		Name      *string `json:"name,omitempty"`
		Color     *string `json:"color,omitempty"`
		Position  *int    `json:"position,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		stateID, err := uuid.Parse(in.StateID)
		if err != nil {
			return nil, nil, fmt.Errorf("state_id must be a UUID")
		}
		state, err := deps.StatusSvc.Update(ctx, w.workspace.ID, uuid.Nil, stateID, dto.UpdateTeamStatusRequest{Name: in.Name, Color: in.Color, Position: in.Position})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update state: %w", err)
		}
		return nil, teamStateOut{ID: state.ID.String(), Name: state.Name, Slug: state.Slug, Category: string(state.Category), IsDefault: state.IsDefault, Position: state.Position}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_team_state",
		Title:       "Delete workflow state",
		Description: "Delete a workflow state by id. Requires an API key with write scope and the team:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		StateID   string `json:"state_id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermTeamManage)
		if err != nil {
			return nil, nil, err
		}
		stateID, err := uuid.Parse(in.StateID)
		if err != nil {
			return nil, nil, fmt.Errorf("state_id must be a UUID")
		}
		if err := deps.StatusSvc.Delete(ctx, w.workspace.ID, uuid.Nil, stateID); err != nil {
			return nil, nil, fmt.Errorf("failed to delete state: %w", err)
		}
		return nil, map[string]string{"status": "deleted"}, nil
	})

	// --- projects ---
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_project",
		Title:       "Create project",
		Description: "Create a project; `team` and `lead` accept friendly references (key/name/id, email/id). Requires an API key with write scope and the project:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace   string  `json:"workspace"`
		Name        string  `json:"name"`
		Description *string `json:"description,omitempty"`
		Team        string  `json:"team,omitempty" jsonschema:"team key, name, or id"`
		Lead        string  `json:"lead,omitempty" jsonschema:"member email, name, or id"`
		TargetDate  string  `json:"target_date,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermProjectManage)
		if err != nil {
			return nil, nil, err
		}
		dtoReq := dto.CreateProjectRequest{Name: in.Name, Description: in.Description, TargetDate: strPtrOrNil(in.TargetDate)}
		if in.Team != "" {
			team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
			if err != nil {
				return nil, nil, err
			}
			dtoReq.TeamID = ptrString(team.ID.String())
		}
		if in.Lead != "" {
			member, err := deps.resolveMemberRef(ctx, w.workspace.ID, in.Lead)
			if err != nil {
				return nil, nil, err
			}
			dtoReq.LeadID = ptrString(member.UserID.String())
		}
		project, err := deps.ProjectSvc.Create(ctx, w.workspace.ID, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create project: %w", err)
		}
		return nil, projectToOut(project), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_project",
		Title:       "Update project",
		Description: "Update a project (`project` by name or id): status (planned, in_progress, completed, cancelled), name, description, team, lead, target date. Requires an API key with write scope and the project:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace   string  `json:"workspace"`
		Project     string  `json:"project" jsonschema:"project name or id"`
		Name        *string `json:"name,omitempty"`
		Description *string `json:"description,omitempty"`
		Status      *string `json:"status,omitempty" jsonschema:"planned, in_progress, completed, or cancelled"`
		Team        *string `json:"team,omitempty" jsonschema:"team key, name, or id"`
		Lead        *string `json:"lead,omitempty" jsonschema:"member email, name, or id"`
		TargetDate  *string `json:"target_date,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermProjectManage)
		if err != nil {
			return nil, nil, err
		}
		project, err := deps.resolveProjectRef(ctx, w.workspace.ID, in.Project)
		if err != nil {
			return nil, nil, err
		}
		dtoReq := dto.UpdateProjectRequest{Name: in.Name, Description: in.Description, Status: in.Status, TargetDate: in.TargetDate}
		if in.Team != nil {
			team, err := deps.resolveTeam(ctx, w.workspace.ID, *in.Team)
			if err != nil {
				return nil, nil, err
			}
			dtoReq.TeamID = ptrString(team.ID.String())
		}
		if in.Lead != nil {
			member, err := deps.resolveMemberRef(ctx, w.workspace.ID, *in.Lead)
			if err != nil {
				return nil, nil, err
			}
			dtoReq.LeadID = ptrString(member.UserID.String())
		}
		updated, err := deps.ProjectSvc.Update(ctx, w.workspace.ID, project.ID, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update project: %w", err)
		}
		return nil, projectToOut(updated), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_project",
		Title:       "Delete project",
		Description: "Delete a project (`project` by name or id). Requires an API key with write scope and the project:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Project   string `json:"project" jsonschema:"project name or id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermProjectManage)
		if err != nil {
			return nil, nil, err
		}
		project, err := deps.resolveProjectRef(ctx, w.workspace.ID, in.Project)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.ProjectSvc.Delete(ctx, w.workspace.ID, project.ID); err != nil {
			return nil, nil, fmt.Errorf("failed to delete project: %w", err)
		}
		return nil, map[string]string{"status": "deleted"}, nil
	})

	// --- labels ---
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_label",
		Title:       "Create label",
		Description: "Create a label; `color` defaults to a neutral gray when omitted (hex like #ef4444). Requires an API key with write scope and the label:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace   string  `json:"workspace"`
		Name        string  `json:"name"`
		Color       string  `json:"color,omitempty"`
		Description *string `json:"description,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermLabelManage)
		if err != nil {
			return nil, nil, err
		}
		color := in.Color
		if color == "" {
			color = "#6b7280"
		}
		label, err := deps.LabelSvc.Create(ctx, w.workspace.ID, dto.CreateLabelRequest{Name: in.Name, Color: color, Description: in.Description})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create label: %w", err)
		}
		return nil, toLabelOut(*label), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_label",
		Title:       "Update label",
		Description: "Update a label (`label` by name or id): name, color, description. Requires an API key with write scope and the label:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace   string  `json:"workspace"`
		Label       string  `json:"label" jsonschema:"label name or id"`
		Name        *string `json:"name,omitempty"`
		Color       *string `json:"color,omitempty"`
		Description *string `json:"description,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermLabelManage)
		if err != nil {
			return nil, nil, err
		}
		label, err := deps.resolveLabelRef(ctx, w.workspace.ID, in.Label)
		if err != nil {
			return nil, nil, err
		}
		updated, err := deps.LabelSvc.Update(ctx, w.workspace.ID, label.ID, dto.UpdateLabelRequest{Name: in.Name, Color: in.Color, Description: in.Description})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update label: %w", err)
		}
		return nil, toLabelOut(*updated), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_label",
		Title:       "Delete label",
		Description: "Delete a label (`label` by name or id). Requires an API key with write scope and the label:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Label     string `json:"label" jsonschema:"label name or id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermLabelManage)
		if err != nil {
			return nil, nil, err
		}
		label, err := deps.resolveLabelRef(ctx, w.workspace.ID, in.Label)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.LabelSvc.Delete(ctx, w.workspace.ID, label.ID); err != nil {
			return nil, nil, fmt.Errorf("failed to delete label: %w", err)
		}
		return nil, map[string]string{"status": "deleted"}, nil
	})
}

func registerIssueExtraTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "duplicate_issue",
		Title:       "Duplicate issue",
		Description: "Duplicate an issue by identifier, optionally including its sub-issues. Requires an API key with write scope and the issue:create permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace        string `json:"workspace"`
		Identifier       string `json:"identifier"`
		IncludeSubIssues bool   `json:"include_sub_issues,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		id, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermIssueCreate)
		if err != nil {
			return nil, nil, err
		}
		issue, err := deps.IssueSvc.Duplicate(ctx, w.workspace.ID, id.userID, in.Identifier, in.IncludeSubIssues)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to duplicate issue: %w", err)
		}
		teamKeys := deps.teamKeysFor(ctx, w.workspace.ID)
		return nil, toIssueOut(*issue, teamKeys[issue.TeamID]), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_issue_history",
		Title:       "List issue history",
		Description: "List the change history of an issue (field, old and new values, who, when).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, err := deps.resolveWorkspace(ctx, in.Workspace, auth.userID)
		if err != nil {
			return nil, nil, err
		}
		issue, err := deps.IssueSvc.GetByIdentifier(ctx, ws.workspace.ID, in.Identifier)
		if err != nil || issue == nil {
			return nil, nil, fmt.Errorf("issue %s not found in workspace %s", in.Identifier, in.Workspace)
		}
		history, err := deps.IssueSvc.GetHistory(ctx, issue.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list history")
		}
		out := make([]map[string]any, 0, len(history))
		for _, h := range history {
			entry := map[string]any{
				"field":      h.Field,
				"user_id":    h.UserID.String(),
				"created_at": h.CreatedAt,
			}
			if h.OldValue != nil {
				entry["old_value"] = *h.OldValue
			}
			if h.NewValue != nil {
				entry["new_value"] = *h.NewValue
			}
			out = append(out, entry)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_relations",
		Title:       "List issue relations",
		Description: "List an issue's relations (related, blocked_by, blocking, duplicate) with the related issues.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, auth.userID)
		if err != nil {
			return nil, nil, err
		}
		if deps.RelationSvc == nil {
			return nil, []map[string]any{}, nil
		}
		relations, err := deps.RelationSvc.ListByIssue(ctx, w.workspace.ID, in.Identifier)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list relations")
		}
		out := make([]map[string]any, 0, len(relations))
		for _, rel := range relations {
			entry := map[string]any{
				"id":         rel.ID.String(),
				"type":       string(rel.Type),
				"created_at": rel.CreatedAt,
			}
			if rel.RelatedIssue != nil {
				entry["related_identifier"] = rel.RelatedIssue.Identifier
				entry["related_title"] = rel.RelatedIssue.Title
			} else {
				entry["related_issue_id"] = rel.RelatedIssueID.String()
			}
			out = append(out, entry)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_relation",
		Title:       "Relate issues",
		Description: "Link two issues: type is related, blocked_by, blocking, or duplicate. `related` accepts an issue identifier or id. Requires an API key with write scope and the issue:update permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
		Related    string `json:"related" jsonschema:"issue identifier or id"`
		Type       string `json:"type" jsonschema:"related, blocked_by, blocking, or duplicate"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermIssueUpdate)
		if err != nil {
			return nil, nil, err
		}
		if deps.RelationSvc == nil {
			return nil, nil, fmt.Errorf("relations are not available")
		}
		related, err := deps.resolveIssueRef(ctx, w.workspace.ID, in.Related)
		if err != nil {
			return nil, nil, err
		}
		rel, err := deps.RelationSvc.Create(ctx, w.workspace.ID, in.Identifier, dto.CreateIssueRelationRequest{
			RelatedIdentifier: related.Identifier, Type: in.Type,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create relation: %w", err)
		}
		return nil, map[string]string{"id": rel.ID.String(), "type": string(rel.Type), "related": related.Identifier}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_relation",
		Title:       "Delete relation",
		Description: "Remove a relation by its id (from list_relations). Requires an API key with write scope and the issue:update permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		RelationID string `json:"relation_id"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, domain.PermIssueUpdate)
		if err != nil {
			return nil, nil, err
		}
		if deps.RelationSvc == nil {
			return nil, nil, fmt.Errorf("relations are not available")
		}
		relID, err := uuid.Parse(in.RelationID)
		if err != nil {
			return nil, nil, fmt.Errorf("relation_id must be a UUID")
		}
		if err := deps.RelationSvc.Delete(ctx, w.workspace.ID, relID); err != nil {
			return nil, nil, fmt.Errorf("failed to delete relation: %w", err)
		}
		return nil, map[string]string{"status": "deleted"}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "resolve_comment",
		Title:       "Resolve comment",
		Description: "Mark a comment resolved. Requires an API key with write scope and the issue:update permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
		CommentID  string `json:"comment_id"`
	}) (*mcp.CallToolResult, any, error) {
		return commentResolve(ctx, deps, in.Workspace, in.Identifier, in.CommentID, true)
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "reopen_comment",
		Title:       "Reopen comment",
		Description: "Un-resolve a comment. Requires an API key with write scope and the issue:update permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
		CommentID  string `json:"comment_id"`
	}) (*mcp.CallToolResult, any, error) {
		return commentResolve(ctx, deps, in.Workspace, in.Identifier, in.CommentID, false)
	})
}

func commentResolve(ctx context.Context, deps Deps, workspace, identifier, commentID string, resolve bool) (*mcp.CallToolResult, any, error) {
	_, w, err := writeGuard(ctx, deps, workspace, domain.PermIssueUpdate)
	if err != nil {
		return nil, nil, err
	}
	issue, err := deps.IssueSvc.GetByIdentifier(ctx, w.workspace.ID, identifier)
	if err != nil || issue == nil {
		return nil, nil, fmt.Errorf("issue %s not found in workspace %s", identifier, workspace)
	}
	cid, err := uuid.Parse(commentID)
	if err != nil {
		return nil, nil, fmt.Errorf("comment_id must be a UUID")
	}
	comment, err := deps.CommentSvc.GetByID(ctx, cid)
	if err != nil || comment == nil || comment.IssueID != issue.ID {
		return nil, nil, fmt.Errorf("comment not found on issue %s", identifier)
	}
	if resolve {
		err = deps.CommentSvc.Resolve(ctx, cid)
	} else {
		err = deps.CommentSvc.Reopen(ctx, cid)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to update comment: %w", err)
	}
	return nil, map[string]string{"status": "resolved", "comment_id": commentID}, nil
}

func registerMiscTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_cycles",
		Title:       "List cycles",
		Description: "List a team's cycles with status (upcoming, active, completed) and date ranges.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Team      string `json:"team" jsonschema:"team key, name, or id"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, auth.userID)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
		if err != nil {
			return nil, nil, err
		}
		cycles, err := deps.Cycles.ListByTeam(ctx, team.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list cycles")
		}
		out := make([]map[string]any, 0, len(cycles))
		for _, c := range cycles {
			out = append(out, map[string]any{
				"id": c.ID.String(), "name": c.Name, "number": c.Number,
				"status": string(c.Status), "start_date": c.StartDate, "end_date": c.EndDate,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_issue_templates",
		Title:       "List issue templates",
		Description: "List the workspace's issue templates (useful as blueprints when creating issues).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, auth.userID)
		if err != nil {
			return nil, nil, err
		}
		templates, err := deps.Templates.ListByWorkspace(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list templates")
		}
		out := make([]map[string]any, 0, len(templates))
		for _, t := range templates {
			out = append(out, map[string]any{
				"id": t.ID.String(), "title": t.Title, "description": t.Description,
				"status": t.Status, "priority": t.Priority, "is_active": t.IsActive,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_notifications",
		Title:       "List my notifications",
		Description: "List your notifications (mentions, assignments, updates) with an unread count.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Limit int `json:"limit,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		limit := in.Limit
		if limit <= 0 || limit > 100 {
			limit = 25
		}
		notifications, err := deps.NotifSvc.ListByUser(ctx, auth.userID, limit, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list notifications")
		}
		unread, _ := deps.NotifSvc.UnreadCount(ctx, auth.userID)
		out := make([]map[string]any, 0, len(notifications))
		for _, n := range notifications {
			entry := map[string]any{
				"id": n.ID.String(), "type": n.Type, "title": n.Title,
				"read": n.ReadAt != nil, "created_at": n.CreatedAt,
			}
			if n.IssueIdentifier != nil {
				entry["issue"] = *n.IssueIdentifier
			}
			out = append(out, entry)
		}
		return nil, map[string]any{"notifications": out, "unread_count": unread}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "mark_notifications_read",
		Title:       "Mark notifications read",
		Description: "Mark one notification read by id, or every notification when `all` is true.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		ID  string `json:"id,omitempty"`
		All bool   `json:"all,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if in.All {
			if err := deps.NotifSvc.MarkAllRead(ctx, auth.userID); err != nil {
				return nil, nil, fmt.Errorf("failed to mark notifications read")
			}
			return nil, map[string]string{"status": "all_read"}, nil
		}
		nid, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("id must be a UUID, or pass all=true")
		}
		notification, err := deps.NotifSvc.GetByID(ctx, nid)
		if err != nil || notification == nil || notification.UserID != auth.userID {
			return nil, nil, fmt.Errorf("notification not found")
		}
		if _, err := deps.NotifSvc.MarkRead(ctx, nid); err != nil {
			return nil, nil, fmt.Errorf("failed to mark notification read")
		}
		return nil, map[string]string{"status": "read"}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "workspace_analytics",
		Title:       "Workspace analytics",
		Description: "Workspace-level analytics overview: totals, open/completed/overdue/unassigned counts, completion rate, average lead and cycle times. Optional `team` filter.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Team      string `json:"team,omitempty" jsonschema:"team key, name, or id"`
	}) (*mcp.CallToolResult, any, error) {
		auth, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, auth.userID)
		if err != nil {
			return nil, nil, err
		}
		teamID := ""
		if in.Team != "" {
			team, err := deps.resolveTeam(ctx, w.workspace.ID, in.Team)
			if err != nil {
				return nil, nil, err
			}
			teamID = team.ID.String()
		}
		if deps.Analytics == nil {
			return nil, nil, fmt.Errorf("analytics are not available")
		}
		overview, err := deps.Analytics.Overview(ctx, w.workspace.ID.String(), teamID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to compute analytics")
		}
		return nil, overview, nil
	})
}

// resolveTeam resolves a team by id, key, or name, scoped to a workspace.
func (d Deps) resolveTeam(ctx context.Context, workspaceID uuid.UUID, ref string) (*domain.Team, error) {
	idArg, keyArg := ref, ""
	if !looksLikeUUID(ref) {
		idArg, keyArg = "", ref
	}
	return d.findTeam(ctx, workspaceID, idArg, keyArg)
}

func projectToOut(p *domain.Project) projectOut {
	po := projectOut{
		ID: p.ID.String(), Name: p.Name, Status: string(p.Status),
		Description: p.Description, TargetDate: p.TargetDate,
	}
	if p.TeamID != nil {
		po.TeamID = ptrString(p.TeamID.String())
	}
	return po
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
