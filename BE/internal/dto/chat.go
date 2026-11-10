package dto

import "time"

// ChatUser is the author/peer subset the chat UI renders.
type ChatUser struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Email       string  `json:"email"`
	AvatarURL   *string `json:"avatar_url"`
}

type CreateChatChannelRequest struct {
	Name string `json:"name" validate:"required,slug"`
	Type string `json:"type" validate:"omitempty,oneof=public private"`
}

type CreateChatDMRequest struct {
	UserID string `json:"user_id" validate:"required,uuid4"`
}

type CreateChatMessageRequest struct {
	Body string `json:"body" validate:"required,min=1,max=8000"`
}

type UpdateChatMessageRequest struct {
	Body string `json:"body" validate:"required,min=1,max=8000"`
}

type ChatChannelResponse struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Topic       string     `json:"topic"`
	DMPeer      *ChatUser  `json:"dm_peer,omitempty"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastPostAt  *time.Time `json:"last_post_at,omitempty"`
}

type ChatMessageResponse struct {
	ID        string     `json:"id"`
	ChannelID string     `json:"channel_id"`
	UserID    string     `json:"user_id"`
	Author    *ChatUser  `json:"author,omitempty"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	EditedAt  *time.Time `json:"edited_at,omitempty"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type ChatMessageListResponse struct {
	Messages []ChatMessageResponse `json:"messages"`
	// HasMore reports older messages beyond the returned page.
	HasMore bool `json:"has_more"`
}

type ChatChannelListResponse struct {
	Channels []ChatChannelResponse `json:"channels"`
}

// VoiceRoomResponse is one channel's live voice room. Participants are
// per-connection (a user with two tabs appears once per joined tab); the
// hub retires a user's earlier connections on rejoin.
type VoiceRoomResponse struct {
	ChannelID    string                     `json:"channel_id"`
	ChannelName  string                     `json:"channel_name"`
	ChannelType  string                     `json:"channel_type"`
	Participants []VoiceParticipantResponse `json:"participants"`
}

type VoiceParticipantResponse struct {
	PeerID   string    `json:"peer_id"`
	UserID   string    `json:"user_id"`
	User     *ChatUser `json:"user,omitempty"`
	Muted    bool      `json:"muted"`
	Deafened bool      `json:"deafened"`
}

type VoiceRoomListResponse struct {
	Rooms []VoiceRoomResponse `json:"rooms"`
}
