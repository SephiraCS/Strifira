package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Any credential lookup, automation initialization, or other store access
// panics through the embedded interface; only the list read is implemented.
type linkedReposTestStore struct {
	githubStore
	repos       []domain.GitHubRepoModel
	err         error
	ctx         context.Context
	workspaceID uuid.UUID
	calls       int
}

func (s *linkedReposTestStore) ListReposByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.GitHubRepoModel, error) {
	s.ctx, s.workspaceID = ctx, workspaceID
	s.calls++
	return s.repos, s.err
}

func TestListLinkedReposReadsOnlyActiveWorkspaceLinks(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	var upstreamCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, global := range []*GlobalGitHubConfig{nil, {URL: server.URL, Token: "fixture-token"}} {
		store := &linkedReposTestStore{repos: []domain.GitHubRepoModel{
			{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/inactive"},
			{ID: repoID, WorkspaceID: workspaceID, GitHubRepoID: 52, FullName: "org/linked", InstanceURL: server.URL, DefaultBranch: "main", IsActive: true, WebhookID: new(int64(7)), WebhookSecret: "private-webhook-secret"},
			{ID: uuid.New(), WorkspaceID: uuid.New(), FullName: "org/foreign", IsActive: true},
		}}
		svc := &GitHubService{fRepo: store, global: global}
		ctx := t.Context()
		repos, err := svc.ListLinkedRepos(ctx, workspaceID)
		require.NoError(t, err)
		require.Equal(t, []dto.GitHubRepoResponse{{ID: repoID.String(), GitHubRepoID: 52, FullName: "org/linked", InstanceURL: server.URL, DefaultBranch: "main", IsActive: true}}, repos)
		assert.Equal(t, workspaceID, store.workspaceID)
		assert.Same(t, ctx, store.ctx)
		assert.Equal(t, 1, store.calls)
		encoded, err := json.Marshal(repos)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), "private-webhook-secret")
		assert.NotContains(t, string(encoded), "webhook_id")
		assert.NotContains(t, string(encoded), "fixture-token")
	}
	assert.Zero(t, upstreamCalls.Load(), "listing stored links must never contact GitHub")
}

func TestListLinkedReposReturnsEmptyArray(t *testing.T) {
	workspaceID := uuid.New()
	for _, rows := range [][]domain.GitHubRepoModel{
		nil,
		{},
		{{WorkspaceID: workspaceID, IsActive: false}, {WorkspaceID: uuid.New(), IsActive: true}},
	} {
		repos, err := (&GitHubService{fRepo: &linkedReposTestStore{repos: rows}}).ListLinkedRepos(t.Context(), workspaceID)
		require.NoError(t, err)
		require.NotNil(t, repos)
		require.Empty(t, repos)
		encoded, err := json.Marshal(repos)
		require.NoError(t, err)
		assert.Equal(t, "[]", string(encoded))
	}
}

func TestListLinkedReposPropagatesStoreErrorsWithoutPartialData(t *testing.T) {
	workspaceID := uuid.New()
	for _, storeErr := range []error{errors.New("store read failed"), context.Canceled} {
		store := &linkedReposTestStore{err: storeErr, repos: []domain.GitHubRepoModel{{WorkspaceID: workspaceID, IsActive: true}}}
		repos, err := (&GitHubService{fRepo: store}).ListLinkedRepos(t.Context(), workspaceID)
		assert.ErrorIs(t, err, storeErr)
		assert.Nil(t, repos)
		assert.Equal(t, 1, store.calls)
	}
}
