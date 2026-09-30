package migrations_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestIssueIdentifierHistoryMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_identifier_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
	})
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path, parsed.RawPath = "/"+name, ""
	migrator, err := migrate.New("file://.", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Migrate(51))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID, teamID, issueID, statusID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,'identifier-migration@example.test','History','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'History','identifier-migration',$2)`, workspaceID, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO teams(id,workspace_id,name,key) VALUES($1,$2,'History','TS')`, teamID, workspaceID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO team_statuses(id,team_id,name,slug,category) VALUES($1,$2,'Todo','todo','unstarted')`, statusID, teamID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES($1,$2,$3,1,'TS-1','Existing issue',$4,$5)`, issueID, workspaceID, teamID, userID, statusID)
	require.NoError(t, err)
	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(52), version)
	var resolved uuid.UUID
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT issue_id FROM issue_identifier_history WHERE workspace_id=$1 AND identifier_text='TS-1'`, workspaceID).Scan(&resolved))
	require.Equal(t, issueID, resolved)
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_identifier_history WHERE workspace_id=$1 AND identifier_text='DSH-1'`, workspaceID).Scan(&count))
	require.Zero(t, count, "migration must not guess pre-upgrade team keys")
	_, err = db.ExecContext(t.Context(), `UPDATE issues SET identifier_text='KU-1' WHERE id=$1`, issueID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_identifier_history WHERE issue_id=$1`, issueID).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, migrator.Steps(-1))
	var tableExists bool
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT to_regclass('public.issue_identifier_history') IS NOT NULL`).Scan(&tableExists))
	require.False(t, tableExists)
	var triggers int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pg_trigger WHERE tgname='issue_record_identifier'`).Scan(&triggers))
	require.Zero(t, triggers)
	var identifier string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT identifier_text FROM issues WHERE id=$1`, issueID).Scan(&identifier))
	require.Equal(t, "KU-1", identifier, "rollback preserves issue rows and current identifiers")
	_, err = db.ExecContext(t.Context(), `UPDATE issues SET identifier_text='TS-1' WHERE id=$1`, issueID)
	require.NoError(t, err, "rollback removes the trigger function dependency")
	require.NoError(t, migrator.Steps(1))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM issue_identifier_history WHERE issue_id=$1`, issueID).Scan(&count))
	require.Equal(t, 1, count, "reapplication seeds only identities currently known")
}
