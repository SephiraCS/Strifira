package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/lib/pq"
)

var (
	ErrAPIKeyAuthentication = errors.New("invalid API key")
	ErrAPIKeyNotFound       = errors.New("API key not found")
)

const (
	apiKeyPrefix      = "ku_"
	apiKeySecretBytes = 24 // 48 hex chars of entropy after the ku_ prefix
	apiKeyPrefixLen   = 10 // display length of the raw token head
)

// APIKeyService manages personal bearer tokens for MCP access. Raw tokens
// are generated once and returned only from Create; storage keeps the
// SHA-256 hash, mirroring the dev-machine collector token design.
type APIKeyService struct {
	store repository.APIKeyRepo
	users repository.UserRepo
}

func NewAPIKeyService(store repository.APIKeyRepo, users repository.UserRepo) *APIKeyService {
	return &APIKeyService{store: store, users: users}
}

func hashAPIKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIKey produces a new raw token. Split out from Create so tests
// can exercise hashing without touching persistence.
func GenerateAPIKey() (string, error) {
	buf := make([]byte, apiKeySecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return apiKeyPrefix + hex.EncodeToString(buf), nil
}

func (s *APIKeyService) Create(ctx context.Context, userID uuid.UUID, req dto.CreateAPIKeyRequest) (*domain.APIKey, string, error) {
	raw, err := GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}

	key := &domain.APIKey{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      strings.TrimSpace(req.Name),
		TokenHash: hashAPIKey(raw),
		KeyPrefix: raw[:apiKeyPrefixLen],
		Scopes:    pq.StringArray{domain.APIKeyScopeRead},
	}
	if !req.ReadOnly {
		key.Scopes = append(key.Scopes, domain.APIKeyScopeWrite)
	}
	if req.ExpiresInDays != nil && *req.ExpiresInDays > 0 {
		expires := time.Now().AddDate(0, 0, *req.ExpiresInDays)
		key.ExpiresAt = &expires
	}

	if err := s.store.Create(ctx, key); err != nil {
		return nil, "", err
	}
	return key, raw, nil
}

func (s *APIKeyService) List(ctx context.Context, userID uuid.UUID) ([]domain.APIKey, error) {
	return s.store.ListByUser(ctx, userID)
}

func (s *APIKeyService) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	return s.store.Revoke(ctx, id, userID)
}

// Authenticate resolves a raw bearer token to its owner and checks
// revocation and expiry. It updates last_used_at best-effort; the write
// never blocks authentication.
func (s *APIKeyService) Authenticate(ctx context.Context, rawToken string) (*domain.User, *domain.APIKey, error) {
	if !strings.HasPrefix(rawToken, apiKeyPrefix) {
		return nil, nil, ErrAPIKeyAuthentication
	}
	key, err := s.store.GetByHash(ctx, hashAPIKey(rawToken))
	if err != nil {
		return nil, nil, err
	}
	if key == nil {
		return nil, nil, ErrAPIKeyAuthentication
	}
	now := time.Now()
	if key.RevokedAt != nil && !key.RevokedAt.After(now) {
		return nil, nil, ErrAPIKeyAuthentication
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now) {
		return nil, nil, ErrAPIKeyAuthentication
	}
	user, err := s.users.GetByID(ctx, key.UserID)
	if err != nil || user == nil {
		return nil, nil, ErrAPIKeyAuthentication
	}
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.store.TouchLastUsed(bgCtx, key.ID)
	}()
	return user, key, nil
}
