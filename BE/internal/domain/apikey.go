package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

const (
	APIKeyScopeRead  = "read"
	APIKeyScopeWrite = "write"
)

type APIKey struct {
	ID         uuid.UUID      `json:"id" db:"id"`
	UserID     uuid.UUID      `json:"user_id" db:"user_id"`
	Name       string         `json:"name" db:"name"`
	TokenHash  string         `json:"-" db:"token_hash"`
	KeyPrefix  string         `json:"key_prefix" db:"key_prefix"`
	Scopes     pq.StringArray `json:"scopes" db:"scopes"`
	CreatedAt  time.Time      `json:"created_at" db:"created_at"`
	LastUsedAt *time.Time     `json:"last_used_at" db:"last_used_at"`
	ExpiresAt  *time.Time     `json:"expires_at" db:"expires_at"`
	RevokedAt  *time.Time     `json:"revoked_at" db:"revoked_at"`
}

func (k *APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}
