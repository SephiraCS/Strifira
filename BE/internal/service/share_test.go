package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// --- Mocks ---

type mockScreenShareRepo struct {
	mock.Mock
}

func (m *mockScreenShareRepo) Create(ctx context.Context, share *domain.ScreenShare) error {
	args := m.Called(ctx, share)
	return args.Error(0)
}

func (m *mockScreenShareRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.ScreenShare, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ScreenShare), args.Error(1)
}

func (m *mockScreenShareRepo) ListActiveByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.ScreenShare, error) {
	args := m.Called(ctx, workspaceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.ScreenShare), args.Error(1)
}

func (m *mockScreenShareRepo) ListRecentEndedByWorkspace(ctx context.Context, workspaceID uuid.UUID, limit int) ([]domain.ScreenShare, error) {
	args := m.Called(ctx, workspaceID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.ScreenShare), args.Error(1)
}

func (m *mockScreenShareRepo) GetActiveByPresenter(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.ScreenShare, error) {
	args := m.Called(ctx, workspaceID, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ScreenShare), args.Error(1)
}

func (m *mockScreenShareRepo) MarkEnded(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *mockScreenShareRepo) TouchPresenterSeen(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

type mockUserRepoForShares struct {
	mock.Mock
}

func (m *mockUserRepoForShares) Create(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *mockUserRepoForShares) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *mockUserRepoForShares) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *mockUserRepoForShares) Update(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

// --- Tests ---

func newShareTestService(t *testing.T) (*mockScreenShareRepo, *mockUserRepoForShares, *realtime.Hub, *ScreenShareService) {
	shareRepo := new(mockScreenShareRepo)
	userRepo := new(mockUserRepoForShares)
	// A hub that has been up for an hour: the restart grace window is over,
	// so GC decisions reflect the presenter-contact clock alone.
	hub := realtime.NewHubStartedAt(time.Now().Add(-time.Hour))
	svc := NewScreenShareService(shareRepo, userRepo, hub)
	return shareRepo, userRepo, hub, svc
}

func TestScreenShareService_Create(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	userID := uuid.New()

	shareRepo.On("GetActiveByPresenter", ctx, wsID, userID).Return(nil, nil)
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Return(nil).Run(func(args mock.Arguments) {
		share := args.Get(1).(*domain.ScreenShare)
		share.ID = uuid.New()
		share.CreatedAt = share.CreatedAt.UTC()
	})

	share, err := svc.Create(ctx, wsID, userID, dto.CreateScreenShareRequest{})

	assert.NoError(t, err)
	assert.Equal(t, domain.ScreenShareStatusActive, share.Status)
	assert.Equal(t, wsID, share.WorkspaceID)
	assert.Equal(t, userID, share.StartedBy)
	assert.NotEqual(t, uuid.Nil, share.ID)
	assert.Equal(t, hub, svc.hub)
}

func TestScreenShareService_CreateRejectsExistingPresenterTab(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	userID := uuid.New()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: userID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Minute)}

	shareRepo.On("GetActiveByPresenter", ctx, wsID, userID).Return(existing, nil)
	shareRepo.On("GetByID", ctx, existing.ID).Return(existing, nil)
	registerSharePresence(t, hub, existing, userID, "presenter")

	share, err := svc.Create(ctx, wsID, userID, dto.CreateScreenShareRequest{})

	assert.ErrorIs(t, err, ErrScreenShareAlreadyActive)
	assert.Nil(t, share)
	shareRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestScreenShareService_End(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)

	ctx := t.Context()
	presenterID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)
	// Register live presence so ClearShare has something to drop.
	registerSharePresence(t, hub, share, presenterID, "presenter")

	_, err := svc.End(ctx, share.WorkspaceID, share.ID, presenterID, share.PresenterToken, false)
	assert.NoError(t, err)

	viewers, online := hub.ShareState(share.ID)
	assert.Equal(t, 0, viewers)
	assert.False(t, online, "End must clear hub presence")
}

func TestScreenShareService_EndForbiddenForOthers(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := t.Context()
	presenterID := uuid.New()
	otherUser := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)

	_, err := svc.End(ctx, share.WorkspaceID, share.ID, otherUser, uuid.Nil, false)
	assert.ErrorIs(t, err, ErrScreenShareForbidden)
}

func TestScreenShareService_EndAllowsAdminForce(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := t.Context()
	presenterID := uuid.New()
	adminID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)

	_, err := svc.End(ctx, share.WorkspaceID, share.ID, adminID, uuid.Nil, true)
	assert.NoError(t, err)
}

func TestScreenShareService_ListReportsHubCounts(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	viewerA := uuid.New()
	viewerB := uuid.New()
	live := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Minute)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{live}, nil)
	shareRepo.On("GetByID", ctx, live.ID).Return(&live, nil)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	registerSharePresence(t, hub, &live, presenterID, "presenter")
	registerSharePresence(t, hub, &live, viewerA, "viewer")
	registerSharePresence(t, hub, &live, viewerB, "viewer")

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1)
	assert.Equal(t, live.ID.String(), list.Active[0].ID)
	assert.Equal(t, 2, list.Active[0].ViewerCount, "distinct viewer users from the hub")
}

func TestScreenShareService_ListGCsSourcelessShares(t *testing.T) {
	shareRepo, userRepo, _, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	// Presenter dropped without ending; their last contact is far older
	// than the reconnect grace period.
	stale := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-noSourceGraceSeconds * 2 * time.Second), LastPresenterSeenAt: time.Now().Add(-noSourceGraceSeconds * 2 * time.Second)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{stale}, nil)
	shareRepo.On("GetByID", ctx, stale.ID).Return(&stale, nil)
	shareRepo.On("MarkEnded", ctx, stale.ID).Return(nil)
	ended := stale
	ended.Status = domain.ScreenShareStatusEnded
	ended.EndedAt = new(time.Now())
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{ended}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Empty(t, list.Active, "share with no presenter contact past the grace must be auto-removed")
	assert.Len(t, list.Recent, 1)
	assert.Equal(t, stale.ID.String(), list.Recent[0].ID)
	assert.Equal(t, domain.ScreenShareStatusEnded, list.Recent[0].Status)
	assert.NotNil(t, list.Recent[0].EndedAt)
}

func TestScreenShareService_ListKeepsFreshSourcelessShares(t *testing.T) {
	shareRepo, userRepo, _, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	// Last presenter contact seconds ago: the presenter may simply be
	// mid-reconnect (a WS drop, a page reload — both re-announce quickly).
	fresh := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-5 * time.Second), LastPresenterSeenAt: time.Now().Add(-5 * time.Second)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{fresh}, nil)
	shareRepo.On("GetByID", ctx, fresh.ID).Return(&fresh, nil)
	shareRepo.On("MarkEnded", ctx, mock.Anything).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "share within the presenter-contact grace must survive")
}

func TestScreenShareService_ListGrantsRestartGrace(t *testing.T) {
	shareRepo := new(mockScreenShareRepo)
	userRepo := new(mockUserRepoForShares)
	// A freshly restarted backend: every previously live share gets a new
	// grace window so reconnecting presenters (WS backoff) re-adopt their
	// share instead of finding it GC'd by the first wall load.
	hub := realtime.NewHubStartedAt(time.Now().Add(-2 * time.Second))
	svc := NewScreenShareService(shareRepo, userRepo, hub)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	preRestart := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour), LastPresenterSeenAt: time.Now().Add(-time.Hour)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{preRestart}, nil)
	shareRepo.On("GetByID", ctx, preRestart.ID).Return(&preRestart, nil)
	shareRepo.On("MarkEnded", ctx, mock.Anything).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "a backend restart must not end shares older than the restart grace")
}

func TestScreenShareService_ListUsesHubSightingOverStalePersistedClock(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	// The persisted timestamp is an hour old, but the presenter announced
	// seconds ago and then dropped — the freshest contact governs.
	reconnecting := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour), LastPresenterSeenAt: time.Now().Add(-time.Hour)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{reconnecting}, nil)
	shareRepo.On("GetByID", ctx, reconnecting.ID).Return(&reconnecting, nil)
	shareRepo.On("MarkEnded", ctx, mock.Anything).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	client := hub.Register(nil, wsID, presenterID)
	hub.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool { return true }
	hub.SharePresence(t.Context(), client, reconnecting.ID, "presenter", "join", "")
	hub.Unregister(client)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "a presenter seen seconds ago is within grace even with an old persisted clock")
}

func TestScreenShareService_TouchPresenterSeen(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := t.Context()
	presenterID := uuid.New()
	active := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}
	ended := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusEnded, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, active.ID).Return(active, nil)
	shareRepo.On("TouchPresenterSeen", ctx, active.ID).Return(nil)
	shareRepo.On("GetByID", ctx, ended.ID).Return(ended, nil)

	svc.TouchPresenterSeen(ctx, active.ID)
	svc.TouchPresenterSeen(ctx, ended.ID)

	shareRepo.AssertCalled(t, "TouchPresenterSeen", ctx, active.ID)
	shareRepo.AssertNotCalled(t, "TouchPresenterSeen", ctx, ended.ID)
}

func TestScreenShareService_ListKeepsPresentedSharesRegardlessOfAge(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)

	ctx := t.Context()
	wsID := uuid.New()
	presenterID := uuid.New()
	old := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{old}, nil)
	shareRepo.On("GetByID", ctx, old.ID).Return(&old, nil)
	shareRepo.On("MarkEnded", ctx, mock.Anything).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	registerSharePresence(t, hub, &old, presenterID, "presenter")

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "a live presenter connection is the source of truth, not age")
}

// registerSharePresence announces a share role through the hub's public
// presence API — the same path the WS read pump drives.
func registerSharePresence(t *testing.T, hub *realtime.Hub, share *domain.ScreenShare, userID uuid.UUID, role string) {
	t.Helper()
	client := hub.Register(nil, share.WorkspaceID, userID)
	hub.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool { return true }
	hub.SharePresence(t.Context(), client, share.ID, role, "join", "")
}

func TestScreenShareService_PresenceAuthorization(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive}
	shareRepo.On("GetByID", mock.Anything, share.ID).Return(share, nil)
	assert.True(t, svc.AuthorizePresence(t.Context(), share.WorkspaceID, share.StartedBy, share.ID, "presenter", share.PresenterToken.String()))
	assert.True(t, svc.AuthorizePresence(t.Context(), share.WorkspaceID, uuid.New(), share.ID, "viewer", ""))
	assert.False(t, svc.AuthorizePresence(t.Context(), uuid.New(), share.StartedBy, share.ID, "presenter", share.PresenterToken.String()))
	assert.False(t, svc.AuthorizePresence(t.Context(), share.WorkspaceID, uuid.New(), share.ID, "presenter", share.PresenterToken.String()))
	assert.False(t, svc.AuthorizePresence(t.Context(), share.WorkspaceID, share.StartedBy, share.ID, "presenter", uuid.NewString()))
	share.Status = domain.ScreenShareStatusEnded
	assert.False(t, svc.AuthorizePresence(t.Context(), share.WorkspaceID, share.StartedBy, share.ID, "viewer", ""))
}

func TestScreenShareService_EndRejectsForeignWorkspaceAndOtherTab(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive}
	shareRepo.On("GetByID", t.Context(), share.ID).Return(share, nil)
	_, err := svc.End(t.Context(), uuid.New(), share.ID, uuid.New(), uuid.Nil, true)
	assert.ErrorIs(t, err, ErrScreenShareNotFound, "an admin in a different workspace cannot force-end this share")
	_, err = svc.End(t.Context(), share.WorkspaceID, share.ID, share.StartedBy, uuid.New(), false)
	assert.ErrorIs(t, err, ErrScreenShareForbidden, "another tab of the same presenter cannot end the capture")
	shareRepo.AssertNotCalled(t, "MarkEnded", mock.Anything, mock.Anything)
}

func TestScreenShareService_GCWaitsForInFlightPresenterJoin(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)
	ctx := t.Context()
	share := domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour), LastPresenterSeenAt: time.Now().Add(-time.Hour)}
	entered, resume, joined, sampled := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	resumeJoin := sync.OnceFunc(func() { close(resume) })
	defer resumeJoin()
	hub.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool {
		close(entered)
		<-resume
		return true
	}
	client := hub.Register(nil, share.WorkspaceID, share.StartedBy)
	go func() {
		defer close(joined)
		hub.SharePresence(ctx, client, share.ID, "presenter", "join", share.PresenterToken.String())
	}()
	<-entered
	shareRepo.On("ListActiveByWorkspace", ctx, share.WorkspaceID).Return([]domain.ScreenShare{share}, nil).Run(func(mock.Arguments) { close(sampled) })
	shareRepo.On("GetByID", ctx, share.ID).Return(&share, nil)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, share.WorkspaceID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, share.StartedBy).Return(&domain.User{ID: share.StartedBy}, nil)
	type result struct {
		list dto.ScreenShareListResponse
		err  error
	}
	finished := make(chan result, 1)
	go func() { list, err := svc.List(ctx, share.WorkspaceID); finished <- result{list, err} }()
	<-sampled
	select {
	case <-finished:
		t.Fatal("GC must wait for an already authorized presenter join")
	case <-time.After(100 * time.Millisecond):
	}
	resumeJoin()
	<-joined
	resultValue := <-finished
	assert.NoError(t, resultValue.err)
	assert.Len(t, resultValue.list.Active, 1, "the rejoined presenter must not be garbage-collected")
	shareRepo.AssertNotCalled(t, "MarkEnded", mock.Anything, mock.Anything)
}

func TestScreenShareService_ListSkipsShareEndedAfterInitialSnapshot(t *testing.T) {
	shareRepo, userRepo, _, svc := newShareTestService(t)
	ctx := t.Context()
	share := domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now(), LastPresenterSeenAt: time.Now()}
	// List captures an active row, then End commits before List takes its
	// per-share lifecycle lock. Its current read must honor that End.
	shareRepo.On("ListActiveByWorkspace", ctx, share.WorkspaceID).Return([]domain.ScreenShare{share}, nil).Run(func(mock.Arguments) {
		_, err := svc.End(ctx, share.WorkspaceID, share.ID, share.StartedBy, share.PresenterToken, false)
		assert.NoError(t, err)
	})
	shareRepo.On("GetByID", ctx, share.ID).Return(&share, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil).Once().Run(func(mock.Arguments) {
		share.Status = domain.ScreenShareStatusEnded
		share.EndedAt = new(time.Now())
	})
	endedShare := share
	endedShare.Status = domain.ScreenShareStatusEnded
	endedShare.EndedAt = new(time.Now())
	shareRepo.On("ListRecentEndedByWorkspace", ctx, share.WorkspaceID, recentEndedLimit).Return([]domain.ScreenShare{endedShare}, nil)
	userRepo.On("GetByID", ctx, share.StartedBy).Return(&domain.User{ID: share.StartedBy}, nil)
	list, err := svc.List(ctx, share.WorkspaceID)
	assert.NoError(t, err)
	assert.Empty(t, list.Active, "an ended share must never be reintroduced as active by the old list snapshot")
	assert.Len(t, list.Recent, 1)
	assert.Equal(t, domain.ScreenShareStatusEnded, list.Recent[0].Status)
	shareRepo.AssertNumberOfCalls(t, "MarkEnded", 1)
}

func TestScreenShareService_CreateRetiresExpiredSourceWithoutList(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)
	ctx := t.Context()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour), LastPresenterSeenAt: time.Now().Add(-time.Hour)}
	shareRepo.On("GetActiveByPresenter", ctx, existing.WorkspaceID, existing.StartedBy).Return(existing, nil)
	shareRepo.On("GetByID", ctx, existing.ID).Return(existing, nil)
	shareRepo.On("MarkEnded", ctx, existing.ID).Return(nil).Once()
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Return(nil).Once()
	created, err := svc.Create(ctx, existing.WorkspaceID, existing.StartedBy, dto.CreateScreenShareRequest{})
	assert.NoError(t, err)
	assert.NotEqual(t, existing.ID, created.ID)
	assert.NotEqual(t, existing.PresenterToken, created.PresenterToken)
	shareRepo.AssertExpectations(t)
}

func TestScreenShareService_CreatePreservesOfflineReconnectGrace(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)
	ctx := t.Context()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), Status: domain.ScreenShareStatusActive, LastPresenterSeenAt: time.Now()}
	shareRepo.On("GetActiveByPresenter", ctx, existing.WorkspaceID, existing.StartedBy).Return(existing, nil)
	shareRepo.On("GetByID", ctx, existing.ID).Return(existing, nil)
	created, err := svc.Create(ctx, existing.WorkspaceID, existing.StartedBy, dto.CreateScreenShareRequest{})
	assert.ErrorIs(t, err, ErrScreenShareAlreadyActive)
	assert.Nil(t, created)
	shareRepo.AssertNotCalled(t, "MarkEnded", mock.Anything, mock.Anything)
	shareRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestScreenShareService_CreateRetiresShareBeyondReconnectWindow(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)
	ctx := t.Context()
	// Offline for longer than one reconnect cycle but well inside the 45s
	// GC grace: the previous tab is gone (crash, reload, failed Stop), so a
	// fresh capture must claim the slot instead of failing the start.
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Minute), LastPresenterSeenAt: time.Now().Add(-20 * time.Second)}
	shareRepo.On("GetActiveByPresenter", ctx, existing.WorkspaceID, existing.StartedBy).Return(existing, nil)
	shareRepo.On("GetByID", ctx, existing.ID).Return(existing, nil)
	shareRepo.On("MarkEnded", ctx, existing.ID).Return(nil).Once()
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Return(nil).Once()
	created, err := svc.Create(ctx, existing.WorkspaceID, existing.StartedBy, dto.CreateScreenShareRequest{})
	assert.NoError(t, err)
	assert.NotEqual(t, existing.ID, created.ID)
	shareRepo.AssertExpectations(t)
}

func TestScreenShareService_CreateWaitsForReconnectingPresenter(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)
	ctx := t.Context()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, LastPresenterSeenAt: time.Now().Add(-time.Hour)}
	entered, resume, joined, lookedUp := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	resumeJoin := sync.OnceFunc(func() { close(resume) })
	defer resumeJoin()
	hub.AuthorizeSharePresence = func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) bool {
		close(entered)
		<-resume
		return true
	}
	client := hub.Register(nil, existing.WorkspaceID, existing.StartedBy)
	go func() {
		defer close(joined)
		hub.SharePresence(ctx, client, existing.ID, "presenter", "join", existing.PresenterToken.String())
	}()
	<-entered
	shareRepo.On("GetActiveByPresenter", ctx, existing.WorkspaceID, existing.StartedBy).Return(existing, nil).Run(func(mock.Arguments) { close(lookedUp) })
	shareRepo.On("GetByID", ctx, existing.ID).Return(existing, nil)
	finished := make(chan error, 1)
	go func() {
		_, err := svc.Create(ctx, existing.WorkspaceID, existing.StartedBy, dto.CreateScreenShareRequest{})
		finished <- err
	}()
	<-lookedUp
	select {
	case <-finished:
		t.Fatal("a second tab must wait for the in-flight presenter rejoin")
	case <-time.After(100 * time.Millisecond):
	}
	resumeJoin()
	<-joined
	assert.ErrorIs(t, <-finished, ErrScreenShareAlreadyActive)
	shareRepo.AssertNotCalled(t, "MarkEnded", mock.Anything, mock.Anything)
	shareRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestScreenShareService_ListRecentSnapshotWinsAfterActiveRead(t *testing.T) {
	shareRepo, userRepo, _, svc := newShareTestService(t)
	ctx := t.Context()
	share := domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: uuid.New(), PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive, LastPresenterSeenAt: time.Now()}
	endedShare := share
	endedShare.Status = domain.ScreenShareStatusEnded
	endedShare.EndedAt = new(time.Now())
	shareRepo.On("ListActiveByWorkspace", ctx, share.WorkspaceID).Return([]domain.ScreenShare{share}, nil)
	shareRepo.On("GetByID", ctx, share.ID).Return(&share, nil)
	userRepo.On("GetByID", ctx, share.StartedBy).Return(&domain.User{ID: share.StartedBy}, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil).Once().Run(func(mock.Arguments) {
		share.Status = domain.ScreenShareStatusEnded
		share.EndedAt = endedShare.EndedAt
	})
	shareRepo.On("ListRecentEndedByWorkspace", ctx, share.WorkspaceID, recentEndedLimit).Return([]domain.ScreenShare{endedShare}, nil).Run(func(mock.Arguments) {
		_, err := svc.End(ctx, share.WorkspaceID, share.ID, share.StartedBy, share.PresenterToken, false)
		assert.NoError(t, err)
	})
	list, err := svc.List(ctx, share.WorkspaceID)
	assert.NoError(t, err)
	assert.Empty(t, list.Active, "the later ended snapshot must override the previously decorated active share")
	assert.Len(t, list.Recent, 1)
	assert.Equal(t, share.ID.String(), list.Recent[0].ID)
	assert.Equal(t, domain.ScreenShareStatusEnded, list.Recent[0].Status)
	shareRepo.AssertNumberOfCalls(t, "MarkEnded", 1)
}
