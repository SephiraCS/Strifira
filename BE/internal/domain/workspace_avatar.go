package domain

import (
	"time"

	"github.com/google/uuid"
)

type WorkspaceAvatar struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	StorageKey  string    `json:"storage_key" db:"storage_key"`
	Filename    string    `json:"filename" db:"filename"`
	ContentType string    `json:"content_type" db:"content_type"`
	Size        int64     `json:"size" db:"size"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}
