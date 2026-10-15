package service

import (
	"context"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mock ---

type mockIssueRelationRepo struct {
	mock.Mock
}

func (m *mockIssueRelationRepo) Create(ctx context.Context, rel *domain.IssueRelation) error {
	args := m.Called(ctx, rel)
	return args.Error(0)
}

func (m *mockIssueRelationRepo) ListByIssue(ctx context.Context, issueID uuid.UUID) ([]domain.IssueRelation, error) {
	args := m.Called(ctx, issueID)
	return args.Get(0).([]domain.IssueRelation), args.Error(1)
}

func (m *mockIssueRelationRepo) ListByIssues(ctx context.Context, issueIDs []uuid.UUID) ([]domain.IssueRelation, error) {
	args := m.Called(ctx, issueIDs)
	return args.Get(0).([]domain.IssueRelation), args.Error(1)
}

func (m *mockIssueRelationRepo) Delete(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mockIssueRelationRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.IssueRelation, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.IssueRelation), args.Error(1)
}

func (m *mockIssueRelationRepo) DeleteByIssues(ctx context.Context, issueID, relatedIssueID uuid.UUID, relType domain.IssueRelationType) error {
	args := m.Called(ctx, issueID, relatedIssueID, relType)
	return args.Error(0)
}

func (m *mockIssueRelationRepo) CreateWithInverse(ctx context.Context, rel, inverse *domain.IssueRelation) error {
	args := m.Called(ctx, rel, inverse)
	return args.Error(0)
}

func (m *mockIssueRelationRepo) DeleteInWorkspace(ctx context.Context, workspaceID, id uuid.UUID) (bool, error) {
	args := m.Called(ctx, workspaceID, id)
	return args.Bool(0), args.Error(1)
}

// --- Tests ---

func TestIssueRelationService_Create_SelfReference(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	wsID := uuid.New()
	issue := &domain.Issue{ID: uuid.New(), Identifier: "ENG-1"}

	issueRepo.On("GetByIdentifier", ctx, wsID, "ENG-1").Return(issue, nil)

	_, err := svc.Create(ctx, wsID, "ENG-1", dto.CreateIssueRelationRequest{
		RelatedIdentifier: "ENG-1",
		Type:              "related",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot relate an issue to itself")
}

func TestIssueRelationService_Create_IssueNotFound(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	wsID := uuid.New()

	issueRepo.On("GetByIdentifier", ctx, wsID, "ENG-999").Return(nil, nil)

	_, err := svc.Create(ctx, wsID, "ENG-999", dto.CreateIssueRelationRequest{
		RelatedIdentifier: "ENG-2",
		Type:              "related",
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "issue not found")
}

func TestIssueRelationService_ListByIssue(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	wsID := uuid.New()
	issueID := uuid.New()
	relatedID := uuid.New()
	issue := &domain.Issue{ID: issueID, WorkspaceID: wsID, Identifier: "ENG-1"}
	related := &domain.Issue{ID: relatedID, WorkspaceID: wsID, Identifier: "ENG-2", Title: "Related issue"}

	issueRepo.On("GetByIdentifier", ctx, wsID, "ENG-1").Return(issue, nil)
	relations := []domain.IssueRelation{
		{ID: uuid.New(), IssueID: issueID, RelatedIssueID: relatedID, Type: domain.RelationRelated},
	}
	relRepo.On("ListByIssue", ctx, issueID).Return(relations, nil)
	issueRepo.On("GetByID", ctx, relatedID).Return(related, nil)

	result, err := svc.ListByIssue(ctx, wsID, "ENG-1")

	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "ENG-2", result[0].RelatedIssue.Identifier)
}

func TestIssueRelationService_ListByIssue_NormalizesInverseRows(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	wsID := uuid.New()
	issueID := uuid.New()
	otherID := uuid.New()
	issue := &domain.Issue{ID: issueID, WorkspaceID: wsID, Identifier: "ENG-1"}
	other := &domain.Issue{ID: otherID, WorkspaceID: wsID, Identifier: "ENG-2", Title: "Other issue"}
	created := uuid.New()

	issueRepo.On("GetByIdentifier", ctx, wsID, "ENG-1").Return(issue, nil)
	relations := []domain.IssueRelation{
		{ID: created, IssueID: issueID, RelatedIssueID: otherID, Type: domain.RelationBlocking},
		{ID: uuid.New(), IssueID: otherID, RelatedIssueID: issueID, Type: domain.RelationBlockedBy},
	}
	relRepo.On("ListByIssue", ctx, issueID).Return(relations, nil)
	issueRepo.On("GetByID", ctx, otherID).Return(other, nil)

	result, err := svc.ListByIssue(ctx, wsID, "ENG-1")

	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, created, result[0].ID)
	assert.Equal(t, issueID, result[0].IssueID)
	assert.Equal(t, otherID, result[0].RelatedIssueID)
	assert.Equal(t, domain.RelationBlocking, result[0].Type)
	assert.Equal(t, "ENG-2", result[0].RelatedIssue.Identifier)
}

func TestIssueRelationService_CountByIssues_NormalizesAndDeduplicates(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	issueA := uuid.New()
	issueB := uuid.New()
	relations := []domain.IssueRelation{
		{ID: uuid.New(), IssueID: issueA, RelatedIssueID: issueB, Type: domain.RelationBlocking},
		{ID: uuid.New(), IssueID: issueB, RelatedIssueID: issueA, Type: domain.RelationBlockedBy},
	}
	relRepo.On("ListByIssues", ctx, []uuid.UUID{issueA, issueB}).Return(relations, nil)
	issueRepo.On("GetByID", ctx, issueA).Return(&domain.Issue{ID: issueA, Identifier: "ENG-1"}, nil)
	issueRepo.On("GetByID", ctx, issueB).Return(&domain.Issue{ID: issueB, Identifier: "ENG-2"}, nil)

	counts, err := svc.CountByIssues(ctx, []uuid.UUID{issueA, issueB})

	assert.NoError(t, err)
	assert.Equal(t, 1, counts[issueA].Blocking)
	assert.Equal(t, 0, counts[issueA].BlockedBy)
	assert.Equal(t, 1, counts[issueB].BlockedBy)
	assert.Equal(t, 0, counts[issueB].Blocking)
}

func TestIssueRelationService_SummariesByIssues_IncludesBlockingIssues(t *testing.T) {
	relRepo := new(mockIssueRelationRepo)
	issueRepo := new(mockIssueRepo)
	svc := NewIssueRelationService(relRepo, issueRepo)

	ctx := context.Background()
	issueA := uuid.New()
	issueB := uuid.New()
	issueC := uuid.New()
	issueD := uuid.New()
	relations := []domain.IssueRelation{
		{ID: uuid.New(), IssueID: issueA, RelatedIssueID: issueC, Type: domain.RelationRelated},
		{ID: uuid.New(), IssueID: issueA, RelatedIssueID: issueB, Type: domain.RelationBlockedBy},
		{ID: uuid.New(), IssueID: issueA, RelatedIssueID: issueD, Type: domain.RelationDuplicate},
	}
	relRepo.On("ListByIssues", ctx, []uuid.UUID{issueA}).Return(relations, nil)
	issueRepo.On("GetByID", ctx, issueB).Return(&domain.Issue{ID: issueB, Identifier: "ENG-2", Title: "Blocks A"}, nil)
	issueRepo.On("GetByID", ctx, issueC).Return(&domain.Issue{ID: issueC, Identifier: "ENG-3", Title: "Related to A"}, nil)
	issueRepo.On("GetByID", ctx, issueD).Return(&domain.Issue{ID: issueD, Identifier: "ENG-4", Title: "Duplicate of A"}, nil)

	summaries, err := svc.SummariesByIssues(ctx, []uuid.UUID{issueA})

	assert.NoError(t, err)
	assert.Equal(t, 1, summaries[issueA].Counts.BlockedBy)
	assert.Equal(t, 1, summaries[issueA].Counts.Related)
	assert.Equal(t, 1, summaries[issueA].Counts.Duplicate)
	assert.Len(t, summaries[issueA].BlockedBy, 1)
	assert.Len(t, summaries[issueA].Related, 1)
	assert.Len(t, summaries[issueA].Duplicate, 1)
	assert.Equal(t, "ENG-2", summaries[issueA].BlockedBy[0].Identifier)
	assert.Equal(t, "ENG-3", summaries[issueA].Related[0].Identifier)
	assert.Equal(t, "ENG-4", summaries[issueA].Duplicate[0].Identifier)
}
