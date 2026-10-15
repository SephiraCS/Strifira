package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks ---

type mockAPIKeyRepo struct {
	mock.Mock
}

func (m *mockAPIKeyRepo) Create(ctx context.Context, key *domain.APIKey) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *mockAPIKeyRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.APIKey, error) {
	args := m.Called(ctx, userID)
	if keys, ok := args.Get(0).([]domain.APIKey); ok {
		return keys, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *mockAPIKeyRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.APIKey), args.Error(1)
}

func (m *mockAPIKeyRepo) GetByHash(ctx context.Context, tokenHash string) (*domain.APIKey, error) {
	args := m.Called(ctx, tokenHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.APIKey), args.Error(1)
}

func (m *mockAPIKeyRepo) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	args := m.Called(ctx, id, userID)
	return args.Error(0)
}

func (m *mockAPIKeyRepo) TouchLastUsed(ctx context.Context, id uuid.UUID) error {
	m.Called(ctx, id)
	return nil
}

// authenticateAPIKey wires a service over mocks with the permissive
// expectations every successful path needs (TouchLastUsed runs async).
func authenticateAPIKey(t *testing.T, store *mockAPIKeyRepo, users *mockUserRepo) *APIKeyService {
	t.Helper()
	store.On("TouchLastUsed", mock.Anything, mock.Anything).Return().Maybe()
	return NewAPIKeyService(store, users)
}

// --- Tests ---

func newAPIKeyTestUser(id uuid.UUID) *domain.User {
	return &domain.User{ID: id, Email: "owner@example.test", Name: "Owner", DisplayName: "Owner"}
}

func TestGenerateAPIKeyFormat(t *testing.T) {
	raw, err := GenerateAPIKey()
	require.NoError(t, err)
	// strifira_ prefix + 48 hex chars
	assert.Len(t, raw, 3+48)
	assert.Regexp(t, `^strifira_[0-9a-f]{48}$`, raw)

	second, err := GenerateAPIKey()
	require.NoError(t, err)
	assert.NotEqual(t, raw, second, "tokens must not repeat")
}

func TestAPIKeyServiceCreate(t *testing.T) {
	userID := uuid.New()
	store := &mockAPIKeyRepo{}
	store.On("Create", mock.Anything, mock.AnythingOfType("*domain.APIKey")).Return(nil)
	users := &mockUserRepo{}
	svc := authenticateAPIKey(t, store, users)

	days := 30
	key, raw, err := svc.Create(context.Background(), userID, dto.CreateAPIKeyRequest{Name: "  CI bot  ", ExpiresInDays: &days})
	require.NoError(t, err)

	assert.Equal(t, "CI bot", key.Name, "name is trimmed")
	assert.Equal(t, raw[:10], key.KeyPrefix)
	assert.Equal(t, hashAPIKey(raw), key.TokenHash, "hash, not the raw token, is stored")
	assert.NotContains(t, key.TokenHash, raw)
	assert.ElementsMatch(t, []string{"read", "write"}, key.Scopes)
	require.NotNil(t, key.ExpiresAt)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 30), *key.ExpiresAt, time.Minute)

	roKey, roRaw, err := svc.Create(context.Background(), userID, dto.CreateAPIKeyRequest{Name: "reporter", ReadOnly: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"read"}, roKey.Scopes)
	assert.NotEqual(t, raw, roRaw)
}

func TestAPIKeyServiceAuthenticate(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	user := newAPIKeyTestUser(userID)

	store := &mockAPIKeyRepo{}
	store.On("Create", mock.Anything, mock.AnythingOfType("*domain.APIKey")).Return(nil)
	users := &mockUserRepo{}
	users.On("GetByID", mock.Anything, userID).Return(user, nil).Maybe()
	svc := authenticateAPIKey(t, store, users)

	key, raw, err := svc.Create(ctx, userID, dto.CreateAPIKeyRequest{Name: "mcp"})
	require.NoError(t, err)
	store.On("GetByHash", ctx, hashAPIKey(raw)).Return(key, nil)

	authUser, authKey, err := svc.Authenticate(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, userID, authUser.ID)
	assert.Equal(t, key.ID, authKey.ID)
	assert.ElementsMatch(t, []string{"read", "write"}, authKey.Scopes)

	// Wrong shape: not a strifira_ token
	_, _, err = svc.Authenticate(ctx, "not_a_strifira_token")
	assert.ErrorIs(t, err, ErrAPIKeyAuthentication)

	// Unknown token: hash lookup misses
	unknown := "strifira_" + strings.Repeat("ab", 24)
	store.On("GetByHash", ctx, hashAPIKey(unknown)).Return(nil, nil)
	_, _, err = svc.Authenticate(ctx, unknown)
	assert.ErrorIs(t, err, ErrAPIKeyAuthentication)
}

func TestAPIKeyServiceAuthenticateRevokedAndExpired(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	past := time.Now().Add(-time.Hour)

	revoked := &domain.APIKey{ID: uuid.New(), UserID: userID, TokenHash: "h1", Scopes: []string{"read"}}
	revoked.RevokedAt = &past

	expired := &domain.APIKey{ID: uuid.New(), UserID: userID, TokenHash: "h2", Scopes: []string{"read"}}
	expired.ExpiresAt = &past

	store := &mockAPIKeyRepo{}
	users := &mockUserRepo{}
	users.On("GetByID", mock.Anything, userID).Return(newAPIKeyTestUser(userID), nil).Maybe()
	svc := authenticateAPIKey(t, store, users)

	store.On("GetByHash", ctx, hashAPIKey("strifira_revoked")).Return(revoked, nil)
	_, _, err := svc.Authenticate(ctx, "strifira_revoked")
	assert.ErrorIs(t, err, ErrAPIKeyAuthentication, "revoked keys must not authenticate")

	store.On("GetByHash", ctx, hashAPIKey("strifira_expired")).Return(expired, nil)
	_, _, err = svc.Authenticate(ctx, "strifira_expired")
	assert.ErrorIs(t, err, ErrAPIKeyAuthentication, "expired keys must not authenticate")
}

func TestAPIKeyServiceAuthenticateMissingUser(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	key := &domain.APIKey{ID: uuid.New(), UserID: userID, TokenHash: hashAPIKey("strifira_orphan"), Scopes: []string{"read"}}

	store := &mockAPIKeyRepo{}
	store.On("GetByHash", ctx, hashAPIKey("strifira_orphan")).Return(key, nil)
	users := &mockUserRepo{}
	users.On("GetByID", ctx, userID).Return(nil, nil)

	svc := authenticateAPIKey(t, store, users)
	_, _, err := svc.Authenticate(ctx, "strifira_orphan")
	assert.ErrorIs(t, err, ErrAPIKeyAuthentication, "keys whose owner was deleted must not authenticate")
}
