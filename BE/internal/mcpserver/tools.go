package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools wires every ku MCP tool. Inputs are plain structs; the SDK
// infers their JSON schema, validates arguments, and converts returned
// errors into tool-level error results (not protocol failures).
func registerTools(s *mcp.Server, deps Deps) {
	registerIdentityTools(s, deps)
	registerCatalogTools(s, deps)
	registerIssueTools(s, deps)
}

func registerIdentityTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_me",
		Title:       "Who am I",
		Description: "Return the account and scopes of the API key in use. Call this first to learn your user ID and which workspaces you can reach.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		user, err := deps.Users.GetByID(ctx, id.userID)
		if err != nil || user == nil {
			return nil, nil, fmt.Errorf("API key owner no longer exists")
		}
		return nil, map[string]any{
			"user":   toUserOut(*user),
			"scopes": id.scopes,
		}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_workspaces",
		Title:       "List workspaces",
		Description: "List every workspace the key owner belongs to, with their role in each. Use the slug in every other tool's `workspace` argument.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		workspaces, err := deps.Workspaces.ListByUser(ctx, id.userID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list workspaces")
		}
		out := make([]workspaceOut, 0, len(workspaces))
		for _, ws := range workspaces {
			role := ""
			if member, err := deps.Workspaces.GetMember(ctx, ws.ID, id.userID); err == nil && member != nil {
				role = member.Role
			}
			out = append(out, workspaceOut{Slug: ws.Slug, Name: ws.Name, ID: ws.ID.String(), Role: role, OwnerID: ws.OwnerID.String()})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_workspace",
		Title:       "Get workspace",
		Description: "Get one workspace by slug, including your own role.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace" jsonschema:"slug of the workspace"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		ws := w.workspace
		return nil, workspaceOut{Slug: ws.Slug, Name: ws.Name, ID: ws.ID.String(), Role: w.role, OwnerID: ws.OwnerID.String()}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_members",
		Title:       "List workspace members",
		Description: "List members of a workspace with their user IDs and roles. User IDs are the accepted values for assignees.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		members, err := deps.Workspaces.ListMembersWithUsers(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list members")
		}
		out := make([]memberOut, 0, len(members))
		for _, m := range members {
			out = append(out, memberOut{
				userOut: userOut{
					ID: m.UserID.String(), Email: m.Email, Name: m.Name,
					DisplayName: m.DisplayName, AvatarURL: m.AvatarURL,
				},
				Role: m.Role, CreatedAt: m.CreatedAt,
			})
		}
		return nil, out, nil
	})
}

func registerCatalogTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_teams",
		Title:       "List teams",
		Description: "List teams in a workspace. Team keys drive issue identifiers (KEY-123).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		teams, err := deps.Teams.ListByWorkspace(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list teams")
		}
		out := make([]teamOut, 0, len(teams))
		for _, t := range teams {
			out = append(out, teamOut{ID: t.ID.String(), Key: t.Key, Name: t.Name, Description: t.Description})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_team_states",
		Title:       "List team workflow states",
		Description: "List the custom workflow states of one team (by team_id or team key). Use state ids as `status_id` when creating or updating issues.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		TeamID    string `json:"team_id,omitempty"`
		TeamKey   string `json:"team_key,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		team, err := deps.findTeam(ctx, w.workspace.ID, in.TeamID, in.TeamKey)
		if err != nil {
			return nil, nil, err
		}
		states, err := deps.TeamStatuses.ListByTeam(ctx, team.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list team states")
		}
		out := make([]teamStateOut, 0, len(states))
		for _, st := range states {
			out = append(out, teamStateOut{
				ID: st.ID.String(), Name: st.Name, Slug: st.Slug,
				Category: string(st.Category), IsDefault: st.IsDefault, Position: st.Position,
			})
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_projects",
		Title:       "List projects",
		Description: "List projects in a workspace with their status and owning team.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		projects, err := deps.Projects.ListByWorkspace(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list projects")
		}
		out := make([]projectOut, 0, len(projects))
		for _, p := range projects {
			po := projectOut{
				ID: p.ID.String(), Name: p.Name, Status: string(p.Status),
				Description: p.Description, TargetDate: p.TargetDate,
			}
			if p.TeamID != nil {
				po.TeamID = ptrString(p.TeamID.String())
			}
			out = append(out, po)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_labels",
		Title:       "List labels",
		Description: "List labels in a workspace with their ids and colors.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		labels, err := deps.Labels.ListByWorkspace(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list labels")
		}
		out := make([]labelOut, 0, len(labels))
		for _, l := range labels {
			out = append(out, toLabelOut(l))
		}
		return nil, out, nil
	})
}

func registerIssueTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "list_issues",
		Title: "List issues",
		Description: "Search and list issues in a workspace. Filters are optional and combine with AND. " +
			"`team`/`project` accept ids; `status` accepts a state id or a legacy status name (backlog, todo, in_progress, in_review, done, cancelled); " +
			"`assignee` accepts a user id. Results are newest first unless sorted otherwise.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listIssuesIn) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}

		params := dto.IssueFilterParams{
			Status:     in.Status,
			Priority:   in.Priority,
			AssigneeID: in.Assignee,
			TeamID:     in.Team,
			ProjectID:  in.Project,
			LabelID:    in.Label,
			Search:     in.Search,
			Sort:       in.Sort,
			Order:      in.Order,
		}
		params.Page = in.Page
		params.PerPage = in.PerPage
		params.Defaults()

		issues, total, err := deps.IssueSvc.List(ctx, w.workspace.ID, params)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list issues")
		}

		ctxTool := ctx
		issueIDs := make([]uuid.UUID, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		teamKeys := deps.teamKeysFor(ctxTool, w.workspace.ID)
		labelsMap, _ := deps.IssueSvc.GetLabelsForIssues(ctxTool, issueIDs)
		assigneesMap, _ := deps.IssueSvc.GetAssigneesForIssues(ctxTool, issueIDs)
		usersMap, _ := deps.Users.ListByIDs(ctxTool, uniqueIDs(assigneesMap))
		statuses := deps.statusesForIssues(ctxTool, issues)

		out := make([]issueOut, 0, len(issues))
		for _, issue := range issues {
			o := toIssueOut(issue, teamKeys[issue.TeamID])
			o.Labels = labelOuts(labelsMap[issue.ID])
			o.AssigneeIDs, o.Assignees = assigneeOuts(assigneesMap[issue.ID], usersMap)
			if issue.StatusID != nil {
				applyStatusOut(&o, statuses[*issue.StatusID])
			}
			out = append(out, o)
		}
		return nil, map[string]any{
			"issues":   out,
			"total":    total,
			"page":     params.Page,
			"per_page": params.PerPage,
			"has_more": params.Page*params.PerPage < total,
		}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_issue",
		Title:       "Get issue detail",
		Description: "Get one issue by its identifier (e.g. ENG-42) with status, labels, assignees, relation summary, sub-issue counts, and its comments.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		issue, err := deps.IssueSvc.GetByIdentifier(ctx, w.workspace.ID, in.Identifier)
		if err != nil || issue == nil {
			return nil, nil, fmt.Errorf("issue %s not found in workspace %s", in.Identifier, in.Workspace)
		}

		teamKeys := deps.teamKeysFor(ctx, w.workspace.ID)
		o := toIssueOut(*issue, teamKeys[issue.TeamID])

		if labels, err := deps.IssueSvc.GetLabels(ctx, issue.ID); err == nil {
			o.Labels = labelOuts(labels)
		}
		if assignees, err := deps.IssueSvc.GetAssignees(ctx, issue.ID); err == nil && len(assignees) > 0 {
			usersMap, _ := deps.Users.ListByIDs(ctx, assignees)
			o.AssigneeIDs, o.Assignees = assigneeOuts(assignees, usersMap)
		}
		if total, done, err := deps.IssueSvc.CountSubIssues(ctx, issue.ID); err == nil {
			o.SubIssueCount, o.SubIssueDone = total, done
		}
		if issue.StatusID != nil {
			if statuses := deps.statusesForIssues(ctx, []domain.Issue{*issue}); statuses[*issue.StatusID] != nil {
				applyStatusOut(&o, statuses[*issue.StatusID])
			}
		}

		relations := map[string]any{}
		if deps.RelationSvc != nil {
			if summaries, err := deps.RelationSvc.SummariesByIssues(ctx, []uuid.UUID{issue.ID}); err == nil {
				if summary, ok := summaries[issue.ID]; ok {
					relations = map[string]any{
						"counts":     summary.Counts,
						"related":    identifiers(summary.Related),
						"blocked_by": identifiers(summary.BlockedBy),
						"blocking":   identifiers(summary.Blocking),
						"duplicate":  identifiers(summary.Duplicate),
					}
				}
			}
		}

		commentsOut := []commentOut{}
		if comments, err := deps.CommentSvc.ListByIssue(ctx, issue.ID); err == nil {
			usersByID := map[uuid.UUID]domain.User{}
			for _, c := range comments {
				if _, ok := usersByID[c.UserID]; !ok {
					if u, err := deps.Users.GetByID(ctx, c.UserID); err == nil && u != nil {
						usersByID[c.UserID] = *u
					}
				}
			}
			for _, c := range comments {
				co := commentOut{
					ID: c.ID.String(), Body: c.Body, ParentID: nil,
					ResolvedAt: c.ResolvedAt, CreatedAt: c.CreatedAt,
				}
				if c.ParentID != nil {
					co.ParentID = ptrString(c.ParentID.String())
				}
				if u, ok := usersByID[c.UserID]; ok {
					co.User = toUserOut(u)
				}
				commentsOut = append(commentsOut, co)
			}
		}

		return nil, map[string]any{
			"issue":     o,
			"relations": relations,
			"comments":  commentsOut,
		}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "create_issue",
		Title: "Create issue",
		Description: "Create an issue. `team_id` or `team_key` is required. " +
			"Use list_team_states to pick a `status_id`; without one the team's default state applies. " +
			"Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createIssueIn) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := id.requireWriteScope(); err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.requirePermission(ctx, w, id.userID, domain.PermIssueCreate); err != nil {
			return nil, nil, err
		}

		team, err := deps.findTeam(ctx, w.workspace.ID, in.TeamID, in.TeamKey)
		if err != nil {
			return nil, nil, err
		}

		dtoReq := dto.CreateIssueRequest{
			Title:       in.Title,
			TeamID:      team.ID.String(),
			ProjectID:   in.ProjectID,
			AssigneeIDs: in.AssigneeIDs,
			LabelIDs:    in.LabelIDs,
		}
		if in.Description != "" {
			dtoReq.Description = &in.Description
		}
		if in.Status != "" {
			dtoReq.Status = in.Status
		}
		if in.StatusID != "" {
			dtoReq.StatusID = &in.StatusID
		}
		if in.Priority != nil {
			dtoReq.Priority = in.Priority
		}
		if in.ParentID != "" {
			dtoReq.ParentID = &in.ParentID
		}
		if in.DueDate != "" {
			dtoReq.DueDate = &in.DueDate
		}

		issue, err := deps.IssueSvc.Create(ctx, w.workspace.ID, id.userID, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create issue: %w", err)
		}
		o := toIssueOut(*issue, team.Key)
		if labels, err := deps.IssueSvc.GetLabels(ctx, issue.ID); err == nil {
			o.Labels = labelOuts(labels)
		}
		return nil, o, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:  "update_issue",
		Title: "Update issue",
		Description: "Update an issue by identifier. Only provided fields change. " +
			"`status_id` moves the issue between workflow states; `status` accepts legacy names. Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateIssueIn) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := id.requireWriteScope(); err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.requirePermission(ctx, w, id.userID, domain.PermIssueUpdate); err != nil {
			return nil, nil, err
		}

		dtoReq := dto.UpdateIssueRequest{
			ProjectID:   in.ProjectID,
			AssigneeIDs: in.AssigneeIDs,
			LabelIDs:    in.LabelIDs,
		}
		if in.Title != nil {
			dtoReq.Title = in.Title
		}
		if in.Description != nil {
			dtoReq.Description = in.Description
		}
		if in.Status != nil {
			dtoReq.Status = in.Status
		}
		if in.StatusID != nil {
			dtoReq.StatusID = in.StatusID
		}
		if in.Priority != nil {
			dtoReq.Priority = in.Priority
		}
		if in.ParentID != nil {
			dtoReq.ParentID = in.ParentID
		}
		if in.DueDate != nil {
			dtoReq.DueDate = in.DueDate
		}

		issue, err := deps.IssueSvc.Update(ctx, w.workspace.ID, id.userID, in.Identifier, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to update issue: %w", err)
		}
		teamKeys := deps.teamKeysFor(ctx, w.workspace.ID)
		o := toIssueOut(*issue, teamKeys[issue.TeamID])
		if labels, err := deps.IssueSvc.GetLabels(ctx, issue.ID); err == nil {
			o.Labels = labelOuts(labels)
		}
		applyStatusOut(&o, deps.statusForIssue(ctx, issue))
		return nil, o, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_issue",
		Title:       "Delete issue",
		Description: "Delete an issue by identifier. Members may delete their own issues; admins and owners may delete any. Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := id.requireWriteScope(); err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		if !domain.HasPermission(w.role, domain.PermIssueDelete) {
			issue, err := deps.IssueSvc.GetByIdentifier(ctx, w.workspace.ID, in.Identifier)
			if err != nil || issue == nil {
				return nil, nil, fmt.Errorf("issue %s not found in workspace %s", in.Identifier, in.Workspace)
			}
			if issue.CreatorID != id.userID {
				return nil, nil, fmt.Errorf("members may only delete issues they created; ask an admin to delete %s", in.Identifier)
			}
		}
		if err := deps.IssueSvc.Delete(ctx, w.workspace.ID, id.userID, in.Identifier); err != nil {
			return nil, nil, fmt.Errorf("failed to delete issue: %w", err)
		}
		return nil, map[string]string{"status": "deleted", "identifier": in.Identifier}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_comments",
		Title:       "List issue comments",
		Description: "List the comment thread of an issue, newest last, with reply threading.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		issue, err := deps.IssueSvc.GetByIdentifier(ctx, w.workspace.ID, in.Identifier)
		if err != nil || issue == nil {
			return nil, nil, fmt.Errorf("issue %s not found in workspace %s", in.Identifier, in.Workspace)
		}
		comments, err := deps.CommentSvc.ListByIssue(ctx, issue.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list comments")
		}
		usersByID := map[uuid.UUID]domain.User{}
		for _, c := range comments {
			if _, ok := usersByID[c.UserID]; !ok {
				if u, err := deps.Users.GetByID(ctx, c.UserID); err == nil && u != nil {
					usersByID[c.UserID] = *u
				}
			}
		}
		out := make([]commentOut, 0, len(comments))
		for _, c := range comments {
			co := commentOut{ID: c.ID.String(), Body: c.Body, ResolvedAt: c.ResolvedAt, CreatedAt: c.CreatedAt}
			if c.ParentID != nil {
				co.ParentID = ptrString(c.ParentID.String())
			}
			if u, ok := usersByID[c.UserID]; ok {
				co.User = toUserOut(u)
			}
			out = append(out, co)
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_comment",
		Title:       "Comment on issue",
		Description: "Add a comment to an issue. Optionally reply to a comment by passing its id as `parent_id`. Requires an API key with write scope.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace  string `json:"workspace"`
		Identifier string `json:"identifier"`
		Body       string `json:"body"`
		ParentID   string `json:"parent_id,omitempty"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := authenticate(ctx)
		if err != nil {
			return nil, nil, err
		}
		if err := id.requireWriteScope(); err != nil {
			return nil, nil, err
		}
		w, err := deps.resolveWorkspace(ctx, in.Workspace, id.userID)
		if err != nil {
			return nil, nil, err
		}
		if err := deps.requirePermission(ctx, w, id.userID, domain.PermIssueCreate); err != nil {
			return nil, nil, err
		}
		issue, err := deps.IssueSvc.GetByIdentifier(ctx, w.workspace.ID, in.Identifier)
		if err != nil || issue == nil {
			return nil, nil, fmt.Errorf("issue %s not found in workspace %s", in.Identifier, in.Workspace)
		}
		dtoReq := dto.CreateCommentRequest{Body: in.Body}
		if in.ParentID != "" {
			dtoReq.ParentID = &in.ParentID
		}
		comment, err := deps.CommentSvc.Create(ctx, issue.ID, id.userID, dtoReq)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create comment: %w", err)
		}
		co := commentOut{ID: comment.ID.String(), Body: comment.Body, CreatedAt: comment.CreatedAt}
		if u, err := deps.Users.GetByID(ctx, comment.UserID); err == nil && u != nil {
			co.User = toUserOut(*u)
		}
		return nil, co, nil
	})
}

// --- input shapes ---

type listIssuesIn struct {
	Workspace string `json:"workspace"`
	Team      string `json:"team,omitempty"`
	Project   string `json:"project,omitempty"`
	Status    string `json:"status,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	Label     string `json:"label,omitempty"`
	Search    string `json:"search,omitempty"`
	Sort      string `json:"sort,omitempty" jsonschema:"one of: created_at, updated_at, priority, sort_order, status, due_date"`
	Order     string `json:"order,omitempty" jsonschema:"asc or desc"`
	Page      int    `json:"page,omitempty"`
	PerPage   int    `json:"per_page,omitempty"`
}

type createIssueIn struct {
	Workspace   string   `json:"workspace"`
	Title       string   `json:"title"`
	TeamID      string   `json:"team_id,omitempty"`
	TeamKey     string   `json:"team_key,omitempty"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status,omitempty"`
	StatusID    string   `json:"status_id,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	ProjectID   *string  `json:"project_id,omitempty"`
	AssigneeIDs []string `json:"assignee_ids,omitempty"`
	LabelIDs    []string `json:"label_ids,omitempty"`
	ParentID    string   `json:"parent_id,omitempty"`
	DueDate     string   `json:"due_date,omitempty"`
}

type updateIssueIn struct {
	Workspace   string   `json:"workspace"`
	Identifier  string   `json:"identifier"`
	Title       *string  `json:"title,omitempty"`
	Description *string  `json:"description,omitempty"`
	Status      *string  `json:"status,omitempty"`
	StatusID    *string  `json:"status_id,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
	ProjectID   *string  `json:"project_id,omitempty"`
	AssigneeIDs []string `json:"assignee_ids,omitempty"`
	LabelIDs    []string `json:"label_ids,omitempty"`
	ParentID    *string  `json:"parent_id,omitempty"`
	DueDate     *string  `json:"due_date,omitempty"`
}

// --- shared helpers ---

// findTeam resolves a team by id or by key (case-insensitive), both scoped
// to the workspace.
func (d Deps) findTeam(ctx context.Context, workspaceID uuid.UUID, teamID, teamKey string) (*domain.Team, error) {
	if teamID != "" {
		id, err := uuid.Parse(teamID)
		if err != nil {
			return nil, fmt.Errorf("team_id is not a valid UUID")
		}
		team, err := d.Teams.GetByID(ctx, id)
		if err != nil || team == nil || team.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("team %s not found in this workspace", teamID)
		}
		return team, nil
	}
	if teamKey != "" {
		teams, err := d.Teams.ListByWorkspace(ctx, workspaceID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve team key %q", teamKey)
		}
		for i := range teams {
			if strings.EqualFold(teams[i].Key, teamKey) {
				return &teams[i], nil
			}
		}
		return nil, fmt.Errorf("no team with key %q in this workspace", teamKey)
	}
	return nil, fmt.Errorf("either team_id or team_key is required")
}

func (d Deps) teamKeysFor(ctx context.Context, workspaceID uuid.UUID) map[uuid.UUID]string {
	keys := map[uuid.UUID]string{}
	teams, err := d.Teams.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return keys
	}
	for _, t := range teams {
		keys[t.ID] = t.Key
	}
	return keys
}

func (d Deps) statusesForIssues(ctx context.Context, issues []domain.Issue) map[uuid.UUID]*domain.TeamStatus {
	statusIDs := make(map[uuid.UUID]struct{})
	for _, issue := range issues {
		if issue.StatusID != nil {
			statusIDs[*issue.StatusID] = struct{}{}
		}
	}
	result := map[uuid.UUID]*domain.TeamStatus{}
	if len(statusIDs) == 0 {
		return result
	}
	ids := make([]uuid.UUID, 0, len(statusIDs))
	for id := range statusIDs {
		ids = append(ids, id)
	}
	statuses, err := d.TeamStatuses.GetByIDs(ctx, ids)
	if err != nil {
		return result
	}
	for i := range statuses {
		result[statuses[i].ID] = &statuses[i]
	}
	return result
}

func (d Deps) statusForIssue(ctx context.Context, issue *domain.Issue) *domain.TeamStatus {
	if issue == nil || issue.StatusID == nil {
		return nil
	}
	statuses := d.statusesForIssues(ctx, []domain.Issue{*issue})
	return statuses[*issue.StatusID]
}

func applyStatusOut(o *issueOut, st *domain.TeamStatus) {
	if st == nil {
		return
	}
	o.StatusName = st.Name
	o.StatusCategory = string(st.Category)
}

func labelOuts(labels []domain.Label) []labelOut {
	if len(labels) == 0 {
		return nil
	}
	out := make([]labelOut, len(labels))
	for i, l := range labels {
		out[i] = toLabelOut(l)
	}
	return out
}

func assigneeOuts(assigneeIDs []uuid.UUID, users map[uuid.UUID]domain.User) ([]string, []userOut) {
	if len(assigneeIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(assigneeIDs))
	outs := make([]userOut, 0, len(assigneeIDs))
	for _, uid := range assigneeIDs {
		ids = append(ids, uid.String())
		if u, ok := users[uid]; ok {
			outs = append(outs, toUserOut(u))
		}
	}
	return ids, outs
}

func uniqueIDs(m map[uuid.UUID][]uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	ids := make([]uuid.UUID, 0, len(m))
	for _, list := range m {
		for _, id := range list {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func identifiers(issues []domain.Issue) []string {
	if len(issues) == 0 {
		return nil
	}
	out := make([]string, len(issues))
	for i, issue := range issues {
		out[i] = issue.Identifier
	}
	return out
}
