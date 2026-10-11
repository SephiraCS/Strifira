package service

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/dto"
	gh "github.com/ku/ku-backend/pkg/github"
)

var (
	ErrGitHubActionRepoNotFound = errors.New("repository is not linked in this workspace")
	ErrGitHubActionJobNotFound  = errors.New("workflow job was not found in this run")
	ErrGitHubActionConnection   = errors.New("GitHub connection is unavailable or differs from the linked repository")
	ErrGitHubActionParams       = errors.New("invalid GitHub Actions parameters")
	ErrGitHubActionState        = errors.New("the run's current state does not allow this action")
)

// GitHubActionStatusFilters is the run-status filter vocabulary accepted by
// the REST and MCP surfaces. GitHub filters runs by queued, in_progress or
// completed; waiting and running are accepted aliases for the first two.
var GitHubActionStatusFilters = []string{"queued", "waiting", "running", "in_progress", "completed"}

// actionClient resolves only a stored, active repository in this workspace and
// pins it to the configured instance before any token-bearing request.
func (s *GitHubService) actionClient(ctx context.Context, workspaceID, repoID uuid.UUID) (*gh.Client, string, string, string, error) {
	repo, err := s.fRepo.GetRepoByID(ctx, repoID)
	if err != nil {
		return nil, "", "", "", err
	}
	if repo == nil || repo.WorkspaceID != workspaceID || !repo.IsActive {
		return nil, "", "", "", ErrGitHubActionRepoNotFound
	}
	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil {
		return nil, "", "", "", ErrGitHubActionConnection
	}
	instance, err := gh.NormalizeInstanceURL(conn.InstanceURL)
	linkedInstance, linkedErr := gh.NormalizeInstanceURL(repo.InstanceURL)
	if err != nil || linkedErr != nil || instance != linkedInstance || strings.TrimSpace(conn.Token) == "" {
		return nil, "", "", "", ErrGitHubActionConnection
	}
	owner, name, ok := splitRepoOwnerName(repo.FullName)
	if !ok || owner == "." || owner == ".." || name == "." || name == ".." || strings.ContainsAny(repo.FullName, "\\?#\x00\r\n") {
		return nil, "", "", "", ErrGitHubActionRepoNotFound
	}
	return gh.NewClient(instance, conn.Token), owner, name, instance, nil
}

func normalizeActionPage(page, perPage, defaultSize int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = defaultSize
	}
	if page < 1 || page > 10000 || perPage < 1 || perPage > 100 {
		return 0, 0, ErrGitHubActionParams
	}
	return page, perPage, nil
}

func (s *GitHubService) ListActionRuns(ctx context.Context, workspaceID, repoID uuid.UUID, opts gh.ActionRunOptions) (*dto.ListResponse[dto.GitHubActionRunResponse], error) {
	page, perPage, err := normalizeActionPage(opts.Page, opts.PerPage, 20)
	if err != nil {
		return nil, err
	}
	opts.Page, opts.PerPage = page, perPage
	if opts.Status != "" && !slices.Contains(GitHubActionStatusFilters, opts.Status) {
		return nil, ErrGitHubActionParams
	}
	for _, value := range []string{opts.Status, opts.Ref, opts.Event, opts.HeadSHA, opts.WorkflowID} {
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrGitHubActionParams
		}
	}
	client, owner, name, instance, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	list, err := client.ListActionRuns(ctx, owner, name, opts)
	if err != nil {
		return nil, err
	}
	result := &dto.ListResponse[dto.GitHubActionRunResponse]{
		Data: make([]dto.GitHubActionRunResponse, 0, len(list.Runs)), TotalCount: int(list.TotalCount),
		Page: page, PerPage: perPage, HasMore: int64(page)*int64(perPage) < list.TotalCount,
	}
	for _, run := range list.Runs {
		result.Data = append(result.Data, actionRunResponse(run, instance))
	}
	return result, nil
}

func (s *GitHubService) GetActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) (*dto.GitHubActionRunResponse, error) {
	if runID <= 0 {
		return nil, ErrGitHubActionParams
	}
	client, owner, name, instance, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	run, err := client.GetActionRun(ctx, owner, name, runID)
	if err != nil {
		return nil, err
	}
	result := actionRunResponse(*run, instance)
	return &result, nil
}

func (s *GitHubService) ListActionJobs(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64, page, perPage int) (*dto.ListResponse[dto.GitHubActionJobResponse], error) {
	page, perPage, err := normalizeActionPage(page, perPage, 100)
	if err != nil || runID <= 0 {
		return nil, ErrGitHubActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	jobs, err := client.ListActionJobs(ctx, owner, name, runID)
	if err != nil {
		return nil, err
	}
	start := min((page-1)*perPage, len(jobs))
	end := min(start+perPage, len(jobs))
	result := &dto.ListResponse[dto.GitHubActionJobResponse]{
		Data: make([]dto.GitHubActionJobResponse, 0, end-start), TotalCount: len(jobs), Page: page, PerPage: perPage, HasMore: end < len(jobs),
	}
	for _, job := range jobs[start:end] {
		result.Data = append(result.Data, actionJobResponse(job))
	}
	return result, nil
}

func (s *GitHubService) GetActionJob(ctx context.Context, workspaceID, repoID uuid.UUID, runID, jobID int64) (*dto.GitHubActionJobResponse, error) {
	if runID <= 0 || jobID <= 0 {
		return nil, ErrGitHubActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	job, err := findActionJob(ctx, client, owner, name, runID, jobID)
	if err != nil {
		return nil, err
	}
	result := actionJobResponse(job)
	return &result, nil
}

func (s *GitHubService) GetActionJobLog(ctx context.Context, workspaceID, repoID uuid.UUID, runID, jobID, attempt int64) (*dto.GitHubActionLogResponse, error) {
	if runID <= 0 || jobID <= 0 || attempt < 0 {
		return nil, ErrGitHubActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	job, err := findActionJob(ctx, client, owner, name, runID, jobID)
	if err != nil {
		return nil, err
	}
	// GitHub serves the latest attempt's logs only; an explicit attempt is
	// accepted when it identifies the same attempt the job is currently on.
	if attempt > 0 && job.Attempt > 0 && attempt != job.Attempt {
		return nil, ErrGitHubActionJobNotFound
	}
	log, err := client.GetActionJobLog(ctx, owner, name, jobID)
	if err != nil {
		return nil, err
	}
	return &dto.GitHubActionLogResponse{Content: log.Content, Truncated: log.Truncated, MaxBytes: log.MaxBytes}, nil
}

// GitHub scopes jobs under the run; verify both IDs before returning metadata
// or reading the separate job log endpoint.
func findActionJob(ctx context.Context, client *gh.Client, owner, name string, runID, jobID int64) (gh.ActionJob, error) {
	jobs, err := client.ListActionJobs(ctx, owner, name, runID)
	if err != nil {
		if upstream, ok := errors.AsType[*gh.APIError](err); ok && upstream.StatusCode == 404 {
			return gh.ActionJob{}, ErrGitHubActionJobNotFound
		}
		return gh.ActionJob{}, err
	}
	for _, job := range jobs {
		if job.ID == jobID && job.RunID == runID {
			return job, nil
		}
	}
	return gh.ActionJob{}, ErrGitHubActionJobNotFound
}

func (s *GitHubService) CancelActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) error {
	if runID <= 0 {
		return ErrGitHubActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return err
	}
	run, err := client.GetActionRun(ctx, owner, name, runID)
	if err != nil {
		return err
	}
	active := slices.Contains([]string{"queued", "in_progress"}, run.Status)
	if !active {
		return ErrGitHubActionState
	}
	return client.CancelActionRun(ctx, owner, name, runID)
}

func actionURL(value, instance string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return ""
	}
	origin, originErr := gh.NormalizeInstanceURL(parsed.Scheme + "://" + parsed.Host)
	base, baseErr := gh.NormalizeInstanceURL(instance)
	if originErr != nil || baseErr != nil || origin != base {
		return ""
	}
	return parsed.String()
}

func actionTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() || value.Unix() <= 0 {
		return nil
	}
	return value
}

func actionRunResponse(run gh.ActionRun, instance string) dto.GitHubActionRunResponse {
	var completedAt *time.Time
	if run.Status == "completed" && run.Conclusion != "" {
		completedAt = actionTime(&run.UpdatedAt)
	}
	return dto.GitHubActionRunResponse{
		ID: run.ID, Name: run.WorkflowID, Title: run.Title, RunNumber: run.RunNumber, Event: run.Event,
		Status: run.Status, Conclusion: run.Conclusion, WorkflowID: run.WorkflowID,
		HeadBranch: run.HeadBranch, HeadSHA: run.HeadSHA, HTMLURL: actionURL(run.HTMLURL, instance),
		CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt, StartedAt: actionTime(run.RunStartedAt), CompletedAt: completedAt,
	}
}

func actionJobResponse(job gh.ActionJob) dto.GitHubActionJobResponse {
	return dto.GitHubActionJobResponse{
		ID: job.ID, RunID: job.RunID, Name: job.Name, Status: job.Status, Conclusion: job.Conclusion,
		Attempt: job.Attempt,
	}
}
