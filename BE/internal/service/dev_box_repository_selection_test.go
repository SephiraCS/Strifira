package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/stretchr/testify/require"
)

type devBoxRepositorySelectionStore struct {
	*devBoxDevelopmentFake
	selectedRepo    *domain.ForgejoRepoModel
	lookupErr       error
	lookupWorkspace uuid.UUID
	lookupName      string
	policy          *domain.DevMachineWorkspacePolicy
	scopeReads      int
	scopeWrites     int
}

func (f *devBoxRepositorySelectionStore) GetLinkedRepositoryByFullName(_ context.Context, workspaceID uuid.UUID, fullName string) (*domain.ForgejoRepoModel, error) {
	f.lookupWorkspace, f.lookupName = workspaceID, fullName
	return f.selectedRepo, f.lookupErr
}

func (f *devBoxRepositorySelectionStore) GetScopeSetting(ctx context.Context, workspaceID uuid.UUID, team, project, issue *uuid.UUID) (*domain.DevMachineScopeSetting, error) {
	f.scopeReads++
	return f.devBoxDevelopmentFake.GetScopeSetting(ctx, workspaceID, team, project, issue)
}

func (f *devBoxRepositorySelectionStore) UpsertScopeSetting(context.Context, *domain.DevMachineScopeSetting) error {
	f.scopeWrites++
	return nil
}

func (f *devBoxRepositorySelectionStore) DeleteScopeSetting(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, *uuid.UUID) error {
	f.scopeWrites++
	return nil
}

func (f *devBoxRepositorySelectionStore) GetPolicy(context.Context, uuid.UUID) (*domain.DevMachineWorkspacePolicy, error) {
	return f.policy, nil
}

type devBoxRepositorySelectionFixture struct {
	service     *DevBoxService
	jobs        *devBoxStoreFake
	development *devBoxRepositorySelectionStore
	workspaceID uuid.UUID
	userID      uuid.UUID
	boxID       uuid.UUID
	request     dto.CreateDevBoxJobRequest
}

func newDevBoxRepositorySelectionFixture() *devBoxRepositorySelectionFixture {
	workspaceID, userID, boxID, issueID, defaultRepoID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	jobs := &devBoxStoreFake{box: &domain.DevBox{ID: boxID, WorkspaceID: workspaceID, CreatedByUserID: userID, Enabled: true, Providers: json.RawMessage(`[{"id":"codex","version":"1"}]`)}}
	development := &devBoxRepositorySelectionStore{
		devBoxDevelopmentFake: &devBoxDevelopmentFake{
			issue:        &domain.Issue{ID: issueID, WorkspaceID: workspaceID, TeamID: uuid.New(), Identifier: "KU-42", Title: "Implement the issue"},
			repo:         &domain.ForgejoRepoModel{ID: defaultRepoID, WorkspaceID: workspaceID, FullName: "team/saved", InstanceURL: "https://git.example.test", DefaultBranch: "main", IsActive: true},
			issueSetting: &domain.DevMachineScopeSetting{ForgejoRepoID: new(defaultRepoID), BaseBranch: new("develop")},
		},
		selectedRepo: &domain.ForgejoRepoModel{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "Team/Selected", InstanceURL: "https://git.example.test/", DefaultBranch: "trunk", IsActive: true},
	}
	service := NewDevBoxService(jobs, &devBoxMembersFake{role: domain.RoleMember}, &DevMachineService{store: development, enabled: false}, func(uuid.UUID) bool { return true })
	return &devBoxRepositorySelectionFixture{service: service, jobs: jobs, development: development, workspaceID: workspaceID, userID: userID, boxID: boxID,
		request: dto.CreateDevBoxJobRequest{IssueID: issueID.String(), Provider: "codex", RepositoryFullName: "team/selected", BaseBranch: "release/stable"}}
}

func (f *devBoxRepositorySelectionFixture) create(t *testing.T) (*domain.DevBoxJob, error) {
	t.Helper()
	return f.service.CreateJob(t.Context(), f.workspaceID, f.boxID, f.userID, f.request)
}

func TestDevBoxJobRepositorySelectionIsWorkspaceScopedAndDoesNotChangeDefaults(t *testing.T) {
	for _, branch := range []string{"release/stable", ""} {
		t.Run("branch="+branch, func(t *testing.T) {
			f := newDevBoxRepositorySelectionFixture()
			f.request.BaseBranch = branch
			job, err := f.create(t)
			require.NoError(t, err)
			require.Equal(t, f.workspaceID, f.development.lookupWorkspace)
			require.Equal(t, "team/selected", f.development.lookupName)
			require.Equal(t, f.development.selectedRepo.ID, job.ForgejoRepoID)
			require.Equal(t, "Team/Selected", job.RepositoryFullName)
			require.Equal(t, "https://git.example.test/Team/Selected", job.RepositoryURL)
			if branch == "" {
				require.Equal(t, "trunk", job.BaseBranch)
			} else {
				require.Equal(t, branch, job.BaseBranch)
			}
			require.Contains(t, job.Prompt, "Repository: Team/Selected")
			require.Contains(t, job.Prompt, "Base branch: "+job.BaseBranch)
			require.Zero(t, f.development.scopeReads)
			require.Zero(t, f.development.scopeWrites)
			require.Equal(t, f.development.repo.ID, *f.development.issueSetting.ForgejoRepoID)
			require.Equal(t, "develop", *f.development.issueSetting.BaseBranch)
		})
	}
}

func TestDevBoxJobRepositorySelectionRetainsSavedDefaults(t *testing.T) {
	f := newDevBoxRepositorySelectionFixture()
	f.request.RepositoryFullName, f.request.BaseBranch = "", ""
	job, err := f.create(t)
	require.NoError(t, err)
	require.Equal(t, f.development.repo.ID, job.ForgejoRepoID)
	require.Equal(t, "develop", job.BaseBranch)
	require.Empty(t, f.development.lookupName)
	require.Positive(t, f.development.scopeReads)
	require.Zero(t, f.development.scopeWrites)

	f.development.issueSetting.BaseBranch = nil
	job, err = f.create(t)
	require.NoError(t, err)
	require.Equal(t, "main", job.BaseBranch)
}

func TestDevBoxJobRepositorySelectionRejectsUnavailableOrInvalidLinks(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*devBoxRepositorySelectionFixture)
	}{
		{"unlinked", func(f *devBoxRepositorySelectionFixture) { f.development.selectedRepo = nil }},
		{"foreign workspace", func(f *devBoxRepositorySelectionFixture) { f.development.selectedRepo.WorkspaceID = uuid.New() }},
		{"inactive", func(f *devBoxRepositorySelectionFixture) { f.development.selectedRepo.IsActive = false }},
		{"wrong repository", func(f *devBoxRepositorySelectionFixture) { f.development.selectedRepo.FullName = "team/other" }},
		{"credential URL", func(f *devBoxRepositorySelectionFixture) {
			f.development.selectedRepo.InstanceURL = "https://secret@git.example.test"
		}},
		{"invalid linked branch", func(f *devBoxRepositorySelectionFixture) {
			f.request.BaseBranch = ""
			f.development.selectedRepo.DefaultBranch = "bad..branch"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newDevBoxRepositorySelectionFixture()
			test.change(f)
			_, err := f.create(t)
			require.ErrorIs(t, err, ErrInvalidDevBoxInput)
			require.Nil(t, f.jobs.job)
			require.Zero(t, f.development.scopeWrites)
		})
	}
	f := newDevBoxRepositorySelectionFixture()
	f.development.lookupErr = errors.New("lookup unavailable")
	_, err := f.create(t)
	require.ErrorIs(t, err, f.development.lookupErr)
	require.Nil(t, f.jobs.job)
}

func TestDevBoxJobRepositorySelectionRejectsMalformedInput(t *testing.T) {
	for _, name := range []string{"https://git.example.test/team/repo", "team/repo/extra", "../repo", "team/repo ", "team/"} {
		t.Run("repository="+name, func(t *testing.T) {
			f := newDevBoxRepositorySelectionFixture()
			f.request.RepositoryFullName = name
			_, err := f.create(t)
			require.ErrorIs(t, err, ErrInvalidDevBoxInput)
			require.Empty(t, f.development.lookupName)
			require.Nil(t, f.jobs.job)
		})
	}
	for _, branch := range []string{"bad..branch", "--option", "@", "feature/.hidden", "null\x00branch", string([]byte{0xff}), strings.Repeat("b", 256)} {
		t.Run("branch="+branch, func(t *testing.T) {
			f := newDevBoxRepositorySelectionFixture()
			f.request.BaseBranch = branch
			_, err := f.create(t)
			require.ErrorIs(t, err, ErrInvalidDevBoxInput)
			require.Empty(t, f.development.lookupName)
			require.Nil(t, f.jobs.job)
		})
	}
	f := newDevBoxRepositorySelectionFixture()
	f.request.RepositoryFullName = ""
	_, err := f.create(t)
	require.ErrorIs(t, err, ErrInvalidDevBoxInput)
	require.Zero(t, f.development.scopeReads)
}

func TestDevBoxJobRepositorySelectionEnforcesDevelopmentPolicy(t *testing.T) {
	for _, policy := range []*domain.DevMachineWorkspacePolicy{
		{AllowedRepositories: json.RawMessage(`["team/saved"]`), AllowedProviders: json.RawMessage(`["codex"]`), MaxRuntimeMinutes: 60},
		{AllowedRepositories: json.RawMessage(`["team/selected"]`), AllowedProviders: json.RawMessage(`["claude-code"]`), MaxRuntimeMinutes: 60},
		{AllowedRepositories: json.RawMessage(`["team/selected"]`), AllowedProviders: json.RawMessage(`["codex"]`), MaxRuntimeMinutes: 1},
	} {
		f := newDevBoxRepositorySelectionFixture()
		f.development.policy = policy
		_, err := f.create(t)
		require.ErrorIs(t, err, ErrDevBoxConflict)
		require.Nil(t, f.jobs.job)
		require.Zero(t, f.development.scopeWrites)
	}
	f := newDevBoxRepositorySelectionFixture()
	f.development.policy = &domain.DevMachineWorkspacePolicy{AllowedRepositories: json.RawMessage(`["team/selected"]`), AllowedProviders: json.RawMessage(`["codex"]`), MaxRuntimeMinutes: 60}
	_, err := f.create(t)
	require.NoError(t, err)
}
