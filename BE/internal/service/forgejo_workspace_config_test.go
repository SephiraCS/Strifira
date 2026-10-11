package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workspaceConfigStore backs the per-workspace configuration tests: a
// connection row, a disable marker and linked repos, plus whatever the
// connection flow saved. Unimplemented methods panic through the embedded
// interface so unexpected persistence calls fail loudly.
type workspaceConfigStore struct {
	forgejoStore
	conn    *domain.ForgejoConnection
	disable *domain.ForgejoWorkspaceDisable
	repos   []domain.ForgejoRepoModel
	saved   []*domain.ForgejoConnection
}

func (s *workspaceConfigStore) GetConnectionByWorkspace(_ context.Context, workspaceID uuid.UUID) (*domain.ForgejoConnection, error) {
	if s.conn != nil && s.conn.WorkspaceID == workspaceID {
		return s.conn, nil
	}
	return nil, nil
}

func (s *workspaceConfigStore) GetWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) (*domain.ForgejoWorkspaceDisable, error) {
	if s.disable != nil && s.disable.WorkspaceID == workspaceID {
		return s.disable, nil
	}
	return nil, nil
}

func (s *workspaceConfigStore) SetWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) error {
	s.disable = &domain.ForgejoWorkspaceDisable{WorkspaceID: workspaceID}
	return nil
}

func (s *workspaceConfigStore) ClearWorkspaceDisable(_ context.Context, workspaceID uuid.UUID) error {
	if s.disable != nil && s.disable.WorkspaceID == workspaceID {
		s.disable = nil
	}
	return nil
}

func (s *workspaceConfigStore) UpsertConnection(_ context.Context, conn *domain.ForgejoConnection) error {
	s.saved = append(s.saved, conn)
	s.conn = conn
	return nil
}

func (s *workspaceConfigStore) ListReposByWorkspace(_ context.Context, workspaceID uuid.UUID) ([]domain.ForgejoRepoModel, error) {
	var repos []domain.ForgejoRepoModel
	for _, repo := range s.repos {
		if repo.WorkspaceID == workspaceID {
			repos = append(repos, repo)
		}
	}
	return repos, nil
}

func (s *workspaceConfigStore) GetRepoByID(_ context.Context, repoID uuid.UUID) (*domain.ForgejoRepoModel, error) {
	for _, repo := range s.repos {
		if repo.ID == repoID {
			return &repo, nil
		}
	}
	return nil, nil
}

func (s *workspaceConfigStore) DeleteRepo(_ context.Context, repoID uuid.UUID) error {
	for i, repo := range s.repos {
		if repo.ID == repoID {
			s.repos = append(s.repos[:i], s.repos[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *workspaceConfigStore) GetRepoByForgejoID(_ context.Context, _ uuid.UUID, forgejoRepoID int64) (*domain.ForgejoRepoModel, error) {
	for _, repo := range s.repos {
		if repo.ForgejoRepoID == forgejoRepoID {
			return &repo, nil
		}
	}
	return nil, nil
}

func (s *workspaceConfigStore) CreateRepo(_ context.Context, repo *domain.ForgejoRepoModel) error {
	s.repos = append(s.repos, *repo)
	return nil
}

func (s *workspaceConfigStore) ListAutoTransitions(context.Context, uuid.UUID) ([]domain.ForgejoAutoTransition, error) {
	return []domain.ForgejoAutoTransition{{Event: "pr_opened", IsActive: true}}, nil
}

// forgejoUserServer fakes the token-validation endpoint.
func forgejoUserServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests != nil {
			requests.Add(1)
		}
		fmt.Fprint(w, `{"id":1,"login":"alice","avatar_url":"https://avatars.example/alice"}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func encryptedWorkspaceToken(t *testing.T, key []byte, token string) string {
	t.Helper()
	encrypted, err := crypto.Encrypt(token, key)
	require.NoError(t, err)
	return encrypted
}

func TestForgejoConnectSucceedsUnderGlobalMode(t *testing.T) {
	server := forgejoUserServer(t, nil)
	key := crypto.DeriveKey("workspace-config-fixture")
	store := &workspaceConfigStore{}
	svc := &ForgejoService{
		fRepo: store, encryptionKey: key,
		global: &GlobalForgejoConfig{URL: "https://global.example", Token: "global-token"},
	}

	account, err := svc.Connect(t.Context(), uuid.New(), server.URL, "workspace-token", nil)

	require.NoError(t, err)
	assert.Equal(t, "alice", account.Login)
	require.Len(t, store.saved, 1)
	assert.Equal(t, server.URL, store.saved[0].InstanceURL)
	assert.Nil(t, store.saved[0].Organization)
	token, err := crypto.Decrypt(store.saved[0].Token, key)
	require.NoError(t, err)
	assert.Equal(t, "workspace-token", token, "the per-workspace row must be stored even in global mode")
}

func TestForgejoConnectRejectsWhileDisabled(t *testing.T) {
	var requests atomic.Int64
	server := forgejoUserServer(t, &requests)
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn:    &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL},
		disable: &domain.ForgejoWorkspaceDisable{WorkspaceID: workspaceID},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: crypto.DeriveKey("workspace-config-fixture")}

	_, err := svc.Connect(t.Context(), workspaceID, server.URL, "workspace-token", nil)

	require.ErrorIs(t, err, ErrForgejoDisabled)
	assert.Zero(t, requests.Load(), "a disabled workspace must not contact Forgejo")
	assert.Empty(t, store.saved, "the stored connection must stay untouched")
}

func TestForgejoConnectValidatesOrganization(t *testing.T) {
	var requests atomic.Int64
	server := forgejoUserServer(t, &requests)
	key := crypto.DeriveKey("workspace-config-fixture")

	for _, tc := range []struct {
		name string
		org  string
	}{
		{name: "spaces", org: "bad org"},
		{name: "slash", org: "org/sub"},
		{name: "leading dash", org: "-org"},
		{name: "too long", org: strings.Repeat("a", 101)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := tc.org
			store := &workspaceConfigStore{}
			svc := &ForgejoService{fRepo: store, encryptionKey: key}

			_, err := svc.Connect(t.Context(), uuid.New(), server.URL, "workspace-token", &org)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "organization")
			assert.Zero(t, requests.Load(), "invalid organizations must fail before token validation")
			assert.Empty(t, store.saved)
		})
	}
}

func TestForgejoConnectNormalizesOrganization(t *testing.T) {
	server := forgejoUserServer(t, nil)
	key := crypto.DeriveKey("workspace-config-fixture")
	store := &workspaceConfigStore{}
	svc := &ForgejoService{fRepo: store, encryptionKey: key}
	trimmed := "  Org.Name-42  "

	_, err := svc.Connect(t.Context(), uuid.New(), server.URL, "workspace-token", &trimmed)
	require.NoError(t, err)
	require.Len(t, store.saved, 1)
	require.NotNil(t, store.saved[0].Organization)
	assert.Equal(t, "Org.Name-42", *store.saved[0].Organization)

	empty := "   "
	store = &workspaceConfigStore{}
	svc = &ForgejoService{fRepo: store, encryptionKey: key}
	_, err = svc.Connect(t.Context(), uuid.New(), server.URL, "workspace-token", &empty)
	require.NoError(t, err)
	require.Len(t, store.saved, 1)
	assert.Nil(t, store.saved[0].Organization, "an empty organization must clear the restriction")
}

func TestForgejoConnectOrganizationChangeBlockedByOutOfOrgRepositories(t *testing.T) {
	server := forgejoUserServer(t, nil)
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn:  &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL},
		repos: []domain.ForgejoRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "other/project"}},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: crypto.DeriveKey("workspace-config-fixture")}
	org := "org"

	_, err := svc.Connect(t.Context(), workspaceID, server.URL, "workspace-token", &org)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unlink repositories outside the organization first")
	assert.Empty(t, store.saved)

	// Clearing the organization is always allowed.
	_, err = svc.Connect(t.Context(), workspaceID, server.URL, "workspace-token", nil)
	require.NoError(t, err)
	require.Len(t, store.saved, 1)
	assert.Nil(t, store.saved[0].Organization)

	// Same-organization repositories do not block setting the restriction.
	store = &workspaceConfigStore{
		conn:  &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL},
		repos: []domain.ForgejoRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, InstanceURL: server.URL, FullName: "ORG/project"}},
	}
	svc = &ForgejoService{fRepo: store, encryptionKey: crypto.DeriveKey("workspace-config-fixture")}
	_, err = svc.Connect(t.Context(), workspaceID, server.URL, "workspace-token", &org)
	require.NoError(t, err)
	require.Len(t, store.saved, 1)
	require.NotNil(t, store.saved[0].Organization)
	assert.Equal(t, "org", *store.saved[0].Organization)
}

func TestForgejoResolveConnectionPrecedence(t *testing.T) {
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()
	row := &domain.ForgejoConnection{
		WorkspaceID: workspaceID, InstanceURL: "https://row.example",
		Organization: new("row-org"), Token: encryptedWorkspaceToken(t, key, "row-token"),
	}
	global := &GlobalForgejoConfig{URL: "https://global.example", Token: "global-token"}

	t.Run("disable marker beats row and global", func(t *testing.T) {
		svc := &ForgejoService{fRepo: &workspaceConfigStore{conn: row, disable: &domain.ForgejoWorkspaceDisable{WorkspaceID: workspaceID}}, encryptionKey: key, global: global}
		conn, err := svc.resolveConnection(t.Context(), workspaceID)
		assert.Nil(t, conn)
		require.ErrorIs(t, err, ErrForgejoDisabled)
	})

	t.Run("row beats global", func(t *testing.T) {
		svc := &ForgejoService{fRepo: &workspaceConfigStore{conn: row}, encryptionKey: key, global: global}
		conn, err := svc.resolveConnection(t.Context(), workspaceID)
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Equal(t, "https://row.example", conn.InstanceURL)
		assert.Equal(t, "row-token", conn.Token)
		assert.False(t, conn.Global)
		require.NotNil(t, conn.Organization)
		assert.Equal(t, "row-org", *conn.Organization)
	})

	t.Run("global fallback", func(t *testing.T) {
		svc := &ForgejoService{fRepo: &workspaceConfigStore{}, encryptionKey: key, global: global}
		conn, err := svc.resolveConnection(t.Context(), workspaceID)
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Equal(t, "https://global.example", conn.InstanceURL)
		assert.Equal(t, "global-token", conn.Token)
		assert.True(t, conn.Global)
		assert.Nil(t, conn.Organization)
	})

	t.Run("not connected", func(t *testing.T) {
		svc := &ForgejoService{fRepo: &workspaceConfigStore{}, encryptionKey: key}
		conn, err := svc.resolveConnection(t.Context(), workspaceID)
		assert.Nil(t, conn)
		require.ErrorIs(t, err, ErrForgejoNotConnected)
	})
}

func TestForgejoSetDisabledUnlinksReposAndKeepsConnection(t *testing.T) {
	workspaceID := uuid.New()
	conn := &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: "https://row.example", Token: "saved-credentials"}
	repos := []domain.ForgejoRepoModel{
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/first"},
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/second"},
	}
	store := &forgejoLifecycleStore{conn: conn, repos: append([]domain.ForgejoRepoModel{}, repos...)}
	svc := &ForgejoService{fRepo: store}

	require.NoError(t, svc.SetDisabled(t.Context(), workspaceID, true))

	assert.Empty(t, store.repos, "disabling must unlink every repository")
	require.NotNil(t, store.disable, "disabling must set the marker")
	assert.Same(t, conn, store.conn, "disabling is a pause: the connection row must survive")

	// Idempotent: a second disable keeps succeeding without repos to unlink.
	require.NoError(t, svc.SetDisabled(t.Context(), workspaceID, true))
	assert.NotNil(t, store.disable)
	assert.Same(t, conn, store.conn)

	require.NoError(t, svc.SetDisabled(t.Context(), workspaceID, false))
	assert.Nil(t, store.disable, "enabling must clear the marker")
	assert.Same(t, conn, store.conn, "enabling must not touch the connection row")

	// Enabling without a marker stays a no-op.
	require.NoError(t, svc.SetDisabled(t.Context(), workspaceID, false))
	assert.Nil(t, store.disable)
}

func TestForgejoSetDisabledStopsOnUnlinkFailure(t *testing.T) {
	workspaceID := uuid.New()
	conn := &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: "https://row.example", Token: "saved-credentials"}
	repos := []domain.ForgejoRepoModel{
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/first"},
		{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/blocked"},
	}
	deleteErr := errors.New("repository still referenced by a checkout")
	store := &forgejoLifecycleStore{conn: conn, repos: append([]domain.ForgejoRepoModel{}, repos...), blockedRepoID: repos[1].ID, deleteRepoErr: deleteErr}
	svc := &ForgejoService{fRepo: store}

	err := svc.SetDisabled(t.Context(), workspaceID, true)

	require.ErrorIs(t, err, deleteErr)
	assert.Nil(t, store.disable, "a failed unlink must not pause the integration")
	assert.Same(t, conn, store.conn)
}

func TestForgejoGetStatusShapes(t *testing.T) {
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()

	t.Run("disabled", func(t *testing.T) {
		var requests atomic.Int64
		server := forgejoUserServer(t, &requests)
		store := &workspaceConfigStore{
			conn:    &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Token: encryptedWorkspaceToken(t, key, "token")},
			disable: &domain.ForgejoWorkspaceDisable{WorkspaceID: workspaceID},
			repos:   []domain.ForgejoRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/project", InstanceURL: server.URL, IsActive: true}},
		}
		svc := &ForgejoService{fRepo: store, encryptionKey: key}

		status, err := svc.GetStatus(t.Context(), workspaceID)

		require.NoError(t, err, "a disabled workspace reports its state without error")
		assert.True(t, status.Disabled)
		assert.False(t, status.Configured)
		assert.False(t, status.Connected)
		assert.Empty(t, status.ManagedBy)
		assert.Empty(t, status.InstanceURL)
		assert.Nil(t, status.Organization)
		assert.Nil(t, status.Account)
		assert.Empty(t, status.Repos)
		assert.Zero(t, requests.Load(), "a disabled status must not contact Forgejo")
	})

	t.Run("workspace-managed row with organization", func(t *testing.T) {
		server := forgejoUserServer(t, nil)
		store := &workspaceConfigStore{
			conn:  &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Organization: new("org"), Token: encryptedWorkspaceToken(t, key, "token")},
			repos: []domain.ForgejoRepoModel{{ID: uuid.New(), WorkspaceID: workspaceID, FullName: "org/project", InstanceURL: server.URL, IsActive: true}},
		}
		svc := &ForgejoService{fRepo: store, encryptionKey: key}

		status, err := svc.GetStatus(t.Context(), workspaceID)

		require.NoError(t, err)
		assert.False(t, status.Disabled)
		assert.True(t, status.Configured)
		assert.True(t, status.Connected)
		assert.False(t, status.GlobalConfig)
		assert.Equal(t, "workspace", status.ManagedBy)
		assert.Equal(t, server.URL, status.InstanceURL)
		require.NotNil(t, status.Organization)
		assert.Equal(t, "org", *status.Organization)
		require.NotNil(t, status.Account)
		assert.Equal(t, "alice", status.Account.Login)
		assert.Len(t, status.Repos, 1)
	})

	t.Run("server-managed global fallback", func(t *testing.T) {
		server := forgejoUserServer(t, nil)
		store := &workspaceConfigStore{}
		svc := &ForgejoService{fRepo: store, encryptionKey: key, global: &GlobalForgejoConfig{URL: server.URL, Token: "global-token"}}

		status, err := svc.GetStatus(t.Context(), workspaceID)

		require.NoError(t, err)
		assert.False(t, status.Disabled)
		assert.True(t, status.Configured)
		assert.True(t, status.Connected)
		assert.True(t, status.GlobalConfig)
		assert.Equal(t, "server", status.ManagedBy)
		assert.Nil(t, status.Organization)
	})

	t.Run("unconfigured", func(t *testing.T) {
		svc := &ForgejoService{fRepo: &workspaceConfigStore{}, encryptionKey: key}
		status, err := svc.GetStatus(t.Context(), workspaceID)
		require.NoError(t, err)
		assert.False(t, status.Disabled)
		assert.False(t, status.Configured)
		assert.Empty(t, status.ManagedBy)
		assert.Empty(t, status.Repos)
	})
}

// forgejoRepoListServer fakes paginated repo discovery.
func forgejoRepoListServer(t *testing.T, repos string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user/repos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, repos)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestForgejoListAvailableReposFiltersByOrganization(t *testing.T) {
	server := forgejoRepoListServer(t, `[{"id":1,"full_name":"org/inside","default_branch":"main"},{"id":2,"full_name":"ORG/case-variant","default_branch":"main"},{"id":3,"full_name":"elsewhere/outside","default_branch":"main"}]`)
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn: &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Organization: new("org"), Token: encryptedWorkspaceToken(t, key, "token")},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: key}

	repos, err := svc.ListAvailableRepos(t.Context(), workspaceID)

	require.NoError(t, err)
	require.Len(t, repos, 2)
	assert.Equal(t, "org/inside", repos[0].FullName)
	assert.Equal(t, "ORG/case-variant", repos[1].FullName, "the organization match is case-insensitive")

	// Without a restriction every repository stays visible.
	store.conn = &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Token: encryptedWorkspaceToken(t, key, "token")}
	repos, err = svc.ListAvailableRepos(t.Context(), workspaceID)
	require.NoError(t, err)
	assert.Len(t, repos, 3)
}

func TestForgejoListAvailableReposRejectsDisabledWorkspace(t *testing.T) {
	server := forgejoRepoListServer(t, `[]`)
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn:    &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Token: encryptedWorkspaceToken(t, key, "token")},
		disable: &domain.ForgejoWorkspaceDisable{WorkspaceID: workspaceID},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: key}

	repos, err := svc.ListAvailableRepos(t.Context(), workspaceID)

	assert.Nil(t, repos)
	require.ErrorIs(t, err, ErrForgejoDisabled)
}

func TestForgejoLinkReposRejectsOutsideOrganization(t *testing.T) {
	var webhooks atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/user/repos":
			fmt.Fprint(w, `[{"id":1,"full_name":"org/inside","default_branch":"main"},{"id":2,"full_name":"elsewhere/outside","default_branch":"main"}]`)
		case "POST /api/v1/repos/elsewhere/outside/hooks":
			webhooks.Add(1)
			fmt.Fprint(w, `{"id":9}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn: &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Organization: new("org"), Token: encryptedWorkspaceToken(t, key, "token")},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: key}

	err := svc.LinkRepos(t.Context(), workspaceID, dto.LinkForgejoReposRequest{ForgejoRepoIDs: []int64{2}})

	require.Error(t, err)
	assert.Equal(t, "repository elsewhere/outside is outside the configured organization", err.Error())
	assert.Zero(t, webhooks.Load(), "no webhook may be created for an out-of-organization repository")
	assert.Empty(t, store.repos)
}

func TestForgejoLinkReposWithinOrganizationSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/user/repos":
			fmt.Fprint(w, `[{"id":1,"full_name":"ORG/inside","default_branch":"main"}]`)
		case "POST /api/v1/repos/ORG/inside/hooks":
			fmt.Fprint(w, `{"id":7}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	key := crypto.DeriveKey("workspace-config-fixture")
	workspaceID := uuid.New()
	store := &workspaceConfigStore{
		conn: &domain.ForgejoConnection{WorkspaceID: workspaceID, InstanceURL: server.URL, Organization: new("org"), Token: encryptedWorkspaceToken(t, key, "token")},
	}
	svc := &ForgejoService{fRepo: store, encryptionKey: key, webhookURL: "https://hooks.example/f"}

	require.NoError(t, svc.LinkRepos(t.Context(), workspaceID, dto.LinkForgejoReposRequest{ForgejoRepoIDs: []int64{1}}))
	require.Len(t, store.repos, 1)
	assert.Equal(t, "ORG/inside", store.repos[0].FullName)
}
