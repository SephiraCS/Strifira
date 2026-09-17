package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

// Friendly-reference resolvers: MCP tools accept human handles (names,
// emails, keys, identifiers) wherever the REST API wants UUIDs. Every
// resolver matches UUIDs exactly first, then case-insensitive names, and
// reports the valid options on a miss so the caller can self-correct.

func looksLikeUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// resolveMemberRef resolves a workspace member by user id, email, display
// name, or name.
func (d Deps) resolveMemberRef(ctx context.Context, workspaceID uuid.UUID, ref string) (*domain.WorkspaceMemberWithUser, error) {
	members, err := d.Workspaces.ListMembersWithUsers(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspace members")
	}
	return matchOne(len(members), ref, func(i int) (*domain.WorkspaceMemberWithUser, string) {
		return &members[i], members[i].UserID.String()
	}, func(m *domain.WorkspaceMemberWithUser) []string {
		return []string{m.Email, m.DisplayName, m.Name}
	}, "member")
}

// resolveLabelRef resolves a label by id or name.
func (d Deps) resolveLabelRef(ctx context.Context, workspaceID uuid.UUID, ref string) (*domain.Label, error) {
	labels, err := d.Labels.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list labels")
	}
	return matchOne(len(labels), ref, func(i int) (*domain.Label, string) {
		return &labels[i], labels[i].ID.String()
	}, func(l *domain.Label) []string { return []string{l.Name} }, "label")
}

// resolveProjectRef resolves a project by id or name.
func (d Deps) resolveProjectRef(ctx context.Context, workspaceID uuid.UUID, ref string) (*domain.Project, error) {
	projects, err := d.Projects.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects")
	}
	return matchOne(len(projects), ref, func(i int) (*domain.Project, string) {
		return &projects[i], projects[i].ID.String()
	}, func(p *domain.Project) []string { return []string{p.Name} }, "project")
}

// resolveStateRef resolves a team workflow state by id, name, or slug.
func (d Deps) resolveStateRef(ctx context.Context, teamID uuid.UUID, ref string) (*domain.TeamStatus, error) {
	states, err := d.TeamStatuses.ListByTeam(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("failed to list team states")
	}
	return matchOne(len(states), ref, func(i int) (*domain.TeamStatus, string) {
		return &states[i], states[i].ID.String()
	}, func(s *domain.TeamStatus) []string { return []string{s.Name, s.Slug} }, "workflow state")
}

// resolveIssueRef resolves an issue by identifier (e.g. ENG-42) or id,
// scoped to the workspace.
func (d Deps) resolveIssueRef(ctx context.Context, workspaceID uuid.UUID, ref string) (*domain.Issue, error) {
	if looksLikeUUID(ref) {
		id, _ := uuid.Parse(ref)
		issue, err := d.IssueSvc.GetByID(ctx, id)
		if err != nil || issue == nil || issue.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("issue %s not found in this workspace", ref)
		}
		return issue, nil
	}
	issue, err := d.IssueSvc.GetByIdentifier(ctx, workspaceID, ref)
	if err != nil || issue == nil {
		return nil, fmt.Errorf("issue %s not found in this workspace", ref)
	}
	return issue, nil
}

// matchOne implements the shared matching strategy: exact UUID/id match
// first, then unique case-insensitive name match; ambiguity and misses
// report the available options.
func matchOne[T any](n int, ref string, at func(int) (T, string), namesOf func(T) []string, kind string) (T, error) {
	var zero T
	if ref == "" {
		return zero, fmt.Errorf("empty %s reference", kind)
	}
	if looksLikeUUID(ref) {
		for i := 0; i < n; i++ {
			if item, id := at(i); id == ref {
				return item, nil
			}
		}
	}
	var candidates []string
	matchCount := 0
	var matched T
	for i := 0; i < n; i++ {
		item, id := at(i)
		for _, name := range append([]string{id}, namesOf(item)...) {
			if strings.EqualFold(name, ref) {
				matchCount++
				matched = item
				break
			}
		}
	}
	if matchCount == 1 {
		return matched, nil
	}
	if matchCount > 1 {
		return zero, fmt.Errorf("%s reference %q is ambiguous; pass the id instead", kind, ref)
	}
	for i := 0; i < n && len(candidates) < 12; i++ {
		item, id := at(i)
		if ns := namesOf(item); len(ns) > 0 && ns[0] != "" {
			candidates = append(candidates, fmt.Sprintf("%s (%s)", ns[0], id[:8]))
		}
	}
	if len(candidates) == 0 {
		return zero, fmt.Errorf("no %s matches %q", kind, ref)
	}
	return zero, fmt.Errorf("no %s matches %q; available: %s", kind, ref, strings.Join(candidates, ", "))
}
