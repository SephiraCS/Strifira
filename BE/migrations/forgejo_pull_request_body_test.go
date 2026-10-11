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

func TestGitHubPullRequestBodyMigrationRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_github_body_migration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, migrator.Migrate(52))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID, repoID, prID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,'github-body-migration@example.test','Migration','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Migration','github-body-migration',$2)`, workspaceID, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_repos(id,workspace_id,forgejo_repo_id,full_name,instance_url) VALUES($1,$2,42,'org/project','https://github.example.test')`, repoID, workspaceID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO forgejo_pull_requests(id,workspace_id,forgejo_repo_id,number,title,state,checks_green,author_login,html_url,head_sha)
		VALUES($1,$2,$3,7,'Implement FA-1','open',true,'contributor','https://github.example.test/org/project/pulls/7','head-commit')`, prID, workspaceID, repoID)
	require.NoError(t, err)

	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(53), version)
	var body string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT body FROM forgejo_pull_requests WHERE id=$1`, prID).Scan(&body))
	require.Empty(t, body, "existing rows cannot invent an earlier webhook's body")
	_, err = db.ExecContext(t.Context(), `UPDATE forgejo_pull_requests SET body='Fixes FA-2' WHERE id=$1`, prID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT body FROM forgejo_pull_requests WHERE id=$1`, prID).Scan(&body))
	require.Equal(t, "Fixes FA-2", body)

	require.NoError(t, migrator.Steps(-1))
	var title, sha string
	var green bool
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT title,head_sha,checks_green FROM forgejo_pull_requests WHERE id=$1`, prID).Scan(&title, &sha, &green))
	require.Equal(t, "Implement FA-1", title)
	require.Equal(t, "head-commit", sha)
	require.True(t, green, "rollback must preserve the PR and its check state")
	var columns int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='forgejo_pull_requests' AND column_name='body'`).Scan(&columns))
	require.Zero(t, columns)
	require.NoError(t, migrator.Steps(1))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT body FROM forgejo_pull_requests WHERE id=$1`, prID).Scan(&body))
	require.Empty(t, body)
}
