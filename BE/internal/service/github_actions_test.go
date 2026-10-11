package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/pkg/crypto"
	gh "github.com/ku/ku-backend/pkg/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Embedding the store interface makes unexpected calls fail immediately;
// overrides below are the only persistence calls these flows need.
type actionsTestStore struct {
	githubStore
	repo      *domain.GitHubRepoModel
	conn      *domain.GitHubConnection
	repos     []domain.GitHubRepoModel
	saved     *domain.GitHubConnection
	createErr error
	onCreate  func()
}

func (s *actionsTestStore) GetRepoByID(context.Context, uuid.UUID) (*domain.GitHubRepoModel, error) {
	return s.repo, nil
}

func (s *actionsTestStore) GetConnectionByWorkspace(context.Context, uuid.UUID) (*domain.GitHubConnection, error) {
	return s.conn, nil
}

func (s *actionsTestStore) GetWorkspaceDisable(context.Context, uuid.UUID) (*domain.GitHubWorkspaceDisable, error) {
	return nil, nil
}

func (s *actionsTestStore) ListReposByWorkspace(context.Context, uuid.UUID) ([]domain.GitHubRepoModel, error) {
	return s.repos, nil
}

func (s *actionsTestStore) ListAutoTransitions(context.Context, uuid.UUID) ([]domain.GitHubAutoTransition, error) {
	return []domain.GitHubAutoTransition{{Event: "pr_opened", IsActive: true}}, nil
}

func (s *actionsTestStore) UpsertConnection(_ context.Context, conn *domain.GitHubConnection) error {
	s.saved = conn
	return nil
}

func (s *actionsTestStore) GetRepoByGitHubID(context.Context, uuid.UUID, int64) (*domain.GitHubRepoModel, error) {
	return nil, nil
}

func (s *actionsTestStore) CreateRepo(context.Context, *domain.GitHubRepoModel) error {
	if s.onCreate != nil {
		s.onCreate()
	}
	return s.createErr
}

func TestGitHubActionsRejectUnlinkedOrForeignRepositories(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(w, `{"workflow_runs":[],"total_count":0}`)
	}))
	defer server.Close()
	for _, tc := range []struct {
		name string
		repo *domain.GitHubRepoModel
		err  error
	}{
		{name: "missing", err: ErrGitHubActionRepoNotFound},
		{name: "foreign workspace", repo: &domain.GitHubRepoModel{WorkspaceID: uuid.New(), IsActive: true}, err: ErrGitHubActionRepoNotFound},
		{name: "inactive", repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID}, err: ErrGitHubActionRepoNotFound},
		{name: "stale instance", repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, IsActive: true, InstanceURL: "https://other.example", FullName: "org/project"}, err: ErrGitHubActionConnection},
		{name: "repository path traversal", repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, IsActive: true, InstanceURL: server.URL, FullName: "org/.."}, err: ErrGitHubActionRepoNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &GitHubService{fRepo: &actionsTestStore{repo: tc.repo}, global: &GlobalGitHubConfig{URL: server.URL, Token: "workspace-token"}}
			_, err := svc.ListActionRuns(t.Context(), workspaceID, repoID, gh.ActionRunOptions{})
			assert.ErrorIs(t, err, tc.err)
			_, err = svc.GetActionJobLog(t.Context(), workspaceID, repoID, 4, 12, 0)
			assert.ErrorIs(t, err, tc.err)
			_, err = svc.GetActionJob(t.Context(), workspaceID, repoID, 4, 12)
			assert.ErrorIs(t, err, tc.err)
			assert.ErrorIs(t, svc.CancelActionRun(t.Context(), workspaceID, repoID, 12), tc.err)
		})
	}
	assert.Zero(t, calls, "credentials must not be sent before workspace and instance checks pass")
}

func TestGitHubActionsMapsModelsAndPaginatesJobs(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer workspace-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/v3/repos/org/project/actions/runs":
			assert.Equal(t, "queued", r.URL.Query().Get("status"))
			assert.Equal(t, "feature/work", r.URL.Query().Get("branch"))
			assert.Equal(t, "20", r.URL.Query().Get("per_page"))
			fmt.Fprint(w, `{"workflow_runs":[{"id":4,"display_title":"Build project","run_number":8,"path":"build.yaml","name":"Build","head_branch":"feature/work","head_sha":"0123456789","event":"push","status":"completed","conclusion":"success","created_at":"2026-09-30T12:00:00Z","updated_at":"2026-09-30T12:00:01Z","run_started_at":"0001-01-01T00:00:00Z","html_url":"javascript:alert(1)"}],"total_count":22}`)
		case "/api/v3/repos/org/project/actions/runs/4/jobs":
			fmt.Fprint(w, `{"total_count":3,"jobs":[{"id":1,"run_id":4,"name":"Test"},{"id":2,"run_id":4,"name":"Build","run_attempt":3,"status":"completed","conclusion":"failure"},{"id":3,"run_id":4,"name":"Deploy"}]}`)
		case "/api/v3/repos/org/project/actions/jobs/2/logs":
			fmt.Fprint(w, "attempt two failed\n")
		default:
			t.Errorf("unexpected request %s", r.URL)
		}
	}))
	defer server.Close()
	svc := &GitHubService{
		fRepo:  &actionsTestStore{repo: &domain.GitHubRepoModel{ID: repoID, WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", IsActive: true}},
		global: &GlobalGitHubConfig{URL: server.URL, Token: "workspace-token"},
	}
	runs, err := svc.ListActionRuns(t.Context(), workspaceID, repoID, gh.ActionRunOptions{Status: "queued", Ref: "refs/heads/feature/work"})
	require.NoError(t, err)
	require.Len(t, runs.Data, 1)
	run := runs.Data[0]
	assert.EqualValues(t, 8, run.RunNumber)
	assert.Equal(t, "Build project", run.Title)
	assert.Equal(t, "build.yaml", run.WorkflowID)
	assert.Equal(t, "feature/work", run.HeadBranch)
	assert.Equal(t, "0123456789", run.HeadSHA)
	assert.Equal(t, "success", run.Conclusion)
	assert.Empty(t, run.HTMLURL)
	assert.Nil(t, run.StartedAt)
	assert.NotNil(t, run.CompletedAt)
	assert.True(t, runs.HasMore)
	jobs, err := svc.ListActionJobs(t.Context(), workspaceID, repoID, 4, 2, 1)
	require.NoError(t, err)
	require.Len(t, jobs.Data, 1)
	assert.EqualValues(t, 2, jobs.Data[0].ID)
	assert.EqualValues(t, 3, jobs.Data[0].Attempt)
	assert.Equal(t, 3, jobs.TotalCount)
	assert.True(t, jobs.HasMore)
	job, err := svc.GetActionJob(t.Context(), workspaceID, repoID, 4, 2)
	require.NoError(t, err)
	assert.Equal(t, jobs.Data[0], *job)
	assert.Equal(t, "failure", job.Conclusion)
	log, err := svc.GetActionJobLog(t.Context(), workspaceID, repoID, 4, 2, 3)
	require.NoError(t, err)
	assert.Equal(t, "attempt two failed\n", log.Content)
	for _, tc := range []struct {
		page, size, count int
		hasMore           bool
	}{
		{1, 1, 1, true}, {3, 1, 1, false}, {4, 1, 0, false}, {10000, 100, 0, false},
	} {
		page, err := svc.ListActionJobs(t.Context(), workspaceID, repoID, 4, tc.page, tc.size)
		require.NoError(t, err)
		assert.Len(t, page.Data, tc.count)
		assert.Equal(t, 3, page.TotalCount)
		assert.Equal(t, tc.page, page.Page)
		assert.Equal(t, tc.size, page.PerPage)
		assert.Equal(t, tc.hasMore, page.HasMore)
	}
}

func TestGitHubActionsCancellationRequiresActiveRun(t *testing.T) {
	for _, status := range []string{"completed", "queued", "in_progress"} {
		t.Run(status, func(t *testing.T) {
			workspaceID, repoID := uuid.New(), uuid.New()
			var mutations int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					assert.Equal(t, "/api/v3/repos/org/project/actions/runs/9", r.URL.Path)
					fmt.Fprintf(w, `{"id":9,"status":%q}`, status)
					return
				}
				mutations++
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/api/v3/repos/org/project/actions/runs/9/cancel", r.URL.Path)
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			svc := &GitHubService{fRepo: &actionsTestStore{repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", IsActive: true}}, global: &GlobalGitHubConfig{URL: server.URL, Token: "token"}}
			err := svc.CancelActionRun(t.Context(), workspaceID, repoID, 9)
			if status == "queued" || status == "in_progress" {
				require.NoError(t, err)
				assert.Equal(t, 1, mutations)
			} else {
				assert.ErrorIs(t, err, ErrGitHubActionState)
				assert.Zero(t, mutations)
			}
		})
	}
}

func TestGitHubActionsRejectsInvalidParametersBeforeLookingUpRepository(t *testing.T) {
	svc := &GitHubService{}
	for _, opts := range []gh.ActionRunOptions{{Page: -1}, {Page: 10001}, {PerPage: 101}, {Status: "invalid"}, {Ref: "main\ninvalid"}} {
		_, err := svc.ListActionRuns(t.Context(), uuid.New(), uuid.New(), opts)
		assert.ErrorIs(t, err, ErrGitHubActionParams)
	}
	_, err := svc.GetActionJobLog(t.Context(), uuid.New(), uuid.New(), 1, 1, -1)
	assert.ErrorIs(t, err, ErrGitHubActionParams)
	for _, ids := range [][2]int64{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		_, err := svc.GetActionJob(t.Context(), uuid.New(), uuid.New(), ids[0], ids[1])
		assert.ErrorIs(t, err, ErrGitHubActionParams)
		_, err = svc.GetActionJobLog(t.Context(), uuid.New(), uuid.New(), ids[0], ids[1], 0)
		assert.ErrorIs(t, err, ErrGitHubActionParams)
	}
	assert.ErrorIs(t, svc.CancelActionRun(t.Context(), uuid.New(), uuid.New(), 0), ErrGitHubActionParams)
}

func TestGitHubActionsUsesEncryptedWorkspaceConnection(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer decrypted-workspace-token", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"id":42,"status":"completed","conclusion":"success"}`)
	}))
	defer server.Close()
	key := crypto.DeriveKey("workspace-key")
	token, err := crypto.Encrypt("decrypted-workspace-token", key)
	require.NoError(t, err)
	svc := &GitHubService{
		fRepo: &actionsTestStore{
			repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", IsActive: true},
			conn: &domain.GitHubConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Token: token},
		},
		encryptionKey: key,
	}
	run, err := svc.GetActionRun(t.Context(), workspaceID, repoID, 42)
	require.NoError(t, err)
	assert.Equal(t, "completed", run.Status)
	assert.Equal(t, "success", run.Conclusion)
}

func TestGitHubJobLogsAuthorizeThroughRunJobs(t *testing.T) {
	workspaceID, repoID := uuid.New(), uuid.New()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v3/repos/org/project/actions/runs/9/jobs":
			fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":42,"run_id":9,"run_attempt":3,"status":"completed","conclusion":"success"}]}`)
		case "/api/v3/repos/org/project/actions/jobs/42/logs":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "GitHub Actions log\n")
		default:
			t.Errorf("unsupported request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	svc := &GitHubService{
		fRepo:  &actionsTestStore{repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", IsActive: true}},
		global: &GlobalGitHubConfig{URL: server.URL, Token: "workspace-token"},
	}
	current, err := svc.GetActionJobLog(t.Context(), workspaceID, repoID, 9, 42, 0)
	require.NoError(t, err)
	explicit, err := svc.GetActionJobLog(t.Context(), workspaceID, repoID, 9, 42, 3)
	require.NoError(t, err)
	stale, err := svc.GetActionJobLog(t.Context(), workspaceID, repoID, 9, 42, 2)
	assert.ErrorIs(t, err, ErrGitHubActionJobNotFound)
	assert.Nil(t, stale)
	assert.Equal(t, "GitHub Actions log\n", current.Content)
	assert.Equal(t, current, explicit)
	assert.Equal(t, []string{
		"/api/v3/repos/org/project/actions/runs/9/jobs?per_page=100&page=1", "/api/v3/repos/org/project/actions/jobs/42/logs",
		"/api/v3/repos/org/project/actions/runs/9/jobs?per_page=100&page=1", "/api/v3/repos/org/project/actions/jobs/42/logs",
		"/api/v3/repos/org/project/actions/runs/9/jobs?per_page=100&page=1",
	}, requests)
}

func TestGitHubActionsRejectJobOutsideRunBeforeReadingLogs(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing job", `{"total_count":0,"jobs":[]}`, http.StatusOK},
		{"different job", `{"total_count":1,"jobs":[{"id":43,"run_id":9}]}`, http.StatusOK},
		{"wrong run", `{"total_count":1,"jobs":[{"id":42,"run_id":10}]}`, http.StatusOK},
		{"missing run", ``, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceID, repoID := uuid.New(), uuid.New()
			var requests []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests = append(requests, r.URL.Path)
				assert.Equal(t, "/api/v3/repos/org/project/actions/runs/9/jobs", r.URL.Path)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			svc := &GitHubService{
				fRepo:  &actionsTestStore{repo: &domain.GitHubRepoModel{WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "org/project", IsActive: true}},
				global: &GlobalGitHubConfig{URL: server.URL, Token: "workspace-token"},
			}
			_, err := svc.GetActionJob(t.Context(), workspaceID, repoID, 9, 42)
			assert.ErrorIs(t, err, ErrGitHubActionJobNotFound)
			_, err = svc.GetActionJobLog(t.Context(), workspaceID, repoID, 9, 42, 0)
			assert.ErrorIs(t, err, ErrGitHubActionJobNotFound)
			assert.Equal(t, []string{"/api/v3/repos/org/project/actions/runs/9/jobs", "/api/v3/repos/org/project/actions/runs/9/jobs"}, requests)
		})
	}
}

func TestGitHubActionLinksStayOnConfiguredOrigin(t *testing.T) {
	for _, value := range []string{"javascript:alert(1)", "https://other.example/org/repo", "https://user@git.example/org/repo", "http://git.example/org/repo", "https://git.example:8443/org/repo"} {
		assert.Empty(t, actionURL(value, "https://git.example"))
	}
	assert.Equal(t, "https://GIT.example:443/org/repo/actions/runs/1", actionURL("https://GIT.example:443/org/repo/actions/runs/1", "https://git.example"))
}

func TestGitHubStatusReflectsRevokedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	svc := &GitHubService{fRepo: &actionsTestStore{}, global: &GlobalGitHubConfig{URL: server.URL, Token: "revoked-token"}}
	status, err := svc.GetStatus(t.Context(), uuid.New())
	require.NoError(t, err)
	assert.True(t, status.Configured)
	assert.False(t, status.Connected)
	assert.Nil(t, status.Account)
}

func TestGitHubConnectPreventsInstanceReplacementWithLinkedRepositories(t *testing.T) {
	store := &actionsTestStore{repos: []domain.GitHubRepoModel{{InstanceURL: "https://original.example"}}}
	svc := &GitHubService{fRepo: store}
	_, err := svc.Connect(t.Context(), uuid.New(), "https://replacement.example", "new-token", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unlink existing repositories")
	assert.Nil(t, store.saved)
}

func TestGitHubConnectAllowsTokenRotationOnSameInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer new-token", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"id":1,"login":"alice"}`)
	}))
	defer server.Close()
	key := crypto.DeriveKey("test")
	store := &actionsTestStore{repos: []domain.GitHubRepoModel{{InstanceURL: server.URL}}}
	svc := &GitHubService{fRepo: store, encryptionKey: key}
	account, err := svc.Connect(t.Context(), uuid.New(), server.URL+"/", "new-token", nil)
	require.NoError(t, err)
	assert.Equal(t, "alice", account.Login)
	require.NotNil(t, store.saved)
	assert.Equal(t, server.URL, store.saved.InstanceURL)
	token, err := crypto.Decrypt(store.saved.Token, key)
	require.NoError(t, err)
	assert.Equal(t, "new-token", token)
}

func TestGitHubLinkPersistenceFailureRemovesNewWebhookEvenAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v3/user/repos":
			fmt.Fprint(w, `[{"id":4,"full_name":"org/project"}]`)
		case "POST /api/v3/repos/org/project/hooks":
			fmt.Fprint(w, `{"id":88}`)
		case "DELETE /api/v3/repos/org/project/hooks/88":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	svc := &GitHubService{
		fRepo:  &actionsTestStore{createErr: errors.New("unique constraint with sensitive database details"), onCreate: cancel},
		global: &GlobalGitHubConfig{URL: server.URL, Token: "token"}, encryptionKey: crypto.DeriveKey("test"),
	}
	err := svc.LinkRepos(ctx, uuid.New(), dto.LinkGitHubReposRequest{GitHubRepoIDs: []int64{4}})
	require.Error(t, err)
	assert.Equal(t, "repository link could not be saved", err.Error())
	assert.True(t, deleted)
}
