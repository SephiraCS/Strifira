package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

// Errors surfaced as 4xx responses by the handler.
var (
	ErrChatChannelForbidden = errors.New("channel is not accessible")
	ErrChatMessageForbidden = errors.New("message can only be edited by its author")
	ErrChatMessageDeleted   = errors.New("message was deleted")
	ErrChatEmptyMessage     = errors.New("message body cannot be empty")
	ErrChatUserNotMember    = errors.New("user is not a workspace member")
	ErrChatSelfDM           = errors.New("a direct message to yourself is not supported")
	ErrChatReservedName     = errors.New("this channel name is reserved")
)

// chatDefaultChannel is provisioned lazily so every workspace opens the chat
// into a working channel without any configuration.
const chatDefaultChannel = "general"

// chatMessageListLimit bounds a single page of the timeline; the repository
// fetches one extra row to detect older pages.
const chatMessageListLimit = 50

type ChatService struct {
	channelRepo   repository.ChatChannelRepo
	messageRepo   repository.ChatMessageRepo
	workspaceRepo repository.WorkspaceRepo
	hub           *realtime.Hub
	notifSvc      *NotificationService
}

func NewChatService(
	channelRepo repository.ChatChannelRepo,
	messageRepo repository.ChatMessageRepo,
	workspaceRepo repository.WorkspaceRepo,
	hub *realtime.Hub,
	notifSvc *NotificationService,
) *ChatService {
	return &ChatService{
		channelRepo:   channelRepo,
		messageRepo:   messageRepo,
		workspaceRepo: workspaceRepo,
		hub:           hub,
		notifSvc:      notifSvc,
	}
}

// ListChannels returns the channels the user can see, provisioning the
// default #general channel on the first visit.
func (s *ChatService) ListChannels(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.ChatChannel, error) {
	if err := s.channelRepo.EnsureDefaultChannel(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	channels, err := s.channelRepo.ListVisibleByWorkspace(ctx, workspaceID, userID)
	if err != nil {
		return nil, err
	}
	for i := range channels {
		s.resolveDMPeer(ctx, userID, &channels[i])
	}
	return channels, nil
}

// CreateChannel opens a public or private channel. The creator joins a
// private channel automatically.
func (s *ChatService) CreateChannel(ctx context.Context, workspaceID, userID uuid.UUID, req dto.CreateChatChannelRequest) (*domain.ChatChannel, error) {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if name == "" {
		return nil, ErrChatReservedName
	}
	if name == chatDefaultChannel {
		return nil, ErrChatReservedName
	}
	channelType := domain.ChatChannelPublic
	if req.Type != "" {
		channelType = req.Type
	}

	channel := &domain.ChatChannel{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Type:        channelType,
		Name:        name,
		CreatedBy:   userID,
	}
	if err := s.channelRepo.CreateChannel(ctx, channel, []uuid.UUID{userID}); err != nil {
		return nil, err
	}
	return channel, nil
}

// EnsureDM returns the direct-message channel between the two users,
// creating it on first use.
func (s *ChatService) EnsureDM(ctx context.Context, workspaceID, userID, peerID uuid.UUID) (*domain.ChatChannel, error) {
	if userID == peerID {
		return nil, ErrChatSelfDM
	}
	member, err := s.workspaceRepo.GetMember(ctx, workspaceID, peerID)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, ErrChatUserNotMember
	}

	existing, err := s.channelRepo.GetDMByUsers(ctx, workspaceID, userID, peerID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		s.resolveDMPeer(ctx, userID, existing)
		return existing, nil
	}

	key := repository.DMKey(userID, peerID)
	channel := &domain.ChatChannel{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		Type:        domain.ChatChannelDM,
		DMKey:       &key,
		CreatedBy:   userID,
	}
	if err := s.channelRepo.CreateChannel(ctx, channel, []uuid.UUID{userID, peerID}); err != nil {
		// A concurrent creation of the same pair is a success: read it back.
		if retry, getErr := s.channelRepo.GetDMByUsers(ctx, workspaceID, userID, peerID); getErr == nil && retry != nil {
			return retry, nil
		}
		return nil, err
	}
	// The create path returns an undecorated row; read it back with the DM
	// peer resolved so the API hands the UI a complete identity.
	created, err := s.channelRepo.GetChannelByID(ctx, channel.ID)
	if err != nil {
		return nil, err
	}
	if created == nil {
		return channel, nil
	}
	s.resolveDMPeer(ctx, userID, created)
	return created, nil
}

// resolveDMPeer points a DM channel's peer at the member the viewer is
// talking to — the *other* participant, whoever created the conversation.
// The repository decorates against the creator only, which would label the
// conversation with the viewer's own name on the invited side.
func (s *ChatService) resolveDMPeer(ctx context.Context, viewerID uuid.UUID, channel *domain.ChatChannel) {
	if channel.Type != domain.ChatChannelDM {
		return
	}
	members, err := s.channelRepo.ChannelMemberIDs(ctx, channel.ID)
	if err != nil {
		// Keep the repository-resolved fallback; the rail still renders.
		return
	}
	for _, memberID := range members {
		if memberID == viewerID {
			continue
		}
		peer, err := s.channelRepo.UserByID(ctx, memberID)
		if err != nil || peer == nil {
			return
		}
		channel.DMPeer = peer
		return
	}
}

// ListMessages pages a channel's timeline backwards from the message id in
// before (empty = live edge). Visibility follows the channel rules.
func (s *ChatService) ListMessages(ctx context.Context, workspaceID, userID, channelID uuid.UUID, before string) ([]domain.ChatMessage, bool, error) {
	_, visible, err := s.visibleChannel(ctx, workspaceID, userID, channelID)
	if err != nil {
		return nil, false, err
	}
	if !visible {
		return nil, false, ErrChatChannelForbidden
	}

	var beforeTime *time.Time
	if before != "" {
		cursorID, err := uuid.Parse(before)
		if err != nil {
			return nil, false, err
		}
		cursor, err := s.messageRepo.GetMessageByID(ctx, cursorID)
		if err != nil {
			return nil, false, err
		}
		if cursor != nil {
			t := cursor.CreatedAt
			beforeTime = &t
		}
	}

	messages, err := s.messageRepo.ListMessages(ctx, channelID, beforeTime, chatMessageListLimit)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(messages) > chatMessageListLimit
	if hasMore {
		messages = messages[:chatMessageListLimit]
	}
	return messages, hasMore, nil
}

// SendMessage appends a message, fans the realtime event out to the channel
// audience and creates inbox notifications for the other members.
func (s *ChatService) SendMessage(ctx context.Context, workspaceID, userID, channelID uuid.UUID, req dto.CreateChatMessageRequest) (*domain.ChatMessage, error) {
	channel, visible, err := s.visibleChannel(ctx, workspaceID, userID, channelID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrChatChannelForbidden
	}

	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, ErrChatEmptyMessage
	}

	message := &domain.ChatMessage{
		ID:        uuid.New(),
		ChannelID: channelID,
		UserID:    userID,
		Body:      body,
	}
	if err := s.messageRepo.CreateMessage(ctx, message); err != nil {
		return nil, err
	}

	recipients, workspaceWide, err := s.audience(ctx, channel)
	if err != nil {
		return nil, err
	}

	s.broadcastChatEvent(ctx, workspaceID, userID, "chat.message.created", channel, message, recipients, workspaceWide)
	s.notifyRecipients(ctx, workspaceID, userID, recipients, channel, message)
	return message, nil
}

// EditMessage rewrites the body; author-only in this slice.
func (s *ChatService) EditMessage(ctx context.Context, workspaceID, userID, messageID uuid.UUID, req dto.UpdateChatMessageRequest) (*domain.ChatMessage, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, ErrChatEmptyMessage
	}

	_, channel, err := s.ownMessage(ctx, workspaceID, userID, messageID)
	if err != nil {
		return nil, err
	}

	updated, err := s.messageRepo.UpdateMessageBody(ctx, messageID, body)
	if err != nil {
		return nil, err
	}
	recipients, workspaceWide, err := s.audience(ctx, channel)
	if err != nil {
		return nil, err
	}
	s.broadcastChatEvent(ctx, workspaceID, userID, "chat.message.updated", channel, updated, recipients, workspaceWide)
	return updated, nil
}

// DeleteMessage tombstones the message; author-only in this slice.
func (s *ChatService) DeleteMessage(ctx context.Context, workspaceID, userID, messageID uuid.UUID) error {
	message, channel, err := s.ownMessage(ctx, workspaceID, userID, messageID)
	if err != nil {
		return err
	}
	if err := s.messageRepo.SoftDeleteMessage(ctx, messageID); err != nil {
		return err
	}
	recipients, workspaceWide, err := s.audience(ctx, channel)
	if err != nil {
		return err
	}
	s.broadcastChatEvent(ctx, workspaceID, userID, "chat.message.deleted", channel, message, recipients, workspaceWide)
	return nil
}

// TypingAudience resolves who should receive a typing indicator for a
// channel. It is wired into the hub, which relays chat.typing events without
// this service touching the socket layer. visible=false refuses the relay.
func (s *ChatService) TypingAudience(ctx context.Context, workspaceID, userID, channelID uuid.UUID) (recipients []uuid.UUID, workspaceWide, visible bool) {
	channel, visible, err := s.visibleChannel(ctx, workspaceID, userID, channelID)
	if err != nil || !visible {
		return nil, false, false
	}
	recipients, workspaceWide, err = s.audience(ctx, channel)
	if err != nil {
		return nil, false, false
	}
	return recipients, workspaceWide, true
}

// ownMessage loads a live message and verifies the actor can see its channel
// and wrote the message.
func (s *ChatService) ownMessage(ctx context.Context, workspaceID, userID, messageID uuid.UUID) (*domain.ChatMessage, *domain.ChatChannel, error) {
	message, err := s.messageRepo.GetMessageByID(ctx, messageID)
	if err != nil {
		return nil, nil, err
	}
	if message == nil {
		return nil, nil, ErrChatMessageDeleted
	}
	channel, visible, err := s.visibleChannel(ctx, workspaceID, userID, message.ChannelID)
	if err != nil {
		return nil, nil, err
	}
	if !visible {
		return nil, nil, ErrChatChannelForbidden
	}
	if message.DeletedAt != nil {
		return nil, nil, ErrChatMessageDeleted
	}
	if message.UserID != userID {
		return nil, nil, ErrChatMessageForbidden
	}
	return message, channel, nil
}

// visibleChannel loads the channel and reports whether the user may read it:
// public channels are open to workspace members, everything else gates on
// membership.
func (s *ChatService) visibleChannel(ctx context.Context, workspaceID, userID, channelID uuid.UUID) (*domain.ChatChannel, bool, error) {
	channel, err := s.channelRepo.GetChannelByID(ctx, channelID)
	if err != nil {
		return nil, false, err
	}
	if channel == nil || channel.WorkspaceID != workspaceID {
		return nil, false, nil
	}
	if channel.Type == domain.ChatChannelPublic {
		return channel, true, nil
	}
	member, err := s.channelRepo.IsChannelMember(ctx, channelID, userID)
	if err != nil {
		return nil, false, err
	}
	return channel, member, nil
}

// audience returns who a channel event should reach. Public channels
// broadcast to the whole workspace; private channels and DMs target their
// members individually.
func (s *ChatService) audience(ctx context.Context, channel *domain.ChatChannel) ([]uuid.UUID, bool, error) {
	if channel.Type == domain.ChatChannelPublic {
		return nil, true, nil
	}
	members, err := s.channelRepo.ChannelMemberIDs(ctx, channel.ID)
	if err != nil {
		return nil, false, err
	}
	return members, false, nil
}

// broadcastChatEvent delivers a chat event to the channel audience: a
// workspace-wide broadcast for public channels, direct-to-user events for
// private channels and DMs. The actor's own sockets are skipped — they
// applied the change locally.
func (s *ChatService) broadcastChatEvent(ctx context.Context, workspaceID, actorID uuid.UUID, eventType string, channel *domain.ChatChannel, message *domain.ChatMessage, recipients []uuid.UUID, workspaceWide bool) {
	event := realtime.Event{
		Type: eventType,
		Payload: map[string]any{
			"channel_id": channel.ID.String(),
			"message_id": message.ID.String(),
			"message":    message,
		},
	}
	if workspaceWide {
		s.hub.Broadcast(workspaceID, event)
		return
	}
	for _, recipient := range recipients {
		if recipient == actorID {
			continue
		}
		s.hub.BroadcastToUser(workspaceID, recipient, event)
	}
}

// notifyRecipients creates inbox notifications (unread badge) for everyone
// else in the channel and pings their live sockets so the badge updates in
// place.
func (s *ChatService) notifyRecipients(ctx context.Context, workspaceID, actorID uuid.UUID, recipients []uuid.UUID, channel *domain.ChatChannel, message *domain.ChatMessage) {
	authorName := ""
	if message.Author != nil {
		authorName = message.Author.DisplayName
		if authorName == "" {
			authorName = message.Author.Name
		}
	}
	var title string
	if channel.Type == domain.ChatChannelDM {
		title = fmt.Sprintf("New message from %s", authorName)
	} else {
		title = fmt.Sprintf("%s in #%s: %s", authorName, channel.Name, chatPreview(message.Body, 80))
	}

	for _, recipient := range recipients {
		if recipient == actorID {
			continue
		}
		if err := s.notifSvc.Create(ctx, recipient, workspaceID, nil, "chat_message", title); err != nil {
			log.WithError(err).Warn("failed to create chat notification")
			continue
		}
		s.hub.BroadcastToUser(workspaceID, recipient, realtime.Event{
			Type:    "notification.created",
			Payload: map[string]string{"type": "chat_message"},
		})
	}
}

// chatPreview collapses whitespace so multi-line messages preview on one
// line, capping the length for notification titles.
func chatPreview(body string, max int) string {
	preview := strings.Join(strings.Fields(body), " ")
	if len(preview) > max {
		return preview[:max-1] + "…"
	}
	return preview
}
