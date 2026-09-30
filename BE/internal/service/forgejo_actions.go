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
	fj "github.com/ku/ku-backend/pkg/forgejo"
)

var (
	ErrForgejoActionRepoNotFound = errors.New("repository is not linked in this workspace")
	ErrForgejoActionConnection   = errors.New("Forgejo connection is unavailable or differs from the linked repository")
	ErrForgejoActionParams       = errors.New("invalid Forgejo Actions parameters")
	ErrForgejoActionState        = errors.New("the run's current state does not allow this action")
)

var forgejoActionStatuses = []string{"unknown", "waiting", "running", "success", "failure", "cancelled", "skipped", "blocked"}

// actionClient resolves only a stored, active repository in this workspace and
// pins it to the configured instance before any token-bearing request.
func (s *ForgejoService) actionClient(ctx context.Context, workspaceID, repoID uuid.UUID) (*fj.Client, string, string, string, error) {
	repo, err := s.fRepo.GetRepoByID(ctx, repoID)
	if err != nil {
		return nil, "", "", "", err
	}
	if repo == nil || repo.WorkspaceID != workspaceID || !repo.IsActive {
		return nil, "", "", "", ErrForgejoActionRepoNotFound
	}
	conn, err := s.resolveConnection(ctx, workspaceID)
	if err != nil {
		return nil, "", "", "", ErrForgejoActionConnection
	}
	instance, err := fj.NormalizeInstanceURL(conn.InstanceURL)
	linkedInstance, linkedErr := fj.NormalizeInstanceURL(repo.InstanceURL)
	if err != nil || linkedErr != nil || instance != linkedInstance || strings.TrimSpace(conn.Token) == "" {
		return nil, "", "", "", ErrForgejoActionConnection
	}
	owner, name, ok := splitRepoOwnerName(repo.FullName)
	if !ok || owner == "." || owner == ".." || name == "." || name == ".." || strings.ContainsAny(repo.FullName, "\\?#\x00\r\n") {
		return nil, "", "", "", ErrForgejoActionRepoNotFound
	}
	return fj.NewClient(instance, conn.Token), owner, name, instance, nil
}

func normalizeActionPage(page, perPage, defaultSize int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = defaultSize
	}
	if page < 1 || page > 10000 || perPage < 1 || perPage > 100 {
		return 0, 0, ErrForgejoActionParams
	}
	return page, perPage, nil
}

func (s *ForgejoService) ListActionRuns(ctx context.Context, workspaceID, repoID uuid.UUID, opts fj.ActionRunOptions) (*dto.ListResponse[dto.ForgejoActionRunResponse], error) {
	page, perPage, err := normalizeActionPage(opts.Page, opts.PerPage, 20)
	if err != nil {
		return nil, err
	}
	opts.Page, opts.PerPage = page, perPage
	// Forgejo's queued state is named waiting in its API.
	if opts.Status == "queued" {
		opts.Status = "waiting"
	}
	if (opts.Status != "" && !slices.Contains(forgejoActionStatuses, opts.Status)) || opts.RunNumber < 0 {
		return nil, ErrForgejoActionParams
	}
	for _, value := range []string{opts.Ref, opts.Event, opts.HeadSHA, opts.WorkflowID} {
		if len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, ErrForgejoActionParams
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
	result := &dto.ListResponse[dto.ForgejoActionRunResponse]{
		Data: make([]dto.ForgejoActionRunResponse, 0, len(list.Runs)), TotalCount: int(list.TotalCount),
		Page: page, PerPage: perPage, HasMore: int64(page)*int64(perPage) < list.TotalCount,
	}
	for _, run := range list.Runs {
		result.Data = append(result.Data, actionRunResponse(run, instance))
	}
	return result, nil
}

func (s *ForgejoService) GetActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) (*dto.ForgejoActionRunResponse, error) {
	if runID <= 0 {
		return nil, ErrForgejoActionParams
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

func (s *ForgejoService) ListActionJobs(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64, page, perPage int) (*dto.ListResponse[dto.ForgejoActionJobResponse], error) {
	page, perPage, err := normalizeActionPage(page, perPage, 100)
	if err != nil || runID <= 0 {
		return nil, ErrForgejoActionParams
	}
	client, owner, name, instance, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	jobs, err := client.ListActionJobs(ctx, owner, name, runID)
	if err != nil {
		return nil, err
	}
	start := min((page-1)*perPage, len(jobs))
	end := min(start+perPage, len(jobs))
	result := &dto.ListResponse[dto.ForgejoActionJobResponse]{
		Data: make([]dto.ForgejoActionJobResponse, 0, end-start), TotalCount: len(jobs), Page: page, PerPage: perPage, HasMore: end < len(jobs),
	}
	for _, job := range jobs[start:end] {
		result.Data = append(result.Data, actionJobResponse(job, instance))
	}
	return result, nil
}

func (s *ForgejoService) GetActionJob(ctx context.Context, workspaceID, repoID uuid.UUID, jobID int64) (*dto.ForgejoActionJobResponse, error) {
	if jobID <= 0 {
		return nil, ErrForgejoActionParams
	}
	client, owner, name, instance, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	job, err := client.GetActionJob(ctx, owner, name, jobID)
	if err != nil {
		return nil, err
	}
	result := actionJobResponse(*job, instance)
	return &result, nil
}

func (s *ForgejoService) GetActionJobLog(ctx context.Context, workspaceID, repoID uuid.UUID, jobID, attempt int64) (*dto.ForgejoActionLogResponse, error) {
	if jobID <= 0 || attempt < 0 {
		return nil, ErrForgejoActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return nil, err
	}
	log, err := client.GetActionJobLog(ctx, owner, name, jobID, attempt)
	if err != nil {
		return nil, err
	}
	return &dto.ForgejoActionLogResponse{Content: log.Content, Truncated: log.Truncated, MaxBytes: log.MaxBytes}, nil
}

func (s *ForgejoService) RerunActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) error {
	return s.mutateActionRun(ctx, workspaceID, repoID, runID, true)
}

func (s *ForgejoService) CancelActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64) error {
	return s.mutateActionRun(ctx, workspaceID, repoID, runID, false)
}

func (s *ForgejoService) mutateActionRun(ctx context.Context, workspaceID, repoID uuid.UUID, runID int64, rerun bool) error {
	if runID <= 0 {
		return ErrForgejoActionParams
	}
	client, owner, name, _, err := s.actionClient(ctx, workspaceID, repoID)
	if err != nil {
		return err
	}
	run, err := client.GetActionRun(ctx, owner, name, runID)
	if err != nil {
		return err
	}
	finished := slices.Contains([]string{"success", "failure", "cancelled", "skipped"}, run.Status)
	active := slices.Contains([]string{"waiting", "running", "blocked"}, run.Status)
	if (rerun && !finished) || (!rerun && !active) {
		return ErrForgejoActionState
	}
	if rerun {
		return client.RerunActionRun(ctx, owner, name, runID)
	}
	return client.CancelActionRun(ctx, owner, name, runID)
}

func actionURL(value, instance string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return ""
	}
	origin, originErr := fj.NormalizeInstanceURL(parsed.Scheme + "://" + parsed.Host)
	base, baseErr := fj.NormalizeInstanceURL(instance)
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

func actionConclusion(status string) string {
	if slices.Contains([]string{"success", "failure", "cancelled", "skipped"}, status) {
		return status
	}
	return ""
}

func actionRunResponse(run fj.ActionRun, instance string) dto.ForgejoActionRunResponse {
	return dto.ForgejoActionRunResponse{
		ID: run.ID, Name: run.WorkflowID, Title: run.Title, RunNumber: run.Index, Event: run.Event,
		Status: run.Status, Conclusion: actionConclusion(run.Status), WorkflowID: run.WorkflowID,
		HeadBranch: run.PrettyRef, HeadSHA: run.CommitSHA, HTMLURL: actionURL(run.HTMLURL, instance),
		CreatedAt: run.Created, UpdatedAt: run.Updated, StartedAt: actionTime(run.Started), CompletedAt: actionTime(run.Stopped),
	}
}

func actionJobResponse(job fj.ActionJob, instance string) dto.ForgejoActionJobResponse {
	result := dto.ForgejoActionJobResponse{
		ID: job.ID, RunID: job.RunID, Name: job.Name, Status: job.Status, Conclusion: actionConclusion(job.Status),
		HTMLURL: actionURL(job.HTMLURL, instance), Attempt: job.Attempt, Steps: make([]dto.ForgejoActionStepResponse, 0, len(job.Steps)),
	}
	for _, step := range job.Steps {
		started, completed := actionTime(step.Started), actionTime(step.Stopped)
		result.Steps = append(result.Steps, dto.ForgejoActionStepResponse{
			Number: step.Number, Name: step.Name, Status: step.Status, Conclusion: actionConclusion(step.Status),
			StartedAt: started, CompletedAt: completed,
		})
		if started != nil && (result.StartedAt == nil || started.Before(*result.StartedAt)) {
			result.StartedAt = started
		}
		if completed != nil && (result.CompletedAt == nil || completed.After(*result.CompletedAt)) {
			result.CompletedAt = completed
		}
	}
	if result.Conclusion == "" {
		result.CompletedAt = nil
	}
	return result
}
