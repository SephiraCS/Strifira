package dto

import (
	"time"
)

// CreateScreenShareRequest carries no fields: a share is identified by
// its presenter (product direction 2026-09-17).
type CreateScreenShareRequest struct{}

type ScreenShareTokenResponse struct {
	// LiveKit access token for the requesting user's role in this share.
	Token string `json:"token"`
	// LiveKit server URL the client should connect to (wss:// or ws://).
	ServerURL string `json:"server_url"`
}

// ScreenShareUser is the presenter subset needed to render share tiles.
type ScreenShareUser struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

type ScreenShareResponse struct {
	ID          string           `json:"id"`
	WorkspaceID string           `json:"workspace_id"`
	StartedBy   string           `json:"started_by"`
	// StartedByUser names the presenter — the share's identity.
	StartedByUser *ScreenShareUser `json:"started_by_user,omitempty"`
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
