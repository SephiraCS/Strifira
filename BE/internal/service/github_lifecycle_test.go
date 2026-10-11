package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unimplemented methods panic, so a disconnect cannot silently make an
// unexpected persistence call.
type githubLifecycleStore struct {
	githubStore
	conn          *domain.GitHubConnection
	disable       *domain.GitHubWorkspaceDisable
	repos         []domain.GitHubRepoModel
	listErr       error
	lookupErr     error
	deleteRepoErr error
	blockedRepoID uuid.UUID
	calls         []string
}

func (s *githubLifecycleStore) ListReposByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]domain.GitHubRepoModel, error) {
	s.calls = append(s.calls, "list repos")
	var repos []domain.GitHubRepoModel
	for _, repo := range s.repos {
		if repo.WorkspaceID == workspaceID {
			repos = append(repos, repo)
		}
	}
	return repos, s.listErr
}

func (s *githubLifecycleStore) GetRepoByID(_ context.Context, repoID uuid.UUID) (*domain.GitHubRepoModel, error) {
	s.calls = append(s.calls, "get repo")
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	for _, repo := range s.repos {
		if repo.ID == repoID {
			return &repo, nil
		}
	}
	return nil, nil
}

func (s *githubLifecycleStore) GetConnectionByWorkspace(_ context.Context, workspaceID uuid.UUID) (*domain.GitHubConnection, error) {
	s.calls = append(s.calls, "get connection")
	if s.conn != nil && s.conn.WorkspaceID == workspaceID {
		return s.conn, nil
	}
	return nil, nil
}

func (s *githubLifecycleStore) GetWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) (*domain.GitHubWorkspaceDisable, error) {
	if s.disable != nil && s.disable.WorkspaceID == workspaceID {
		return s.disable, nil
	}
	return nil, nil
}

func (s *githubLifecycleStore) SetWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) error {
	s.calls = append(s.calls, "set disable")
	s.disable = &domain.GitHubWorkspaceDisable{WorkspaceID: workspaceID}
	return nil
}

func (s *githubLifecycleStore) ClearWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) error {
	s.calls = append(s.calls, "clear disable")
	if s.disable != nil && s.disable.WorkspaceID == workspaceID {
		s.disable = nil
	}
	return nil
}

func (s *githubLifecycleStore) DeleteRepo(_ context.Context, repoID uuid.UUID) error {
	s.calls = append(s.calls, "delete repo")
	if repoID == s.blockedRepoID {
		return s.deleteRepoErr
	}
	s.repos = slices.DeleteFunc(s.repos, func(repo domain.GitHubRepoModel) bool { return repo.ID == repoID })
	return nil
}

func (s *githubLifecycleStore) DeleteConnection(_ context.Context, workspaceID uuid.UUID) error {
	s.calls = append(s.calls, "delete connection")
	if s.conn != nil && s.conn.WorkspaceID == workspaceID {
		s.conn = nil
	}
	return nil
}

func TestGitHubDisconnectRetainsConnectionWhenRepoListFails(t *testing.T) {
	for _, listErr := range []error{errors.New("repository list unavailable"), context.Canceled} {
		workspaceID := uuid.New()
		conn := &domain.GitHubConnection{WorkspaceID: workspaceID, Token: "saved-credentials"}
		repos := []domain.GitHubRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/project"}}
		store := &githubLifecycleStore{conn: conn, repos: slices.Clone(repos), listErr: listErr}
		svc := &GitHubService{fRepo: store}

		err := svc.Disconnect(t.Context(), workspaceID)

		require.ErrorIs(t, err, listErr)
		assert.Same(t, conn, store.conn)
		assert.Equal(t, repos, store.repos)
		assert.Equal(t, []string{"list repos"}, store.calls, "partial list results must never be unlinked")
	}
}

func TestGitHubDisconnectRetainsConnectionWhenRepoLookupFails(t *testing.T) {
	workspaceID := uuid.New()
	conn := &domain.GitHubConnection{WorkspaceID: workspaceID, Token: "saved-credentials"}
	lookupErr := errors.New("repository lookup unavailable")
	repos := []domain.GitHubRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/project"}}
	store := &githubLifecycleStore{conn: conn, repos: slices.Clone(repos), lookupErr: lookupErr}
	svc := &GitHubService{fRepo: store}

	err := svc.Disconnect(t.Context(), workspaceID)

	require.ErrorIs(t, err, lookupErr)
	assert.Same(t, conn, store.conn)
	assert.Equal(t, repos, store.repos)
	assert.Equal(t, []string{"list repos", "get repo"}, store.calls)
}

func TestGitHubDisconnectStopsOnLocalUnlinkFailureAndCanRetry(t *testing.T) {
	workspaceID := uuid.New()
	conn := &domain.GitHubConnection{WorkspaceID: workspaceID, Token: "saved-credentials"}
	repos := []domain.GitHubRepoModel{
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/first"},
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/blocked"},
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/last"},
	}
	deleteErr := errors.New("repository still referenced by a checkout")
	store := &githubLifecycleStore{
		conn: conn, repos: slices.Clone(repos), blockedRepoID: repos[1].ID, deleteRepoErr: deleteErr,
	}
	svc := &GitHubService{fRepo: store}

	err := svc.Disconnect(t.Context(), workspaceID)

	require.ErrorIs(t, err, deleteErr)
	assert.Contains(t, err.Error(), "org/blocked")
	assert.Same(t, conn, store.conn, "failed local cleanup must preserve credentials for retry")
	assert.Equal(t, repos[1:], store.repos)
	assert.Equal(t, []string{"list repos", "get repo", "delete repo", "get repo", "delete repo"}, store.calls)

	store.blockedRepoID = uuid.Nil
	store.calls = nil
	require.NoError(t, svc.Disconnect(t.Context(), workspaceID))
	assert.Empty(t, store.repos)
	assert.Nil(t, store.conn)
	assert.Equal(t, []string{"list repos", "get repo", "delete repo", "get repo", "delete repo", "delete connection"}, store.calls)
}

func TestGitHubDisconnectCleansLocalLinksWithBestEffortWebhookDeletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "webhook removed", status: http.StatusNoContent},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls atomic.Int64
			mux := http.NewServeMux()
			mux.HandleFunc("DELETE /api/v3/repos/org/project/hooks/7", func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls.Add(1)
				assert.Equal(t, "Bearer workspace-token", r.Header.Get("Authorization"))
				w.WriteHeader(tc.status)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			key := crypto.DeriveKey("github-disconnect-fixture")
			encrypted, err := crypto.Encrypt("workspace-token", key)
			require.NoError(t, err)
			workspaceID := uuid.New()
			foreignWorkspaceID := uuid.New()
			foreignRepo := domain.GitHubRepoModel{ID: uuid.New(), WorkspaceID: foreignWorkspaceID, FullName: "org/foreign"}
			store := &githubLifecycleStore{
				conn: &domain.GitHubConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Token: encrypted},
				repos: []domain.GitHubRepoModel{
					{ID: uuid.New(), WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", WebhookID: new(int64(7))},
					{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/no-hook"},
					foreignRepo,
				},
			}
			svc := &GitHubService{fRepo: store, encryptionKey: key}

			require.NoError(t, svc.Disconnect(t.Context(), workspaceID))

			assert.Equal(t, int64(1), upstreamCalls.Load())
			assert.Equal(t, []domain.GitHubRepoModel{foreignRepo}, store.repos)
			assert.Nil(t, store.conn)
			assert.Equal(t, []string{"list repos", "get repo", "get connection", "delete repo", "get repo", "delete repo", "delete connection"}, store.calls)
		})
	}
}
