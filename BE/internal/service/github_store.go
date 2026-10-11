package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/repository"
)

// githubStore keeps integration services testable without a live database.
// The production implementation is repository.GitHubRepository.
type githubStore interface {
	ListActiveReposWithHooks(context.Context) ([]domain.GitHubRepoModel, error)
	UpdateWebhookID(context.Context, uuid.UUID, int64) error
	UpsertConnection(context.Context, *domain.GitHubConnection) error
	GetConnectionByWorkspace(context.Context, uuid.UUID) (*domain.GitHubConnection, error)
	DeleteConnection(context.Context, uuid.UUID) error
	GetWorkspaceDisable(context.Context, uuid.UUID) (*domain.GitHubWorkspaceDisable, error)
	SetWorkspaceDisable(context.Context, uuid.UUID) error
	ClearWorkspaceDisable(context.Context, uuid.UUID) error
	CreateRepo(context.Context, *domain.GitHubRepoModel) error
	ListReposByWorkspace(context.Context, uuid.UUID) ([]domain.GitHubRepoModel, error)
	GetRepoByID(context.Context, uuid.UUID) (*domain.GitHubRepoModel, error)
	GetRepoByGitHubID(context.Context, uuid.UUID, int64) (*domain.GitHubRepoModel, error)
	GetRepoByFullName(context.Context, string, string) (*domain.GitHubRepoModel, error)
	DeleteRepo(context.Context, uuid.UUID) error
	UpsertPullRequest(context.Context, *domain.GitHubPullRequest) error
	UpdatePullRequestChecks(context.Context, *domain.GitHubPullRequest, bool) error
	GetPRByRepoAndNumber(context.Context, uuid.UUID, int) (*domain.GitHubPullRequest, error)
	ListReposWithOpenPRs(context.Context) ([]domain.GitHubRepoModel, error)
	ListOpenPRsByRepo(context.Context, uuid.UUID) ([]domain.GitHubPullRequest, error)
	ListOpenPRsByHeadSHA(context.Context, uuid.UUID, string) ([]domain.GitHubPullRequest, error)
	ReplacePRIssueLinks(context.Context, *domain.GitHubPullRequest, []uuid.UUID) error
	ListIssueIDsForPR(context.Context, uuid.UUID) ([]uuid.UUID, error)
	UpsertBranch(context.Context, *domain.GitHubBranch) error
	UpsertCommit(context.Context, *domain.GitHubCommit) error
	UpsertAutoTransition(context.Context, *domain.GitHubAutoTransition) error
	IsWorkspaceMember(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	MatchWorkspaceMemberByName(context.Context, uuid.UUID, string) (*uuid.UUID, error)
	ListUserMappings(context.Context, uuid.UUID) ([]domain.GitHubUserMapping, error)
	ReplaceUserMappings(context.Context, uuid.UUID, []domain.GitHubUserMapping) error
	ApplyIssueAutomation(context.Context, *domain.Issue, *uuid.UUID, *domain.GitHubPullRequest) error
	ListAutoTransitions(context.Context, uuid.UUID) ([]domain.GitHubAutoTransition, error)
	GetAutoTransitionByEvent(context.Context, uuid.UUID, string) (*domain.GitHubAutoTransition, error)
	ListPRsWithRepoByIssue(context.Context, uuid.UUID) ([]repository.PRWithRepo, error)
	ListBranchesWithRepoByIssue(context.Context, uuid.UUID) ([]repository.BranchWithRepo, error)
	ListCommitsWithRepoByIssue(context.Context, uuid.UUID) ([]repository.CommitWithRepo, error)
}
