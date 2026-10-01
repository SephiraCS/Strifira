package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	ScreenShareStatusActive = "active"
	ScreenShareStatusEnded  = "ended"
)

type ScreenShare struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	StartedBy   uuid.UUID `json:"started_by" db:"started_by"`
	// PresenterToken belongs to the capturing tab and is never exposed in lists.
	PresenterToken uuid.UUID  `json:"-" db:"presenter_token"`
	Status         string     `json:"status" db:"status"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
	EndedAt        *time.Time `json:"ended_at" db:"ended_at"`
	// LastPresenterSeenAt is the last presenter contact (share creation or
	// a presence announce). The no-source GC measures its grace from this
	// moment, not from CreatedAt, so a long-lived share survives brief
	// presenter signaling drops and backend restarts.
	LastPresenterSeenAt time.Time `json:"last_presenter_seen_at" db:"last_presenter_seen_at"`
}
