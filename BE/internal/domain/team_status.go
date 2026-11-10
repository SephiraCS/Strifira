package domain

import (
	"time"

	"github.com/google/uuid"
)

type StatusCategory string

const (
	StatusCategoryBacklog   StatusCategory = "backlog"
	StatusCategoryUnstarted StatusCategory = "unstarted"
	StatusCategoryStarted   StatusCategory = "started"
	StatusCategoryCompleted StatusCategory = "completed"
	StatusCategoryCancelled StatusCategory = "cancelled"
)

type TeamStatus struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	TeamID     uuid.UUID      `json:"team_id" db:"team_id"`
	Name       string         `json:"name" db:"name"`
	Slug       string         `json:"slug" db:"slug"`
	Category   StatusCategory `json:"category" db:"category"`
	Color      *string        `json:"color" db:"color"`
	Icon       *string        `json:"icon" db:"icon"`
	Position   int            `json:"position" db:"position"`
	WipLimit   *int           `json:"wip_limit" db:"wip_limit"`
	IsDefault  bool           `json:"is_default" db:"is_default"`
	ArchivedAt *time.Time     `json:"archived_at" db:"archived_at"`
	CreatedAt  time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at" db:"updated_at"`
}
