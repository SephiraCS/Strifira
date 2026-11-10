package service

import (
	"context"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockChatChannelRepo struct {
	mock.Mock
}

func (m *mockChatChannelRepo) CreateChannel(ctx context.Context, channel *domain.ChatChannel, memberIDs []uuid.UUID) error {
	args := m.Called(ctx, channel, memberIDs)
	return args.Error(0)
}

func (m *mockChatChannelRepo) GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.ChatChannel, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChatChannel), args.Error(1)
}

func (m *mockChatChannelRepo) GetDMByUsers(ctx context.Context, workspaceID, userA, userB uuid.UUID) (*domain.ChatChannel, error) {
	args := m.Called(ctx, workspaceID, userA, userB)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChatChannel), args.Error(1)
}

func (m *mockChatChannelRepo) ListVisibleByWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.ChatChannel, error) {
	args := m.Called(ctx, workspaceID, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.ChatChannel), args.Error(1)
}

func (m *mockChatChannelRepo) EnsureDefaultChannel(ctx context.Context, workspaceID, userID uuid.UUID) error {
	args := m.Called(ctx, workspaceID, userID)
	return args.Error(0)
}

func (m *mockChatChannelRepo) AddChannelMember(ctx context.Context, channelID, userID uuid.UUID) error {
	args := m.Called(ctx, channelID, userID)
	return args.Error(0)
}

func (m *mockChatChannelRepo) IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	args := m.Called(ctx, channelID, userID)
	return args.Bool(0), args.Error(1)
}

func (m *mockChatChannelRepo) ChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	args := m.Called(ctx, channelID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]uuid.UUID), args.Error(1)
}

func (m *mockChatChannelRepo) UpdateChannelTopic(ctx context.Context, channelID uuid.UUID, topic string) error {
	args := m.Called(ctx, channelID, topic)
	return args.Error(0)
}

func (m *mockChatChannelRepo) UserByID(ctx context.Context, userID uuid.UUID) (*domain.ChatUser, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChatUser), args.Error(1)
}

type mockChatMessageRepo struct {
	mock.Mock
}

func (m *mockChatMessageRepo) CreateMessage(ctx context.Context, message *domain.ChatMessage) error {
	args := m.Called(ctx, message)
	return args.Error(0)
}

func (m *mockChatMessageRepo) GetMessageByID(ctx context.Context, id uuid.UUID) (*domain.ChatMessage, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChatMessage), args.Error(1)
}

func (m *mockChatMessageRepo) ListMessages(ctx context.Context, channelID uuid.UUID, before *time.Time, limit int) ([]domain.ChatMessage, error) {
	args := m.Called(ctx, channelID, before, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.ChatMessage), args.Error(1)
}

func (m *mockChatMessageRepo) UpdateMessageBody(ctx context.Context, id uuid.UUID, body string) (*domain.ChatMessage, error) {
	args := m.Called(ctx, id, body)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.ChatMessage), args.Error(1)
}

func (m *mockChatMessageRepo) SoftDeleteMessage(ctx context.Context, id uuid.UUID) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

// mockWorkspaceMembersRepo embeds the interface so the mock satisfies
// repository.WorkspaceRepo; only the methods the chat service calls are
// implemented (anything else would call a nil embedded value and panic
// loudly in tests).
type mockWorkspaceMembersRepo struct {
	mock.Mock
	repository.WorkspaceRepo
}

func (m *mockWorkspaceMembersRepo) GetMember(ctx context.Context, workspaceID, userID uuid.UUID) (*domain.WorkspaceMember, error) {
	args := m.Called(ctx, workspaceID, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.WorkspaceMember), args.Error(1)
}

type mockNotificationStore struct {
	mock.Mock
}

func (m *mockNotificationStore) Create(ctx context.Context, n *domain.Notification) error {
	args := m.Called(ctx, n)
	return args.Error(0)
}

func (m *mockNotificationStore) CreateOrRefresh(ctx context.Context, n *domain.Notification, window time.Duration) error {
	args := m.Called(ctx, n, window)
	return args.Error(0)
}

func (m *mockNotificationStore) GetByID(ctx context.Context, id uuid.UUID) (*domain.Notification, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.Notification), args.Error(1)
}

func (m *mockNotificationStore) ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.Notification, error) {
	args := m.Called(ctx, userID, limit, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.Notification), args.Error(1)
}

func (m *mockNotificationStore) ListSnoozed(ctx context.Context, userID uuid.UUID) ([]domain.Notification, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.Notification), args.Error(1)
}

func (m *mockNotificationStore) ListArchived(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Notification, error) {
	args := m.Called(ctx, userID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.Notification), args.Error(1)
}

func (m *mockNotificationStore) Update(ctx context.Context, n *domain.Notification) error {
	args := m.Called(ctx, n)
	return args.Error(0)
}

func (m *mockNotificationStore) MarkAllRead(ctx context.Context, userID uuid.UUID) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

func (m *mockNotificationStore) UnreadCount(ctx context.Context, userID uuid.UUID) (int, error) {
	args := m.Called(ctx, userID)
	return args.Int(0), args.Error(1)
}

// --- Helpers ---

func newChatTestService(t *testing.T) (*ChatService, *mockChatChannelRepo, *mockChatMessageRepo, *mockNotificationStore) {
	t.Helper()
	channels := &mockChatChannelRepo{}
	messages := &mockChatMessageRepo{}
	members := &mockWorkspaceMembersRepo{}
	notifs := &mockNotificationStore{}
	notifSvc := NewNotificationService(notifs)
	svc := NewChatService(channels, messages, members, realtime.NewHub(), notifSvc)
	return svc, channels, messages, notifs
}

func dmTestChannel(channelID, aliceID, bobID uuid.UUID) *domain.ChatChannel {
	return &domain.ChatChannel{
		ID:          channelID,
		WorkspaceID: uuid.New(),
		Type:        domain.ChatChannelDM,
		CreatedBy:   aliceID,
	}
}

// --- Tests ---

func TestChatEnsureDMRejectsSelf(t *testing.T) {
	svc, _, _, _ := newChatTestService(t)
	alice := uuid.New()
	_, err := svc.EnsureDM(context.Background(), uuid.New(), alice, alice)
	assert.ErrorIs(t, err, ErrChatSelfDM)
}

func TestChatEnsureDMRequiresWorkspaceMember(t *testing.T) {
	svc, channels, _, _ := newChatTestService(t)
	workspace, alice, stranger := uuid.New(), uuid.New(), uuid.New()
	channels.On("GetDMByUsers", mock.Anything, workspace, alice, stranger).Return(nil, nil).Once()
	membersRepo := svc.workspaceRepo.(*mockWorkspaceMembersRepo)
	membersRepo.On("GetMember", mock.Anything, workspace, stranger).Return(nil, nil).Once()

	_, err := svc.EnsureDM(context.Background(), workspace, alice, stranger)
	assert.ErrorIs(t, err, ErrChatUserNotMember)
}

func TestChatEnsureDMReusesExistingChannel(t *testing.T) {
	svc, channels, _, _ := newChatTestService(t)
	workspace, alice, bob := uuid.New(), uuid.New(), uuid.New()
	existing := dmTestChannel(uuid.New(), alice, bob)
	existing.WorkspaceID = workspace

	membersRepo := svc.workspaceRepo.(*mockWorkspaceMembersRepo)
	membersRepo.On("GetMember", mock.Anything, workspace, bob).Return(&domain.WorkspaceMember{WorkspaceID: workspace, UserID: bob}, nil).Once()
	channels.On("GetDMByUsers", mock.Anything, workspace, alice, bob).Return(existing, nil).Once()
	channels.On("ChannelMemberIDs", mock.Anything, existing.ID).Return([]uuid.UUID{alice, bob}, nil).Once()
	channels.On("UserByID", mock.Anything, bob).Return(&domain.ChatUser{ID: bob, Name: "Bob"}, nil).Once()

	channel, err := svc.EnsureDM(context.Background(), workspace, alice, bob)
	assert.NoError(t, err)
	assert.Equal(t, existing.ID, channel.ID)
	assert.Equal(t, bob, channel.DMPeer.ID)
	// The membership check short-circuits before any write.
	channels.AssertNotCalled(t, "CreateChannel", mock.Anything, mock.Anything, mock.Anything)
}

// Both sides of a conversation must see the other participant as the DM peer,
// regardless of who created the channel: the invited viewer would otherwise
// see the conversation labelled with their own name.
func TestChatDMPeerIsViewerRelative(t *testing.T) {
	svc, channels, _, _ := newChatTestService(t)
	workspace, alice, bob := uuid.New(), uuid.New(), uuid.New()
	channelID := uuid.New()
	dm := dmTestChannel(channelID, alice, bob)
	dm.WorkspaceID = workspace
	// The repository decorates against the creator, so both views start with
	// bob resolved (wrong for the invited side).
	dm.DMPeer = &domain.ChatUser{ID: bob, Name: "Bob"}

	membersRepo := svc.workspaceRepo.(*mockWorkspaceMembersRepo)
	membersRepo.On("GetMember", mock.Anything, workspace, alice).Return(&domain.WorkspaceMember{WorkspaceID: workspace, UserID: alice}, nil).Once()
	membersRepo.On("GetMember", mock.Anything, workspace, bob).Return(&domain.WorkspaceMember{WorkspaceID: workspace, UserID: bob}, nil).Once()
	channels.On("GetDMByUsers", mock.Anything, workspace, bob, alice).Return(dm, nil).Once()
	channels.On("GetDMByUsers", mock.Anything, workspace, alice, bob).Return(dm, nil).Once()
	channels.On("ChannelMemberIDs", mock.Anything, channelID).Return([]uuid.UUID{alice, bob}, nil).Once()
	channels.On("UserByID", mock.Anything, alice).Return(&domain.ChatUser{ID: alice, Name: "Alice"}, nil).Once()

	channel, err := svc.EnsureDM(context.Background(), workspace, bob, alice)
	assert.NoError(t, err)
	require.NotNil(t, channel.DMPeer)
	assert.Equal(t, alice, channel.DMPeer.ID, "invited viewer sees the creator as the peer")

	channels.On("ChannelMemberIDs", mock.Anything, channelID).Return([]uuid.UUID{alice, bob}, nil).Once()
	channels.On("UserByID", mock.Anything, bob).Return(&domain.ChatUser{ID: bob, Name: "Bob"}, nil).Once()

	channel, err = svc.EnsureDM(context.Background(), workspace, alice, bob)
	assert.NoError(t, err)
	require.NotNil(t, channel.DMPeer)
	assert.Equal(t, bob, channel.DMPeer.ID, "creator sees the invited user as the peer")
}

func TestChatSendMessageInDMNotifiesOnlyPeer(t *testing.T) {
	svc, channels, messages, notifs := newChatTestService(t)
	workspace, alice, bob := uuid.New(), uuid.New(), uuid.New()
	channelID := uuid.New()
	dm := dmTestChannel(channelID, alice, bob)
	dm.WorkspaceID = workspace

	channels.On("GetChannelByID", mock.Anything, channelID).Return(dm, nil).Once()
	channels.On("IsChannelMember", mock.Anything, channelID, alice).Return(true, nil).Once()
	channels.On("ChannelMemberIDs", mock.Anything, channelID).Return([]uuid.UUID{alice, bob}, nil).Once()
	// The real repository reads the message back with its author join;
	// emulate that so notifications and broadcasts carry the author.
	created := &domain.ChatMessage{
		ID:        uuid.New(),
		ChannelID: channelID,
		UserID:    alice,
		Author:    &domain.ChatUser{ID: alice, Name: "Alice Souza"},
		Body:      "hello",
	}
	messages.On("CreateMessage", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*args.Get(1).(*domain.ChatMessage) = *created
	}).Return(nil).Once()
	notifs.On("Create", mock.Anything, mock.Anything).Return(nil).Once()

	message, err := svc.SendMessage(context.Background(), workspace, alice, channelID, dto.CreateChatMessageRequest{Body: "hello"})
	assert.NoError(t, err)
	assert.Equal(t, "hello", message.Body)
	assert.Equal(t, alice, message.Author.ID)

	// Exactly one notification, targeted at the peer (not the author).
	notifs.AssertExpectations(t)
	notification := notifs.Calls[0].Arguments.Get(1).(*domain.Notification)
	assert.Equal(t, bob, notification.UserID)
	assert.Equal(t, "chat_message", notification.Type)
	assert.Contains(t, notification.Title, "Alice Souza")
}

func TestChatEditRejectsNonAuthor(t *testing.T) {
	svc, channels, messages, _ := newChatTestService(t)
	workspace, alice, bob := uuid.New(), uuid.New(), uuid.New()
	channelID := uuid.New()
	dm := dmTestChannel(channelID, alice, bob)
	dm.WorkspaceID = workspace
	original := &domain.ChatMessage{ID: uuid.New(), ChannelID: channelID, UserID: alice, Body: "original"}

	messages.On("GetMessageByID", mock.Anything, original.ID).Return(original, nil).Once()
	channels.On("GetChannelByID", mock.Anything, channelID).Return(dm, nil).Once()
	channels.On("IsChannelMember", mock.Anything, channelID, bob).Return(true, nil).Once()

	_, err := svc.EditMessage(context.Background(), workspace, bob, original.ID, dto.UpdateChatMessageRequest{Body: "hijack"})
	assert.ErrorIs(t, err, ErrChatMessageForbidden)
}

func TestChatDeleteRejectsNonAuthor(t *testing.T) {
	svc, channels, messages, _ := newChatTestService(t)
	workspace, alice, bob := uuid.New(), uuid.New(), uuid.New()
	channelID := uuid.New()
	dm := dmTestChannel(channelID, alice, bob)
	dm.WorkspaceID = workspace
	original := &domain.ChatMessage{ID: uuid.New(), ChannelID: channelID, UserID: alice, Body: "original"}

	messages.On("GetMessageByID", mock.Anything, original.ID).Return(original, nil).Once()
	channels.On("GetChannelByID", mock.Anything, channelID).Return(dm, nil).Once()
	channels.On("IsChannelMember", mock.Anything, channelID, bob).Return(true, nil).Once()

	err := svc.DeleteMessage(context.Background(), workspace, bob, original.ID)
	assert.ErrorIs(t, err, ErrChatMessageForbidden)
}

func TestChatTypingAudienceHiddenChannelRefuses(t *testing.T) {
	svc, channels, _, _ := newChatTestService(t)
	workspace, alice := uuid.New(), uuid.New()
	channelID := uuid.New()
	dm := dmTestChannel(channelID, alice, uuid.New())
	dm.WorkspaceID = workspace

	channels.On("GetChannelByID", mock.Anything, channelID).Return(dm, nil).Once()
	channels.On("IsChannelMember", mock.Anything, channelID, alice).Return(false, nil).Once()

	recipients, workspaceWide, visible := svc.TypingAudience(context.Background(), workspace, alice, channelID)
	assert.False(t, visible)
	assert.Nil(t, recipients)
	assert.False(t, workspaceWide)
}

func TestChatReservedDefaultName(t *testing.T) {
	svc, _, _, _ := newChatTestService(t)
	_, err := svc.CreateChannel(context.Background(), uuid.New(), uuid.New(), dto.CreateChatChannelRequest{Name: "general", Type: "public"})
	assert.ErrorIs(t, err, ErrChatReservedName)
}
