package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerForgejoTools(s *mcp.Server, deps Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "forgejo_status",
		Title:       "Forgejo integration status",
		Description: "Report the workspace's Forgejo connection (instance URL, account, linked repositories). Read tool.",
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
		status, err := deps.ForgejoSvc.GetStatus(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read Forgejo status: %w", err)
		}
		repos := make([]string, 0, len(status.Repos))
		for _, repo := range status.Repos {
			repos = append(repos, repo.FullName)
		}
		account := ""
		if status.Account != nil {
			account = status.Account.Login
		}
		return nil, map[string]any{
			"configured":    status.Configured,
			"connected":     status.Connected,
			"global_config": status.GlobalConfig,
			"instance_url":  status.InstanceURL,
			"account":       account,
			"repos":         repos,
		}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "forgejo_link_repos",
		Title:       "Link Forgejo repositories",
		Description: "Link repositories to the workspace by full name (e.g. TerraScale/ku); ku creates a signed webhook on each. Requires an API key with write scope and the workspace:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string   `json:"workspace"`
		Repos     []string `json:"repos" jsonschema:"repository full names (owner/name)"`
	}) (*mcp.CallToolResult, any, error) {
		if len(in.Repos) == 0 {
			return nil, nil, fmt.Errorf("repos must list at least one owner/name repository")
		}
		_, w, err := writeGuard(ctx, deps, in.Workspace, "workspace:manage")
		if err != nil {
			return nil, nil, err
		}
		available, err := deps.ForgejoSvc.ListAvailableRepos(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list available repositories: %w", err)
		}
		byName := make(map[string]int64, len(available))
		for _, repo := range available {
			byName[strings.ToLower(repo.FullName)] = repo.ForgejoRepoID
		}
		ids := make([]int64, 0, len(in.Repos))
		missing := make([]string, 0)
		for _, name := range in.Repos {
			id, ok := byName[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				missing = append(missing, name)
				continue
			}
			ids = append(ids, id)
		}
		if len(missing) > 0 {
			return nil, nil, fmt.Errorf("repositories not visible to the Forgejo token: %s", strings.Join(missing, ", "))
		}
		if err := deps.ForgejoSvc.LinkRepos(ctx, w.workspace.ID, dto.LinkForgejoReposRequest{ForgejoRepoIDs: ids}); err != nil {
			return nil, nil, fmt.Errorf("failed to link repositories: %w", err)
		}
		return nil, map[string]any{"linked": in.Repos}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "forgejo_unlink_repo",
		Title:       "Unlink a Forgejo repository",
		Description: "Remove a linked repository (by full name) and delete its webhook. Requires an API key with write scope and the workspace:manage permission.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Workspace string `json:"workspace"`
		Repo      string `json:"repo" jsonschema:"repository full name (owner/name)"`
	}) (*mcp.CallToolResult, any, error) {
		_, w, err := writeGuard(ctx, deps, in.Workspace, "workspace:manage")
		if err != nil {
			return nil, nil, err
		}
		status, err := deps.ForgejoSvc.GetStatus(ctx, w.workspace.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to read Forgejo status: %w", err)
		}
		for _, repo := range status.Repos {
			if !strings.EqualFold(repo.FullName, strings.TrimSpace(in.Repo)) {
				continue
			}
			id, parseErr := uuid.Parse(repo.ID)
			if parseErr != nil {
				return nil, nil, fmt.Errorf("invalid linked repository id %q", repo.ID)
			}
			if err := deps.ForgejoSvc.UnlinkRepo(ctx, w.workspace.ID, id); err != nil {
				return nil, nil, fmt.Errorf("failed to unlink %s: %w", repo.FullName, err)
			}
			return nil, map[string]any{"unlinked": repo.FullName}, nil
		}
		return nil, nil, fmt.Errorf("repository %s is not linked (call forgejo_status for the linked list)", in.Repo)
	})
}
