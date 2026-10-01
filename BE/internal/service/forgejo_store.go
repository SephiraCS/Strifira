package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/repository"
)

// forgejoStore keeps integration services testable without a live database.
// The production implementation is repository.ForgejoRepository.
type forgejoStore interface {
	ListActiveReposWithHooks(context.Context) ([]domain.ForgejoRepoModel, error)
	UpdateWebhookID(context.Context, uuid.UUID, int64) error
	UpsertConnection(context.Context, *domain.ForgejoConnection) error
	GetConnectionByWorkspace(context.Context, uuid.UUID) (*domain.ForgejoConnection, error)
	DeleteConnection(context.Context, uuid.UUID) error
	CreateRepo(context.Context, *domain.ForgejoRepoModel) error
	ListReposByWorkspace(context.Context, uuid.UUID) ([]domain.ForgejoRepoModel, error)
	GetRepoByID(context.Context, uuid.UUID) (*domain.ForgejoRepoModel, error)
	GetRepoByForgejoID(context.Context, uuid.UUID, int64) (*domain.ForgejoRepoModel, error)
	GetRepoByFullName(context.Context, string, string) (*domain.ForgejoRepoModel, error)
	DeleteRepo(context.Context, uuid.UUID) error
	UpsertPullRequest(context.Context, *domain.ForgejoPullRequest) error
	UpdatePullRequestChecks(context.Context, *domain.ForgejoPullRequest, bool) error
	GetPRByRepoAndNumber(context.Context, uuid.UUID, int) (*domain.ForgejoPullRequest, error)
	ListReposWithOpenPRs(context.Context) ([]domain.ForgejoRepoModel, error)
	ListOpenPRsByRepo(context.Context, uuid.UUID) ([]domain.ForgejoPullRequest, error)
	ListOpenPRsByHeadSHA(context.Context, uuid.UUID, string) ([]domain.ForgejoPullRequest, error)
	ReplacePRIssueLinks(context.Context, *domain.ForgejoPullRequest, []uuid.UUID) error
	ListIssueIDsForPR(context.Context, uuid.UUID) ([]uuid.UUID, error)
	UpsertBranch(context.Context, *domain.ForgejoBranch) error
	UpsertCommit(context.Context, *domain.ForgejoCommit) error
	UpsertAutoTransition(context.Context, *domain.ForgejoAutoTransition) error
	IsWorkspaceMember(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	MatchWorkspaceMemberByName(context.Context, uuid.UUID, string) (*uuid.UUID, error)
	ListUserMappings(context.Context, uuid.UUID) ([]domain.ForgejoUserMapping, error)
	ReplaceUserMappings(context.Context, uuid.UUID, []domain.ForgejoUserMapping) error
	ApplyIssueAutomation(context.Context, *domain.Issue, *uuid.UUID, *domain.ForgejoPullRequest) error
	ListAutoTransitions(context.Context, uuid.UUID) ([]domain.ForgejoAutoTransition, error)
	GetAutoTransitionByEvent(context.Context, uuid.UUID, string) (*domain.ForgejoAutoTransition, error)
	ListPRsWithRepoByIssue(context.Context, uuid.UUID) ([]repository.PRWithRepo, error)
	ListBranchesWithRepoByIssue(context.Context, uuid.UUID) ([]repository.BranchWithRepo, error)
	ListCommitsWithRepoByIssue(context.Context, uuid.UUID) ([]repository.CommitWithRepo, error)
}
