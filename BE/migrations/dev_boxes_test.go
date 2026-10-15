package migrations_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestDevBoxesMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.PingContext(t.Context()))
	name := "strifira_devbox_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`) })
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + name
	parsed.RawPath = ""
	migrator, err := migrate.New("file://.", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Migrate(49))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID := uuid.New(), uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,'devbox-migration@example.test','Migration','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Migration','devbox-migration',$2)`, workspaceID, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, workspaceID, userID)
	require.NoError(t, err)
	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(50), version)
	boxID := uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO dev_boxes(id,workspace_id,created_by_user_id,name,token_hash,token_prefix) VALUES($1,$2,$3,'strifira',$4,'strifira_test')`, boxID, workspaceID, userID, strings.Repeat("a", 64))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO dev_boxes(workspace_id,created_by_user_id,name,token_hash,token_prefix) VALUES($1,$2,'STRIFIRA',$3,'strifira_other')`, workspaceID, userID, strings.Repeat("b", 64))
	require.Error(t, err, "case-folded names are unique for the creator")
	_, err = db.ExecContext(t.Context(), `INSERT INTO dev_boxes(workspace_id,created_by_user_id,name,token_hash,token_prefix) VALUES($1,$2,'other',$3,'strifira_other')`, workspaceID, uuid.New(), strings.Repeat("c", 64))
	require.Error(t, err, "a creator must belong to the workspace")
	var foreignKeys int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_constraint WHERE conrelid='dev_box_jobs'::regclass AND contype='f'`).Scan(&foreignKeys))
	require.Equal(t, 3, foreignKeys)
	require.NoError(t, migrator.Steps(-1))
	var tables int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('dev_boxes','dev_box_jobs','dev_box_job_logs')`).Scan(&tables))
	require.Zero(t, tables)
	var users int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM users WHERE id=$1`, userID).Scan(&users))
	require.Equal(t, 1, users, "rollback preserves existing users")
	require.NoError(t, migrator.Steps(1))
}

func TestDevBoxesMigrationDefinesBoundedLogs(t *testing.T) {
	up, err := os.ReadFile("000050_dev_boxes.up.sql")
	require.NoError(t, err)
	require.Contains(t, string(up), "logs_bytes BETWEEN 0 AND 4194304")
	require.Contains(t, string(up), "octet_length(content) <= 65536")
	require.Contains(t, string(up), "UNIQUE (job_id, sequence)")
}
