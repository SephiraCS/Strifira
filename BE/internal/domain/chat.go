package domain

import (
	"time"

	"github.com/google/uuid"
)

// Chat channel kinds. Public channels are open to every workspace member,
// private channels gate on membership, DMs are two-member direct messages.
const (
	ChatChannelPublic  = "public"
	ChatChannelPrivate = "private"
	ChatChannelDM      = "dm"
)

type ChatChannel struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	Type        string    `json:"type" db:"type"`
	Name        string    `json:"name" db:"name"`
	Topic       string    `json:"topic" db:"topic"`
	// DMKey is the sorted "<userA>:<userB>" pair identifying a direct
	// message; NULL for channels. Unique per workspace so a DM between two
	// users is a single row.
	DMKey *string `json:"-" db:"dm_key"`
	// DMPeer is the other member of a DM (resolved by the repository); nil
	// for channels. It gives the UI the peer identity without a join table
	// round-trip.
	DMPeer *ChatUser `json:"dm_peer" db:"-"`
	// Private reports whether membership gates visibility (private channel
	// or DM). Resolved alongside DMPeer.
	Private    bool       `json:"-" db:"-"`
	CreatedBy  uuid.UUID  `json:"created_by" db:"created_by"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
	LastPostAt *time.Time `json:"last_post_at" db:"last_post_at"`
}

// ChatUser is the author/peer subset the chat UI renders.
type ChatUser struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	DisplayName string    `json:"display_name" db:"display_name"`
	Email       string    `json:"email" db:"email"`
	AvatarURL   *string   `json:"avatar_url" db:"avatar_url"`
}

type ChatMessage struct {
	ID        uuid.UUID `json:"id" db:"id"`
	ChannelID uuid.UUID `json:"channel_id" db:"channel_id"`
	UserID    uuid.UUID `json:"user_id" db:"user_id"`
	// Author is resolved by the repository (join with users).
	Author    *ChatUser  `json:"author" db:"-"`
	Body      string     `json:"body" db:"body"`
	CreatedAt time.Time  `json:"created_at" db:"created_at"`
	EditedAt  *time.Time `json:"edited_at" db:"edited_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}
