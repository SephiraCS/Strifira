package dto

import "time"

type CreateTeamStatusRequest struct {
	Name       string   `json:"name" validate:"required,min=1,max=100"`
	Category   string   `json:"category" validate:"required,oneof=backlog unstarted started completed cancelled"`
	Color      *string  `json:"color"`
	Icon       *string  `json:"icon" validate:"omitempty,max=50"`
	WipLimit   *int     `json:"wip_limit" validate:"omitempty,gt=0"`
	ProjectIDs []string `json:"project_ids" validate:"omitempty,dive,uuid"`
}

type UpdateTeamStatusRequest struct {
	Name     *string `json:"name" validate:"omitempty,min=1,max=100"`
	Color    *string `json:"color"`
	Icon     *string `json:"icon" validate:"omitempty,max=50"`
	Position *int    `json:"position" validate:"omitempty,min=0"`
	// Zero removes the limit; positive values cap the column's cards.
	WipLimit   *int      `json:"wip_limit" validate:"omitempty,gte=0"`
	Archived   *bool     `json:"archived"`
	ProjectIDs *[]string `json:"project_ids" validate:"omitempty,dive,uuid"`
}

type TeamStatusResponse struct {
	ID         string     `json:"id"`
	TeamID     string     `json:"team_id"`
	Name       string     `json:"name"`
	Slug       string     `json:"slug"`
	Category   string     `json:"category"`
	Color      *string    `json:"color"`
	Icon       *string    `json:"icon"`
	Position   int        `json:"position"`
	WipLimit   *int       `json:"wip_limit"`
	IsDefault  bool       `json:"is_default"`
	ArchivedAt *time.Time `json:"archived_at"`
	ProjectIDs []string   `json:"project_ids"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
