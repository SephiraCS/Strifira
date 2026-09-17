package jwt

import (
	"testing"

	"github.com/google/uuid"
)

func TestRefreshTokensUniqueWithinSameSecond(t *testing.T) {
	secret := "test-secret-32-chars-minimum-abcdefgh"
	user := uuid.New()
	a, _, err := GenerateRefreshToken(user, secret)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := GenerateRefreshToken(user, secret)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two refresh tokens for the same user in the same second must differ (jti)")
	}
	// Both must still validate.
	if _, err := ValidateToken(a, secret); err != nil {
		t.Fatalf("token a invalid: %v", err)
	}
	if _, err := ValidateToken(b, secret); err != nil {
		t.Fatalf("token b invalid: %v", err)
	}
}
