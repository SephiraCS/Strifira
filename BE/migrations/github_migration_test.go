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

// TestGitHubMigrationRoundTrip verifies 000062 renames the Forgejo tables to
// GitHub without losing data, and that the down migration restores the old
// names.
func TestGitHubMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_github_rename_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, migrator.Migrate(61))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID := uuid.New(), uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,'github-migration@example.test','Migration','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Migration','github-migration',$2)`, workspaceID, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_connections(id,workspace_id,instance_url,organization,token,account_login)
		VALUES($1,$2,'https://forgejo.example.test','acme','encrypted-token','owner')`, uuid.New(), workspaceID)
	require.NoError(t, err)
	repoID := uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_repos(id,workspace_id,forgejo_repo_id,full_name,instance_url,default_branch,webhook_secret,is_active)
		VALUES($1,$2,7,'acme/project','https://forgejo.example.test','main','encrypted-secret',true)`, repoID, workspaceID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_pull_requests(id,workspace_id,forgejo_repo_id,number,title,state,checks_green,author_login,html_url,merged_at)
		VALUES($1,$2,$3,5,'Preserve history (KUA-5)','merged',true,'owner','https://forgejo.example.test/acme/project/pull/5','2026-09-30T12:00:00Z')`, uuid.New(), workspaceID, repoID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_workspace_disables(workspace_id) VALUES($1)`, workspaceID)
	require.NoError(t, err)

	// 000062 renames every table (and the repo id column), keeping all rows.
	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(62), version)
	var token, organization string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT token,organization FROM github_connections WHERE workspace_id=$1`, workspaceID).Scan(&token, &organization))
	require.Equal(t, "encrypted-token", token)
	require.Equal(t, "acme", organization)
	var fullName string
	var remoteID int64
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT github_repo_id,full_name FROM github_repos WHERE id=$1`, repoID).Scan(&remoteID, &fullName))
	require.EqualValues(t, 7, remoteID)
	require.Equal(t, "acme/project", fullName)
	var state string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT state FROM github_pull_requests WHERE github_repo_id=$1 AND number=5`, repoID).Scan(&state))
	require.Equal(t, "merged", state, "PR history survives the rename")
	var disabled int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM github_workspace_disables WHERE workspace_id=$1`, workspaceID).Scan(&disabled))
	require.Equal(t, 1, disabled)
	var oldTables int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'forgejo%'`).Scan(&oldTables))
	require.Zero(t, oldTables)

	// Rolling 000062 back restores the Forgejo names and the data.
	require.NoError(t, migrator.Steps(-1))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT token FROM forgejo_connections WHERE workspace_id=$1`, workspaceID).Scan(&token))
	require.Equal(t, "encrypted-token", token)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT full_name FROM forgejo_repos WHERE forgejo_repo_id=7 AND workspace_id=$1`, workspaceID).Scan(&fullName))
	require.Equal(t, "acme/project", fullName)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT state FROM forgejo_pull_requests WHERE number=5 AND workspace_id=$1`, workspaceID).Scan(&state))
	require.Equal(t, "merged", state)
	var newTables int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'github%'`).Scan(&newTables))
	require.Zero(t, newTables)

	// Re-applying leaves one set of GitHub tables with the same rows.
	require.NoError(t, migrator.Steps(1))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT token FROM github_connections WHERE workspace_id=$1`, workspaceID).Scan(&token))
	require.Equal(t, "encrypted-token", token)
}
