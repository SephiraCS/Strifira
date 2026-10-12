package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// TestDevBoxResultStorageBoundUpgrade walks the 000057 upgrade with the
// era-correct schema and names: the 2 MiB constraint installed with the
// dev-box tables rejects a result the 24 MiB bound accepts, the upgrade
// preserves stored results, its aggregate limit still refuses oversized
// payloads, and a rollback that would discard or invalidate a stored result
// is refused while leaving the constraint untouched. The repository-driven
// checks of the same feature live in internal/service, which speaks the
// post-000062 github_* names and cannot run against this historical schema.
func TestDevBoxResultStorageBoundUpgrade(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_devbox_bound_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, migrator.Migrate(56))
	db, err := sql.Open("pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	userID, workspaceID, teamID, statusID, issueID, repoID, boxID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	suffix := strings.ReplaceAll(workspaceID.String(), "-", "")
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'Bound','test')`, []any{userID, suffix + "@example.test"}},
		{`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Bound',$2,$3)`, []any{workspaceID, suffix, userID}},
		{`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, []any{workspaceID, userID}},
		{`INSERT INTO teams(id,workspace_id,name,key) VALUES($1,$2,'Bound','BOX')`, []any{teamID, workspaceID}},
		{`INSERT INTO team_statuses(id,team_id,name,slug,category) VALUES($1,$2,'Todo','todo','unstarted')`, []any{statusID, teamID}},
		{`INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES($1,$2,$3,1,'BOX-1','Bound',$4,$5)`, []any{issueID, workspaceID, teamID, userID, statusID}},
		{`INSERT INTO forgejo_repos(id,workspace_id,forgejo_repo_id,full_name,instance_url) VALUES($1,$2,1,$3,'https://github.test')`, []any{repoID, workspaceID, "tests/" + suffix}},
		{`INSERT INTO dev_boxes(id,workspace_id,created_by_user_id,name,token_hash,token_prefix) VALUES($1,$2,$3,'ku',$4,'kub_test')`, []any{boxID, workspaceID, userID, strings.Repeat("a", 64)}},
		{`INSERT INTO dev_box_jobs(id,workspace_id,box_id,requested_by_user_id,issue_id,issue_identifier,forgejo_repo_id,provider,prompt,repository_url,repository_full_name,base_branch,working_branch,max_runtime_seconds)
			VALUES($1,$2,$3,$4,$5,'BOX-1',$6,'codex','Bound test','https://github.test/tests/repo.git','tests/repo','main','ku/BOX-1-bound',300)`,
			[]any{jobID, workspaceID, boxID, userID, issueID, repoID}},
	} {
		_, err = db.ExecContext(t.Context(), statement.query, statement.args...)
		require.NoError(t, err, statement.query[:40])
	}

	constraint := func() string {
		var definition string
		require.NoError(t, db.QueryRowContext(t.Context(), `SELECT pg_get_constraintdef(oid)
			FROM pg_constraint WHERE conrelid='dev_box_jobs'::regclass AND conname='dev_box_jobs_result_check'`).Scan(&definition))
		return definition
	}
	require.Contains(t, constraint(), "2097152", "schema 56 retains the original applied storage bound")

	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET status='failed',completed_at=NOW(),
		result=jsonb_build_object('status','failed','summary',repeat('x',512)) WHERE id=$1`, jobID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET result=jsonb_build_object('status','failed','summary',repeat('x',2200000)) WHERE id=$1`, jobID)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	require.True(t, ok, "the pre-upgrade schema rejects a result beyond 2 MiB")
	require.Equal(t, "23514", pgErr.Code)

	require.NoError(t, migrator.Steps(1))
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.False(t, dirty)
	require.Equal(t, uint(57), version)
	require.Contains(t, constraint(), "25165824", "the upgrade installs the 24 MiB aggregate bound")

	var preserved string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT result->>'summary' FROM dev_box_jobs WHERE id=$1`, jobID).Scan(&preserved))
	require.Equal(t, strings.Repeat("x", 512), preserved, "the upgrade preserves stored results")

	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET result=jsonb_build_object('status','failed','summary',repeat('x',2200000)) WHERE id=$1`, jobID)
	require.NoError(t, err, "the upgraded schema accepts a result beyond the old bound")
	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET result=jsonb_build_object('status','failed','summary',repeat('x',26000000)) WHERE id=$1`, jobID)
	pgErr, ok = errors.AsType[*pgconn.PgError](err)
	require.True(t, ok, "the aggregate bound still rejects oversized results")
	require.Equal(t, "23514", pgErr.Code)

	rollback, err := os.ReadFile("000057_dev_box_result_storage_bound.down.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(rollback))
	pgErr, ok = errors.AsType[*pgconn.PgError](err)
	require.True(t, ok, "rollback refuses to discard or invalidate larger stored results")
	require.Equal(t, "23514", pgErr.Code)
	require.Contains(t, constraint(), "25165824", "the refused rollback retains the current constraint atomically")
}
