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
	ID          uuid.UUID  `json:"id" db:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	StartedBy   uuid.UUID  `json:"started_by" db:"started_by"`
	Status      string     `json:"status" db:"status"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	EndedAt     *time.Time `json:"ended_at" db:"ended_at"`
}
