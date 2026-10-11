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

func TestForgejoConnectionOrganizationMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_forgejo_org_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, migrator.Migrate(59))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID := uuid.New(), uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,'forgejo-org-migration@example.test','Migration','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Migration','forgejo-org-migration',$2)`, workspaceID, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_connections(id,workspace_id,instance_url,token,account_login)
		VALUES($1,$2,'https://forgejo.example.test','encrypted-token','owner')`, uuid.New(), workspaceID)
	require.NoError(t, err)

	// 000060 adds the nullable organization column; existing rows stay NULL.
	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(60), version)
	var organization *string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT organization FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&organization))
	require.Nil(t, organization, "existing connections cannot invent an organization restriction")
	_, err = db.ExecContext(t.Context(), `UPDATE forgejo_connections SET organization='acme' WHERE workspace_id=$1`, workspaceID)
	require.NoError(t, err)

	// 000061 adds the disable marker table.
	require.NoError(t, migrator.Steps(1))
	version, dirty, err = migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(61), version)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT organization FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&organization))
	require.NotNil(t, organization)
	require.Equal(t, "acme", *organization)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_workspace_disables(workspace_id) VALUES($1)`, workspaceID)
	require.NoError(t, err)
	var disabledAt sql.NullTime
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT disabled_at FROM forgejo_workspace_disables WHERE workspace_id=$1`, workspaceID).Scan(&disabledAt))
	require.True(t, disabledAt.Valid, "disabled_at defaults to NOW()")

	// Rolling 000061 back drops the marker table but keeps the connection and
	// its organization restriction; rolling 000060 back drops the column while
	// preserving the credentials.
	require.NoError(t, migrator.Steps(-1))
	var tables int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='forgejo_workspace_disables'`).Scan(&tables))
	require.Zero(t, tables)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT organization FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&organization))
	require.Equal(t, "acme", *organization)

	require.NoError(t, migrator.Steps(-1))
	var columns int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='forgejo_connections' AND column_name='organization'`).Scan(&columns))
	require.Zero(t, columns)
	var token, login string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT token,account_login FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&token, &login))
	require.Equal(t, "encrypted-token", token)
	require.Equal(t, "owner", login)

	// Re-applying both migrations leaves the restored column empty.
	require.NoError(t, migrator.Steps(1))
	require.NoError(t, migrator.Steps(1))
	version, dirty, err = migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(61), version)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT organization FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&organization))
	require.Nil(t, organization)
}
