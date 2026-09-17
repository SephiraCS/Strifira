package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/realtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/twitchtv/twirp"
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
	return nil, args.Error(1)
}

func (m *mockUserRepoForShares) Update(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

type mockLiveKit struct {
	mock.Mock
}

func (m *mockLiveKit) ServerURL() string {
	return "wss://livekit.example.test"
}

func (m *mockLiveKit) PresenterToken(room, identity, name string) (string, error) {
	args := m.Called(room, identity, name)
	return args.String(0), args.Error(1)
}

func (m *mockLiveKit) ViewerToken(room, identity, name string) (string, error) {
	args := m.Called(room, identity, name)
	return args.String(0), args.Error(1)
}

func (m *mockLiveKit) ViewerCount(ctx context.Context, room, presenterIdentity string) (int, error) {
	args := m.Called(ctx, room, presenterIdentity)
	return args.Int(0), args.Error(1)
}

func (m *mockLiveKit) DeleteRoom(ctx context.Context, room string) error {
	args := m.Called(ctx, room)
	return args.Error(0)
}

// --- Tests ---

func newShareTestService(t *testing.T) (*mockScreenShareRepo, *mockUserRepoForShares, *mockLiveKit, *ScreenShareService) {
	shareRepo := new(mockScreenShareRepo)
	userRepo := new(mockUserRepoForShares)
	lk := new(mockLiveKit)
	svc := NewScreenShareService(shareRepo, userRepo, realtime.NewHub(), lk)
	return shareRepo, userRepo, lk, svc
}

func TestScreenShareService_DisabledWithoutGateway(t *testing.T) {
	svc := NewScreenShareService(new(mockScreenShareRepo), new(mockUserRepoForShares), realtime.NewHub(), nil)

	_, _, err := svc.Create(context.Background(), uuid.New(), uuid.New(), dto.CreateScreenShareRequest{})
	assert.ErrorIs(t, err, ErrScreenSharingDisabled)

	_, err = svc.End(context.Background(), uuid.New(), uuid.New(), false)
	assert.ErrorIs(t, err, ErrScreenSharingDisabled)

	// Listing stays available and empty rather than erroring.
	list, err := svc.List(context.Background(), uuid.New())
	assert.NoError(t, err)
	assert.Empty(t, list.Active)
	assert.Empty(t, list.Recent)
}

func TestScreenShareService_Create(t *testing.T) {
	shareRepo, userRepo, lk, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	userID := uuid.New()

	shareRepo.On("GetActiveByPresenter", ctx, wsID, userID).Return(nil, nil)
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Return(nil).Run(func(args mock.Arguments) {
		share := args.Get(1).(*domain.ScreenShare)
		share.ID = uuid.New()
		share.CreatedAt = share.CreatedAt.UTC()
	})
	userRepo.On("GetByID", ctx, userID).Return(&domain.User{ID: userID, Name: "Mario", DisplayName: "Mario"}, nil)
	lk.On("PresenterToken", mock.AnythingOfType("string"), userID.String(), "Mario").Return("presenter-jwt", nil)

	share, token, err := svc.Create(ctx, wsID, userID, dto.CreateScreenShareRequest{})

	assert.NoError(t, err)
	assert.Equal(t, domain.ScreenShareStatusActive, share.Status)
	assert.Equal(t, wsID, share.WorkspaceID)
	assert.Equal(t, userID, share.StartedBy)
	assert.Equal(t, "presenter-jwt", token)
	assert.Contains(t, share.RoomName, "ku-")
}

func TestScreenShareService_CreateIdempotentForPresenter(t *testing.T) {
	shareRepo, userRepo, lk, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	userID := uuid.New()
	existing := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: userID, RoomName: "ku-existing", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetActiveByPresenter", ctx, wsID, userID).Return(existing, nil)
	shareRepo.On("Create", ctx, mock.AnythingOfType("*domain.ScreenShare")).Times(0)
	userRepo.On("GetByID", ctx, userID).Return(&domain.User{ID: userID, Name: "Mario"}, nil)
	lk.On("PresenterToken", "ku-existing", userID.String(), "Mario").Return("presenter-jwt", nil)

	share, token, err := svc.Create(ctx, wsID, userID, dto.CreateScreenShareRequest{})

	assert.NoError(t, err)
	assert.Equal(t, existing.ID, share.ID, "existing active share should be returned")
	assert.Equal(t, "presenter-jwt", token)
}

func TestScreenShareService_JoinTokenRoles(t *testing.T) {
	shareRepo, userRepo, lk, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	viewerID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, RoomName: "ku-room", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)
	userRepo.On("GetByID", ctx, viewerID).Return(&domain.User{ID: viewerID, Name: "Filippo"}, nil)
	lk.On("PresenterToken", "ku-room", presenterID.String(), "Mario").Return("presenter-jwt", nil)
	lk.On("ViewerToken", "ku-room", viewerID.String(), "Filippo").Return("viewer-jwt", nil)

	_, token, err := svc.JoinToken(ctx, wsID, share.ID, presenterID)
	assert.NoError(t, err)
	assert.Equal(t, "presenter-jwt", token)

	_, token, err = svc.JoinToken(ctx, wsID, share.ID, viewerID)
	assert.NoError(t, err)
	assert.Equal(t, "viewer-jwt", token)
}

func TestScreenShareService_JoinTokenRejectsEnded(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), RoomName: "ku-room", Status: domain.ScreenShareStatusEnded}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)

	_, _, err := svc.JoinToken(ctx, share.WorkspaceID, share.ID, uuid.New())
	assert.ErrorIs(t, err, ErrScreenShareNotFound)
}

func TestScreenShareService_JoinTokenRejectsForeignWorkspace(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), RoomName: "ku-room", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)

	_, _, err := svc.JoinToken(ctx, uuid.New(), share.ID, uuid.New())
	assert.ErrorIs(t, err, ErrScreenShareNotFound)
}

func TestScreenShareService_End(t *testing.T) {
	shareRepo, _, lk, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, RoomName: "ku-room", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	lk.On("DeleteRoom", ctx, "ku-room").Return(nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)

	_, err := svc.End(ctx, share.ID, presenterID, false)
	assert.NoError(t, err)
}

func TestScreenShareService_EndForbiddenForOthers(t *testing.T) {
	shareRepo, _, _, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	otherUser := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, RoomName: "ku-room", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)

	_, err := svc.End(ctx, share.ID, otherUser, false)
	assert.ErrorIs(t, err, ErrScreenShareForbidden)
}

func TestScreenShareService_EndAllowsAdminForce(t *testing.T) {
	shareRepo, _, lk, svc := newShareTestService(t)

	ctx := context.Background()
	presenterID := uuid.New()
	adminID := uuid.New()
	share := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: uuid.New(), StartedBy: presenterID, RoomName: "ku-room", Status: domain.ScreenShareStatusActive}

	shareRepo.On("GetByID", ctx, share.ID).Return(share, nil)
	lk.On("DeleteRoom", ctx, "ku-room").Return(nil)
	shareRepo.On("MarkEnded", ctx, share.ID).Return(nil)

	_, err := svc.End(ctx, share.ID, adminID, true)
	assert.NoError(t, err)
}

func TestScreenShareService_ListGCsRoomsGone(t *testing.T) {
	shareRepo, userRepo, lk, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	live := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, RoomName: "ku-live", Status: domain.ScreenShareStatusActive}
	stale := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, RoomName: "ku-stale", Status: domain.ScreenShareStatusActive}
	ended := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, RoomName: "ku-old", Status: domain.ScreenShareStatusEnded}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{live, stale}, nil)
	lk.On("ViewerCount", ctx, "ku-live", presenterID.String()).Return(3, nil)
	lk.On("ViewerCount", ctx, "ku-stale", presenterID.String()).Return(0, twirp.NotFound.Error("room not found"))
	shareRepo.On("MarkEnded", ctx, stale.ID).Return(nil)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{ended, stale}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1)
	assert.Equal(t, live.ID.String(), list.Active[0].ID)
	assert.Equal(t, 3, list.Active[0].ViewerCount)
	assert.Len(t, list.Recent, 2, "stale share should move to history after GC")
}

func TestScreenShareService_ListSurvivesLiveKitOutage(t *testing.T) {
	shareRepo, userRepo, lk, svc := newShareTestService(t)

	ctx := context.Background()
	wsID := uuid.New()
	presenterID := uuid.New()
	live := domain.ScreenShare{ID: uuid.New(), WorkspaceID: wsID, StartedBy: presenterID, RoomName: "ku-live", Status: domain.ScreenShareStatusActive}

	shareRepo.On("ListActiveByWorkspace", ctx, wsID).Return([]domain.ScreenShare{live}, nil)
	lk.On("ViewerCount", ctx, "ku-live", presenterID.String()).Return(0, errors.New("connection refused"))
	shareRepo.On("MarkEnded", ctx, live.ID).Times(0)
	shareRepo.On("ListRecentEndedByWorkspace", ctx, wsID, recentEndedLimit).Return([]domain.ScreenShare{}, nil)
	userRepo.On("GetByID", ctx, presenterID).Return(&domain.User{ID: presenterID, Name: "Mario"}, nil)

	list, err := svc.List(ctx, wsID)

	assert.NoError(t, err)
	assert.Len(t, list.Active, 1, "transport failure must not drop the share")
	assert.Equal(t, 0, list.Active[0].ViewerCount)
}
