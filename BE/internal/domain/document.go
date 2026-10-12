package domain

import (
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	WorkspaceID uuid.UUID  `json:"workspace_id" db:"workspace_id"`
	ParentID    *uuid.UUID `json:"parent_id" db:"parent_id"`
	Title       string     `json:"title" db:"title"`
	Icon        *string    `json:"icon" db:"icon"`
	ContentHTML string     `json:"content_html" db:"content_html"`
	ContentText string     `json:"content_text" db:"content_text"`
	Position    float64    `json:"position" db:"position"`
	IsTemplate  bool       `json:"is_template" db:"is_template"`
	CreatedBy   uuid.UUID  `json:"created_by" db:"created_by"`
	UpdatedBy   *uuid.UUID `json:"updated_by" db:"updated_by"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at" db:"deleted_at"`
	DeletedBy   *uuid.UUID `json:"deleted_by" db:"deleted_by"`
}

type DocumentRevision struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	DocumentID  uuid.UUID  `json:"document_id" db:"document_id"`
	Title       string     `json:"title" db:"title"`
	Icon        *string    `json:"icon" db:"icon"`
	ContentHTML string     `json:"content_html" db:"content_html"`
	ContentText string     `json:"content_text" db:"content_text"`
	EditedBy    *uuid.UUID `json:"edited_by" db:"edited_by"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

type DocumentTag struct {
	ID          uuid.UUID `json:"id" db:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id" db:"workspace_id"`
	Name        string    `json:"name" db:"name"`
	Color       *string   `json:"color" db:"color"`
	CreatedBy   uuid.UUID `json:"created_by" db:"created_by"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

type DocumentLink struct {
	SourceID  uuid.UUID `json:"source_id" db:"source_id"`
	TargetID  uuid.UUID `json:"target_id" db:"target_id"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type DocumentComment struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	DocumentID uuid.UUID  `json:"document_id" db:"document_id"`
	UserID     uuid.UUID  `json:"user_id" db:"user_id"`
	Body       string     `json:"body" db:"body"`
	ParentID   *uuid.UUID `json:"parent_id" db:"parent_id"`
	ResolvedAt *time.Time `json:"resolved_at" db:"resolved_at"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
}
