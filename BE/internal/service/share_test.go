package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
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
	hub := realtime.NewHub()
	svc := NewScreenShareService(shareRepo, userRepo, hub)
	return shareRepo, userRepo, hub, svc
}

func TestScreenShareService_Create(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)

	ctx := context.Background()
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

func TestScreenShareService_CreateIdempotentForPresenter(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	userID := uuid.New()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: userID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Minute)}

	shareRepo.On("GetActiveByPresenter", ctx, wsID, userID).Return(existing, nil)
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Times(0)

	share, err := svc.Create(ctx, wsID, userID, dto.CreateScreenShareRequest{})

	assert.NoError(t, err)
	assert.Equal(t, existing.ID, share.ID, "existing active share should be returned")
}

func TestScreenShareService_End(t *testing.T) {
	shareRepo, _, hub, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)
	// Register live presence so ClearShare has something to drop.
	registerSharePresence(t, hub, share, presenterID, "presenter")

	_, err := svc.End(ctx, share.ID, presenterID, false)
	assert.NoError(t, err)

	viewers, online := hub.ShareState(share.ID)
	assert.Equal(t, 0, viewers)
	assert.False(t, online, "End must clear hub presence")
}

func TestScreenShareService_EndForbiddenForOthers(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	otherUser := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)

	_, err := svc.End(ctx, share.ID, otherUser, false)
	assert.ErrorIs(t, err, ErrScreenShareForbidden)
}

func TestScreenShareService_EndAllowsAdminForce(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	adminID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now()}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)

	_, err := svc.End(ctx, share.ID, adminID, true)
	assert.NoError(t, err)
}

func TestScreenShareService_ListReportsHubCounts(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	viewerA := uuid.New()
	viewerB := uuid.New()
	live := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Minute)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{live}, nil)
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

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	// Presenter dropped without ending; the share has had no presenter
	// connection for far longer than the reload grace period.
	stale := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-noSourceGraceSeconds * 2 * time.Second)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{stale}, nil)
	shareRepo.On("MarkEnded", ctx, stale.ID).Return(nil)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Empty(t, list.Active, "source-less mature share must be auto-removed")
	assert.Len(t, list.Recent, 1)
	assert.Equal(t, stale.ID.String(), list.Recent[0].ID)
}

func TestScreenShareService_ListKeepsFreshSourcelessShares(t *testing.T) {
	shareRepo, userRepo, _, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	// Share younger than the grace period: the presenter may simply be
	// mid-reload (their wall re-announces presence within seconds).
	fresh := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-5 * time.Second)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{fresh}, nil)
	shareRepo.On("MarkEnded", ctx, mock.Anything).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "fresh share within the reload grace must survive")
}

func TestScreenShareService_ListKeepsPresentedSharesRegardlessOfAge(t *testing.T) {
	shareRepo, userRepo, hub, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	old := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, Status: domain.ScreenShareStatusActive, CreatedAt: time.Now().Add(-time.Hour)}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{old}, nil)
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
	hub.SharePresence(client, share.ID, role, "join")
}
