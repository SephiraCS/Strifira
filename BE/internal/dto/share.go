package dto

import (
	"time"
)

type CreateScreenShareRequest struct {
	Title string `json:"title" validate:"omitempty,max=200"`
}

type ScreenShareTokenResponse struct {
	// LiveKit access token for the requesting user's role in this share.
	Token string `json:"token"`
	// LiveKit server URL the client should connect to (wss:// or ws://).
	ServerURL string `json:"server_url"`
}

// ScreenShareUser is the presenter subset needed to render share cards.
type ScreenShareUser struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

type ScreenShareResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	StartedBy   string `json:"started_by"`
	// StartedByUser is denormalized for list rendering; may be null after
	// user deletion is introduced.
	StartedByUser *ScreenShareUser `json:"started_by_user,omitempty"`
	Title         string           `json:"title"`
	RoomName      string           `json:"room_name"`
	Status        string           `json:"status"`
	ViewerCount   int              `json:"viewer_count"`
	CreatedAt     time.Time        `json:"created_at"`
	EndedAt       *time.Time       `json:"ended_at"`
}

type ScreenShareListResponse struct {
	Active []ScreenShareResponse `json:"active"`
	Recent []ScreenShareResponse `json:"recent"`
}
