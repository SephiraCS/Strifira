package dto

import "time"

type CreateAPIKeyRequest struct {
	Name          string `json:"name" validate:"required,min=1,max=100"`
	ReadOnly      bool   `json:"read_only"`
	ExpiresInDays *int   `json:"expires_in_days" validate:"omitempty,min=1,max=3650"`
}

type APIKeyResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// CreateAPIKeyResponse returns the raw token exactly once, at creation.
type CreateAPIKeyResponse struct {
	APIKeyResponse
	Token string `json:"token"`
}
