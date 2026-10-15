package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type devBoxMembersFake struct {
	role string
	err  error
}

func (f *devBoxMembersFake) GetMember(_ context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.role == "" {
		return nil, nil
	}
	return &domain.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Role: f.role}, nil
}

type devBoxStoreFake struct {
	repository.DevBoxStore
	box         *domain.DevBox
	job         *domain.DevBoxJob
	completed   *domain.DevBoxJobResult
	createCount int
}

func (f *devBoxStoreFake) CreateBox(_ context.Context, box *domain.DevBox) error {
	f.box = box
	f.createCount++
	return nil
}
func (f *devBoxStoreFake) GetBoxByTokenHash(_ context.Context, hash string) (*domain.DevBox, error) {
	if f.box == nil || f.box.TokenHash != hash {
		return nil, repository.ErrDevBoxNotFound
	}
	return f.box, nil
}
func (f *devBoxStoreFake) GetBox(_ context.Context, workspaceID, boxID, userID uuid.UUID) (*domain.DevBox, error) {
	if f.box == nil || f.box.WorkspaceID != workspaceID || f.box.ID != boxID || f.box.CreatedByUserID != userID {
		return nil, repository.ErrDevBoxNotFound
	}
	return f.box, nil
}
func (f *devBoxStoreFake) CreateJob(_ context.Context, job *domain.DevBoxJob) error {
	f.job = job
	return nil
}
func (f *devBoxStoreFake) GetWorkerJob(_ context.Context, boxID, jobID uuid.UUID, tokenHash, hash string) (*domain.DevBoxJob, error) {
	if f.job == nil || f.job.ID != jobID || f.job.BoxID != boxID || f.job.LeaseHash != hash {
		return nil, repository.ErrDevBoxLeaseLost
	}
	if f.box == nil || f.box.TokenHash != tokenHash || tokenHash == "" {
		return nil, repository.ErrDevBoxNotFound
	}
	return f.job, nil
}
func (f *devBoxStoreFake) CompleteJob(_ context.Context, _, _ uuid.UUID, _, _ string, result *domain.DevBoxJobResult) (*domain.DevBoxJob, error) {
	f.completed = result
	f.job.Status = result.Status
	return f.job, nil
}

type devBoxDevelopmentFake struct {
	repository.DevMachineStore
	issue            *domain.Issue
	repo             *domain.GitHubRepoModel
	issueSetting     *domain.DevMachineScopeSetting
	workspaceSetting *domain.DevMachineScopeSetting
}

func (f *devBoxDevelopmentFake) GetIssueDevelopmentContext(_ context.Context, workspaceID, issueID uuid.UUID) (*domain.Issue, error) {
	if f.issue == nil || f.issue.WorkspaceID != workspaceID || f.issue.ID != issueID {
		return nil, nil
	}
	return f.issue, nil
}
func (f *devBoxDevelopmentFake) GetScopeSetting(_ context.Context, _ uuid.UUID, team, project, issue *uuid.UUID) (*domain.DevMachineScopeSetting, error) {
	if issue != nil {
		return f.issueSetting, nil
	}
	if team == nil && project == nil {
		return f.workspaceSetting, nil
	}
	return nil, nil
}
func (f *devBoxDevelopmentFake) GetLinkedRepository(_ context.Context, workspaceID, repoID uuid.UUID) (*domain.GitHubRepoModel, error) {
	if f.repo == nil || f.repo.WorkspaceID != workspaceID || f.repo.ID != repoID {
		return nil, nil
	}
	return f.repo, nil
}
func (f *devBoxDevelopmentFake) GetPolicy(context.Context, uuid.UUID) (*domain.DevMachineWorkspacePolicy, error) {
	return nil, nil
}

func TestDevBoxTokenIsDedicatedHashedAndReturnedOnlyOnce(t *testing.T) {
	store := &devBoxStoreFake{}
	members := &devBoxMembersFake{role: domain.RoleMember}
	svc := NewDevBoxService(store, members, nil, func(uuid.UUID) bool { return true })
	created, err := svc.Create(t.Context(), uuid.New(), uuid.New(), dto.CreateDevBoxRequest{Name: " strifira "})
	require.NoError(t, err)
	require.Equal(t, "strifira", created.Box.Name)
	require.True(t, validDevBoxToken(created.Token, "sfw_"))
	require.NotEqual(t, created.Token, store.box.TokenHash)
	require.Equal(t, hashDevBoxToken(created.Token), store.box.TokenHash)
	encoded, err := json.Marshal(store.box)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), created.Token)
	require.NotContains(t, string(encoded), store.box.TokenHash)
	_, err = svc.AuthenticateWorker(t.Context(), created.Token)
	require.NoError(t, err)
	for _, token := range []string{"sfw_" + strings.Repeat("a", 64), "sfw_" + strings.Repeat("z", 64), "sfw_" + strings.Repeat("a", 64), ""} {
		_, err = svc.AuthenticateWorker(t.Context(), token)
		require.ErrorIs(t, err, ErrDevBoxAuthentication)
	}
	members.role = domain.RoleGuest
	_, err = svc.AuthenticateWorker(t.Context(), created.Token)
	require.ErrorIs(t, err, ErrDevBoxAuthentication)
	_, err = svc.Create(t.Context(), store.box.WorkspaceID, store.box.CreatedByUserID, dto.CreateDevBoxRequest{Name: "denied"})
	require.ErrorIs(t, err, ErrDevBoxForbidden)
	require.Equal(t, 1, store.createCount)
	members.role = domain.RoleMember
	members.err = errors.New("membership database unavailable")
	_, err = svc.AuthenticateWorker(t.Context(), created.Token)
	require.ErrorIs(t, err, members.err)
}

func TestDevBoxJobUsesIssueDefaultsWithoutDockerAndEnforcesCreator(t *testing.T) {
	workspaceID, userID, boxID, issueID, repoID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &devBoxStoreFake{box: &domain.DevBox{ID: boxID, WorkspaceID: workspaceID, CreatedByUserID: userID, Enabled: true, Providers: json.RawMessage(`[{"id":"codex","version":"1"}]`)}}
	development := &devBoxDevelopmentFake{
		issue:            &domain.Issue{ID: issueID, WorkspaceID: workspaceID, TeamID: uuid.New(), Identifier: "STRIFIRA-27", Title: "Implement sum", Description: new("Cover negative numbers")},
		repo:             &domain.GitHubRepoModel{ID: repoID, WorkspaceID: workspaceID, FullName: "team/strifira", InstanceURL: "https://git.example.test/", DefaultBranch: "main", IsActive: true},
		issueSetting:     &domain.DevMachineScopeSetting{GitHubRepoID: new(repoID), BaseBranch: new("develop")},
		workspaceSetting: &domain.DevMachineScopeSetting{GitHubRepoID: new(uuid.New()), BaseBranch: new("wrong-default")},
	}
	svc := NewDevBoxService(store, &devBoxMembersFake{role: domain.RoleMember}, &DevMachineService{store: development, enabled: false}, func(uuid.UUID) bool { return true })
	req := dto.CreateDevBoxJobRequest{IssueID: issueID.String(), Provider: "codex", Prompt: "Use CommonJS", TestCommand: []string{"node", "sum.test.cjs"}, AcceptanceCriteria: []string{"Returns the sum"}}
	job, err := svc.CreateJob(t.Context(), workspaceID, boxID, userID, req)
	require.NoError(t, err)
	require.Equal(t, "https://git.example.test/team/strifira", job.RepositoryURL)
	require.Equal(t, "develop", job.BaseBranch)
	require.True(t, strings.HasPrefix(job.WorkingBranch, "strifira/strifira-27-"))
	require.Contains(t, job.Prompt, "Cover negative numbers")
	require.Contains(t, job.Prompt, "Use CommonJS")
	require.Contains(t, job.Prompt, "Do not push")
	require.JSONEq(t, `["node","sum.test.cjs"]`, string(job.TestCommand))
	_, err = svc.CreateJob(t.Context(), workspaceID, boxID, uuid.New(), req)
	require.ErrorIs(t, err, ErrDevBoxNotFound)
	foreignReq := req
	foreignReq.IssueID = uuid.NewString()
	_, err = svc.CreateJob(t.Context(), workspaceID, boxID, userID, foreignReq)
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	development.repo.InstanceURL = "https://credential@git.example.test"
	_, err = svc.CreateJob(t.Context(), workspaceID, boxID, userID, req)
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
}

func TestDevBoxCompletionRequiresIndependentSuccessfulTests(t *testing.T) {
	for _, test := range []struct {
		name   string
		result domain.DevBoxJobResult
		want   string
	}{
		{"verified", domain.DevBoxJobResult{Status: "succeeded", TestStatus: "passed", TestExitCode: new(0), TestsRun: []string{"node test.cjs"}}, "succeeded"},
		{"harness failure", domain.DevBoxJobResult{Status: "succeeded", ExitCode: 1, TestStatus: "passed", TestExitCode: new(0), TestsRun: []string{"node test.cjs"}}, "failed"},
		{"no independent test", domain.DevBoxJobResult{Status: "succeeded", TestStatus: "not_run"}, "failed"},
		{"missing exit result", domain.DevBoxJobResult{Status: "succeeded", TestStatus: "passed", TestsRun: []string{"node test.cjs"}}, "failed"},
		{"failed test", domain.DevBoxJobResult{Status: "succeeded", TestStatus: "failed", TestExitCode: new(1), TestsRun: []string{"node test.cjs"}}, "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lease, hash, err := newDevBoxToken("sfl_")
			require.NoError(t, err)
			boxID, jobID := uuid.New(), uuid.New()
			box := &domain.DevBox{ID: boxID, TokenHash: strings.Repeat("b", 64)}
			store := &devBoxStoreFake{box: box, job: &domain.DevBoxJob{ID: jobID, BoxID: boxID, LeaseHash: hash, Status: "running", TestCommand: json.RawMessage(`["node","test.cjs"]`)}}
			svc := NewDevBoxService(store, nil, nil, nil)
			job, err := svc.Complete(t.Context(), box, jobID, dto.DevBoxCompleteRequest{LeaseToken: lease, Result: test.result})
			require.NoError(t, err)
			require.Equal(t, test.want, job.Status)
			require.NotNil(t, store.completed.ChangedFiles)
		})
	}
}

func TestDevBoxWorkerPayloadsRejectOversizedLogsResultsAndWrongLeases(t *testing.T) {
	svc := NewDevBoxService(&devBoxStoreFake{}, nil, nil, nil)
	lease, _, err := newDevBoxToken("sfl_")
	require.NoError(t, err)
	box := &domain.DevBox{ID: uuid.New()}
	_, err = svc.AppendLog(t.Context(), box, uuid.New(), dto.DevBoxAppendLogRequest{LeaseToken: lease, Sequence: 1, Stream: "stdout", Content: strings.Repeat("é", 32769)})
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	_, err = svc.AppendLog(t.Context(), box, uuid.New(), dto.DevBoxAppendLogRequest{LeaseToken: lease, Sequence: 1, Stream: "stdout", Content: "null\x00output"})
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	_, err = svc.Start(t.Context(), box, uuid.New(), dto.DevBoxLeaseRequest{LeaseToken: "sfw_" + strings.Repeat("a", 64)})
	require.ErrorIs(t, err, ErrDevBoxLeaseLost)
	_, err = svc.Complete(t.Context(), box, uuid.New(), dto.DevBoxCompleteRequest{LeaseToken: lease, Result: domain.DevBoxJobResult{Status: "succeeded", TestStatus: "not_run", Patch: strings.Repeat("a", DevBoxMaxPatchBytes+1)}})
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	for _, result := range []domain.DevBoxJobResult{
		{Status: "failed", TestStatus: "not_run", Summary: "null\x00summary"},
		{Status: "failed", TestStatus: "not_run", Patch: "null\x00patch"},
	} {
		_, err = svc.Complete(t.Context(), box, uuid.New(), dto.DevBoxCompleteRequest{LeaseToken: lease, Result: result})
		require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	}
}
