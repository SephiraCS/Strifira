package dto

import (
	"time"
)

// CreateScreenShareRequest carries no fields: a share is identified by
// its presenter (product direction 2026-09-17).
type CreateScreenShareRequest struct{}

type EndScreenShareRequest struct {
	PresenterToken string `json:"presenter_token"`
	Force          bool   `json:"force"`
}

// ScreenShareUser is the presenter subset needed to render share tiles.
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
	// StartedByUser names the presenter — the share's identity.
	StartedByUser *ScreenShareUser `json:"started_by_user,omitempty"`
	Status        string           `json:"status"`
	ViewerCount   int              `json:"viewer_count"`
	CreatedAt     time.Time        `json:"created_at"`
	EndedAt       *time.Time       `json:"ended_at"`
}

type ScreenShareListResponse struct {
	Active []ScreenShareResponse `json:"active"`
	Recent []ScreenShareResponse `json:"recent"`
}
