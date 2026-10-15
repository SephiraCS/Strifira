package service

import (
	"context"
	"errors"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockUserPreferencesRepo struct {
	mock.Mock
}

func (m *mockUserPreferencesRepo) Get(ctx context.Context, userID uuid.UUID) (*domain.UserPreferences, error) {
	args := m.Called(ctx, userID)
	if prefs, ok := args.Get(0).(*domain.UserPreferences); ok {
		return prefs, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mockUserPreferencesRepo) Upsert(ctx context.Context, prefs *domain.UserPreferences) error {
	args := m.Called(ctx, prefs)
	return args.Error(0)
}

func TestPreferencesGetDefaults(t *testing.T) {
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(nil, nil)
	svc := NewPreferencesService(repo)

	prefs, err := svc.Get(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, "list", prefs.DefaultViewLayout)
	repo.AssertExpectations(t)
}

func TestPreferencesGetEmptyViewLayoutFallsBackToList(t *testing.T) {
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(&domain.UserPreferences{}, nil)
	svc := NewPreferencesService(repo)

	prefs, err := svc.Get(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, "list", prefs.DefaultViewLayout)
	repo.AssertExpectations(t)
}

func TestPreferencesUpdateDefaultViewLayout(t *testing.T) {
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(nil, nil)
	repo.On("Upsert", mock.Anything, mock.MatchedBy(func(p *domain.UserPreferences) bool {
		return p.DefaultViewLayout == "board"
	})).Return(nil)
	svc := NewPreferencesService(repo)

	board := "board"
	prefs, err := svc.Update(context.Background(), uuid.New(), dto.UpdatePreferencesRequest{DefaultViewLayout: &board})
	require.NoError(t, err)
	assert.Equal(t, "board", prefs.DefaultViewLayout)
	repo.AssertExpectations(t)
}

func TestPreferencesUpdateRejectsInvalidViewLayout(t *testing.T) {
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(nil, nil)
	svc := NewPreferencesService(repo)

	invalid := "table"
	_, err := svc.Update(context.Background(), uuid.New(), dto.UpdatePreferencesRequest{DefaultViewLayout: &invalid})
	require.ErrorIs(t, err, ErrInvalidPreferences)
	repo.AssertNotCalled(t, "Upsert", mock.Anything, mock.Anything)
}

func TestPreferencesUpdateKeepsViewLayoutWhenOmitted(t *testing.T) {
	existing := &domain.UserPreferences{DefaultViewLayout: "board"}
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(existing, nil)
	repo.On("Upsert", mock.Anything, mock.MatchedBy(func(p *domain.UserPreferences) bool {
		return p.DefaultViewLayout == "board"
	})).Return(nil)
	svc := NewPreferencesService(repo)

	prefs, err := svc.Update(context.Background(), uuid.New(), dto.UpdatePreferencesRequest{})
	require.NoError(t, err)
	assert.Equal(t, "board", prefs.DefaultViewLayout)
	repo.AssertExpectations(t)
}

func TestPreferencesUpdateRepoErrorPropagates(t *testing.T) {
	repo := new(mockUserPreferencesRepo)
	repo.On("Get", mock.Anything, mock.Anything).Return(nil, nil)
	repo.On("Upsert", mock.Anything, mock.Anything).Return(errors.New("db down"))
	svc := NewPreferencesService(repo)

	list := "list"
	_, err := svc.Update(context.Background(), uuid.New(), dto.UpdatePreferencesRequest{DefaultViewLayout: &list})
	require.Error(t, err)
	repo.AssertExpectations(t)
}
