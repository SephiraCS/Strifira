package config

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/aiegress"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSysadmins(t *testing.T) {
	id := uuid.New()
	ids, err := parseSysadmins("  " + id.String() + " , " + id.String() + ",")

	require.NoError(t, err)
	assert.Contains(t, ids, id)
	assert.Len(t, ids, 1)
}

func TestLoadBindAddress(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/fixture")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", strings.Repeat("fixture", 8))
	t.Setenv("DEV_MACHINES_ENABLED", "false")
	t.Setenv("SYSADMINS", "")
	t.Setenv("SYSTEM_UPDATER_TOKEN", "")
	t.Setenv("AI_PROVIDER_TRUSTED_ORIGINS", "")
	t.Setenv("BIND_ADDRESS", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Empty(t, cfg.BindAddress)

	t.Setenv("BIND_ADDRESS", "127.0.0.1")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", cfg.BindAddress)
}

func TestLoadAIProviderNetworkPolicy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/fixture")
	t.Setenv("REDIS_URL", "redis://localhost:6379")
	t.Setenv("JWT_SECRET", strings.Repeat("fixture", 8))
	t.Setenv("DEV_MACHINES_ENABLED", "false")
	t.Setenv("SYSADMINS", "")
	t.Setenv("SYSTEM_UPDATER_TOKEN", "")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "approved private provider")
	}))
	t.Cleanup(provider.Close)

	t.Setenv("AI_PROVIDER_TRUSTED_ORIGINS", "")
	cfg, err := Load()
	require.NoError(t, err)
	client := cfg.AIProviderNetworkPolicy.NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, provider.URL+"/chat/completions", nil)
	require.NoError(t, err)
	_, err = client.Do(request)
	require.ErrorIs(t, err, aiegress.ErrDestinationBlocked)

	t.Setenv("AI_PROVIDER_TRUSTED_ORIGINS", provider.URL)
	cfg, err = Load()
	require.NoError(t, err)
	client = cfg.AIProviderNetworkPolicy.NewHTTPClient(time.Second)
	t.Cleanup(client.CloseIdleConnections)
	response, err := client.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)

	for _, value := range []string{"http://*.example", provider.URL + "/v1", "http://user:secret@localhost", provider.URL + ","} {
		t.Setenv("AI_PROVIDER_TRUSTED_ORIGINS", value)
		_, err := Load()
		require.ErrorContains(t, err, "AI_PROVIDER_TRUSTED_ORIGINS")
		assert.NotContains(t, err.Error(), "secret")
	}
}

func TestParseSysadminsRejectsInvalidID(t *testing.T) {
	_, err := parseSysadmins("not-a-uuid")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "SYSADMINS")
}

func TestIsSysAdmin(t *testing.T) {
	id := uuid.New()
	cfg := &Config{sysadminIDs: map[uuid.UUID]struct{}{id: {}}}

	assert.True(t, cfg.IsSysAdmin(id))
	assert.False(t, cfg.IsSysAdmin(uuid.New()))
	assert.False(t, cfg.IsSysAdmin(uuid.Nil))
}

func TestDevMachineConfigRequiresSeparateRegistrableDomain(t *testing.T) {
	config := DevMachineConfig{
		Enabled: true, Domain: "machines.example.com", EncryptionKey: strings.Repeat("x", 32),
		TicketTTLSeconds: 60, SessionTTLMinutes: 60, IngestURL: "https://app.example.com/api/dev-machine-ingest",
	}

	err := config.Validate("https://app.example.com")

	require.ErrorContains(t, err, "separate registrable domain")
	config.Domain = "machines.example.net"
	require.NoError(t, config.Validate("https://app.example.com"))
}

func TestDevMachineConfigAllowsLocalhostDevelopment(t *testing.T) {
	config := DevMachineConfig{
		Enabled: true, Domain: "machines.localhost", EncryptionKey: strings.Repeat("x", 32),
		TicketTTLSeconds: 60, SessionTTLMinutes: 60, IngestURL: "https://localhost/api/dev-machine-ingest",
	}

	require.NoError(t, config.Validate("https://localhost"))
}

func TestGatewayValidationDoesNotRequireControlPlaneSecrets(t *testing.T) {
	config := DevMachineConfig{Enabled: true, Domain: "machines.example.net", TicketTTLSeconds: 60, SessionTTLMinutes: 60}

	require.NoError(t, config.ValidateGateway("https://app.example.com"))
}

func TestLoadMachineGatewayDoesNotRequireJWTOrRedis(t *testing.T) {
	setMachineGatewayEnv(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "postgres://gateway-db/strifira?sslmode=disable")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("REDIS_URL", "")

	cfg, err := LoadMachineGateway()

	require.NoError(t, err)
	require.Equal(t, "postgres://gateway-db/strifira?sslmode=disable", cfg.DatabaseURL)
	require.Empty(t, cfg.JWTSecret)
	require.Empty(t, cfg.RedisURL)
	require.Equal(t, "https://app.example.com", cfg.FrontendURL)
	require.Equal(t, "machines.example.net", cfg.DevMachine.Domain)
}

func TestLoadMachineGatewayRejectsMissingAndInvalidGatewayValues(t *testing.T) {
	t.Run("missing database", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("DATABASE_URL", "")
		t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "DATABASE_URL or DEV_MACHINE_GATEWAY_DATABASE_URL")
	})

	t.Run("missing frontend", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("FRONTEND_URL", "")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "FRONTEND_URL")
	})

	t.Run("disabled dev machines", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("DEV_MACHINES_ENABLED", "false")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "DEV_MACHINES_ENABLED")
	})

	t.Run("same registrable domain", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("DEV_MACHINE_DOMAIN", "machines.example.com")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "separate registrable domain")
	})

	t.Run("invalid session ttl", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("DEV_MACHINE_SESSION_TTL_MINUTES", "1")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "DEV_MACHINE_SESSION_TTL_MINUTES")
	})

	t.Run("production requires separate gateway credential", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("ENVIRONMENT", "production")
		t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "DEV_MACHINE_GATEWAY_DATABASE_URL is required")
	})

	t.Run("production rejects application database user", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("ENVIRONMENT", "production")
		t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "postgres://postgres:gateway@postgres/strifira?sslmode=disable")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "distinct from DATABASE_URL")
	})

	t.Run("production rejects malformed gateway URL", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("ENVIRONMENT", "production")
		t.Setenv("DATABASE_URL", "")
		t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "postgres://postgres/strifira")

		_, err := LoadMachineGateway()

		require.ErrorContains(t, err, "PostgreSQL URL with a username")
	})

	t.Run("production accepts restricted gateway credential", func(t *testing.T) {
		setMachineGatewayEnv(t)
		t.Setenv("ENVIRONMENT", "production")
		t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "postgres://strifira_gateway:gateway@postgres/strifira?sslmode=disable")

		cfg, err := LoadMachineGateway()

		require.NoError(t, err)
		require.Equal(t, "postgres://strifira_gateway:gateway@postgres/strifira?sslmode=disable", cfg.DatabaseURL)
	})
}

func setMachineGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://postgres@postgres/strifira?sslmode=disable")
	t.Setenv("DEV_MACHINE_GATEWAY_DATABASE_URL", "")
	t.Setenv("FRONTEND_URL", "https://app.example.com")
	t.Setenv("DEV_MACHINES_ENABLED", "true")
	t.Setenv("DEV_MACHINE_DOMAIN", "machines.example.net")
	t.Setenv("DEV_MACHINE_ACCESS_TICKET_TTL_SECONDS", "60")
	t.Setenv("DEV_MACHINE_SESSION_TTL_MINUTES", "480")
}

func TestDemoDevMachineAllowed(t *testing.T) {
	sysAdminID := uuid.New()
	normalUserID := uuid.New()

	t.Run("off when demo mode is disabled", func(t *testing.T) {
		cfg := &Config{PublicDemoMode: false, sysadminIDs: map[uuid.UUID]struct{}{}}
		assert.True(t, cfg.DemoDevMachineAllowed(normalUserID))
		assert.True(t, cfg.DemoDevMachineAllowed(sysAdminID))
	})

	t.Run("nil config always returns true", func(t *testing.T) {
		var cfg *Config
		assert.True(t, cfg.DemoDevMachineAllowed(normalUserID))
	})

	t.Run("sysadmin allowed when demo mode on", func(t *testing.T) {
		cfg := &Config{PublicDemoMode: true, sysadminIDs: map[uuid.UUID]struct{}{sysAdminID: {}}}
		assert.True(t, cfg.DemoDevMachineAllowed(sysAdminID))
		assert.False(t, cfg.DemoDevMachineAllowed(normalUserID))
	})

	t.Run("fail closed when SYSADMINS is empty in demo mode", func(t *testing.T) {
		cfg := &Config{PublicDemoMode: true, sysadminIDs: map[uuid.UUID]struct{}{}}
		assert.False(t, cfg.DemoDevMachineAllowed(sysAdminID))
		assert.False(t, cfg.DemoDevMachineAllowed(normalUserID))
	})
}

func TestLoadMachineGatewayReadsDemoConfig(t *testing.T) {
	setMachineGatewayEnv(t)
	t.Setenv("DATABASE_URL", "postgres://postgres/strifira?sslmode=disable")
	sysAdminID := uuid.New()
	t.Setenv("SYSADMINS", sysAdminID.String())
	t.Setenv("PUBLIC_DEMO_MODE", "true")

	cfg, err := LoadMachineGateway()

	require.NoError(t, err)
	assert.True(t, cfg.PublicDemoMode)
	assert.True(t, cfg.IsSysAdmin(sysAdminID))
	assert.False(t, cfg.IsSysAdmin(uuid.New()))
}
