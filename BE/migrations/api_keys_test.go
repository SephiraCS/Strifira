package migrations_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestAPIKeysMigrationRoundTrip(t *testing.T) {
	up, err := os.ReadFile("000034_api_keys.up.sql")
	require.NoError(t, err)
	down, err := os.ReadFile("000034_api_keys.down.sql")
	require.NoError(t, err)
	require.Contains(t, string(up), "CHAR(64) NOT NULL UNIQUE")
	require.Contains(t, string(down), "DROP TABLE IF EXISTS api_keys")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	require.NoError(t, admin.PingContext(ctx))

	databaseName := "ku_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	if err != nil && strings.Contains(err.Error(), "42501") {
		_ = admin.Close()
		t.Skip("DATABASE_URL user cannot create an isolated migration database")
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
		_ = admin.Close()
	})

	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	testURL := parsed.String()

	migrator, err := migrate.New("file://.", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Migrate(33))

	db, err := sql.Open("pgx", testURL)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx))
	t.Cleanup(func() { _ = db.Close() })

	userID := uuid.New()
	_, err = db.Exec(`INSERT INTO users (id,email,name,password_hash) VALUES ($1,'keys@example.test','Keys','test')`, userID)
	require.NoError(t, err)

	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(34), version)

	// Insert one key per scope shape and prove the CHECK constraint holds.
	_, err = db.Exec(`INSERT INTO api_keys (id,user_id,name,token_hash,key_prefix,scopes) VALUES ($1,$2,'full','a',$3,$4)`,
		uuid.New(), userID, "ku_aaaaaaaa", pq.Array([]string{"read", "write"}))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO api_keys (id,user_id,name,token_hash,key_prefix,scopes,expires_at) VALUES ($1,$2,'ro','b',$3,$4,NOW() + INTERVAL '30 days')`,
		uuid.New(), userID, "ku_bbbbbbbb", pq.Array([]string{"read"}))
	require.NoError(t, err)

	// A key without the read scope violates the CHECK (read is mandatory).
	_, err = db.Exec(`INSERT INTO api_keys (id,user_id,name,token_hash,key_prefix,scopes) VALUES ($1,$2,'bad','c',$3,$4)`,
		uuid.New(), userID, "ku_cccccccc", pq.Array([]string{"write"}))
	require.Error(t, err, "write-only scopes must be rejected")
	// Unknown scopes are rejected too.
	_, err = db.Exec(`INSERT INTO api_keys (id,user_id,name,token_hash,key_prefix,scopes) VALUES ($1,$2,'bad','d',$3,$4)`,
		uuid.New(), userID, "ku_dddddddd", pq.Array([]string{"read", "admin"}))
	require.Error(t, err)

	// Duplicate hashes are rejected.
	_, err = db.Exec(`INSERT INTO api_keys (id,user_id,name,token_hash,key_prefix,scopes) VALUES ($1,$2,'dupe','a',$3,$4)`,
		uuid.New(), userID, "ku_aaaaaaaa", pq.Array([]string{"read"}))
	require.Error(t, err)

	// Revocation and last_used updates work.
	_, err = db.Exec(`UPDATE api_keys SET revoked_at = NOW(), last_used_at = NOW() WHERE token_hash = 'a'`)
	require.NoError(t, err)

	require.NoError(t, migrator.Steps(-1))
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM api_keys`).Scan(&count)
	require.Error(t, err, "the table must be gone after rollback")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = $1`, userID).Scan(&count))
	require.Equal(t, 1, count, "users survive the api_keys rollback")
}
