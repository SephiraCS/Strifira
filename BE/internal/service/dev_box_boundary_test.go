package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/stretchr/testify/require"
)

func maximumDevBoxResult(character, branch string) domain.DevBoxJobResult {
	metadata := func(count int) []string {
		values := make([]string, count)
		for i := range count {
			values[i] = strings.Repeat(character, 4096)
		}
		return values
	}
	return domain.DevBoxJobResult{
		Status: domain.DevBoxJobFailed, ExitCode: -1, TestStatus: "not_run", Branch: branch,
		Summary: strings.Repeat(character, 8192), Patch: strings.Repeat(character, DevBoxMaxPatchBytes),
		ChangedFiles: metadata(500), TestsRun: metadata(100), RiskNotes: metadata(100),
	}
}

func TestDevBoxResultSerializedBoundsIncludeMaximumMetadata(t *testing.T) {
	for _, character := range []string{"<", "\x01"} {
		result := maximumDevBoxResult(character, "ku/"+strings.Repeat("b", 252))
		require.NoError(t, validateDevBoxResult(&result))
		require.NoError(t, validateDevBoxResultSize(&result))
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.Greater(t, len(encoded), 2<<20, "the old database bound rejected a valid result")
		require.LessOrEqual(t, len(encoded)+1024, DevBoxMaxResultBytes)
	}
	result := maximumDevBoxResult("x", strings.Repeat("b", 256))
	require.ErrorIs(t, validateDevBoxResult(&result), ErrInvalidDevBoxInput)
}

// Root qualification supplies a disposable PostgreSQL administrator URL. This
// fixture owns its database and never migrates or changes that source database.
func devBoxResultDatabase(t *testing.T) (*sqlx.DB, *migrate.Migrate) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_devbox_result_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
	})
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path, parsed.RawPath = "/"+name, ""
	migrator, err := migrate.New("file://../../migrations", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Migrate(53))
	db, err := sqlx.ConnectContext(t.Context(), "pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var constraint string
	require.NoError(t, db.GetContext(t.Context(), &constraint, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid='dev_box_jobs'::regclass AND conname='dev_box_jobs_result_check'`))
	require.Contains(t, constraint, "2097152", "schema 53 must retain the original applied storage bound")
	return db, migrator
}

func TestDevBoxCompletionPostgresBoundary(t *testing.T) {
	db, migrator := devBoxResultDatabase(t)
	userID, workspaceID, teamID, statusID, issueID, repoID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	suffix := strings.ReplaceAll(workspaceID.String(), "-", "")
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'Result test','test')`, []any{userID, suffix + "@example.test"}},
		{`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Result test',$2,$3)`, []any{workspaceID, suffix, userID}},
		{`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, []any{workspaceID, userID}},
		{`INSERT INTO teams(id,workspace_id,name,key) VALUES($1,$2,'Result team','BOX')`, []any{teamID, workspaceID}},
		{`INSERT INTO team_statuses(id,team_id,name,slug,category) VALUES($1,$2,'Todo','todo','unstarted')`, []any{statusID, teamID}},
		{`INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES($1,$2,$3,1,'BOX-1','Boundary test',$4,$5)`, []any{issueID, workspaceID, teamID, userID, statusID}},
		{`INSERT INTO forgejo_repos(id,workspace_id,forgejo_repo_id,full_name,instance_url) VALUES($1,$2,1,$3,'https://forgejo.test')`, []any{repoID, workspaceID, "tests/" + suffix}},
	} {
		_, err := db.ExecContext(t.Context(), statement.query, statement.args...)
		require.NoError(t, err)
	}
	store := repository.NewDevBoxRepository(db)
	box := &domain.DevBox{ID: uuid.New(), WorkspaceID: workspaceID, CreatedByUserID: userID, Name: "Boundary", Enabled: true, TokenHash: strings.Repeat("b", 64), TokenPrefix: "kub_test", Providers: json.RawMessage(`[{"id":"codex","version":"test"}]`)}
	require.NoError(t, store.CreateBox(t.Context(), box))
	svc := NewDevBoxService(store, nil, nil, nil)
	createJob := func() *domain.DevBoxJob {
		jobID := uuid.New()
		job := &domain.DevBoxJob{ID: jobID, WorkspaceID: workspaceID, BoxID: box.ID, RequestedByUserID: userID, IssueID: issueID, IssueIdentifier: "BOX-1", ForgejoRepoID: repoID,
			Provider: "codex", Prompt: "Boundary test", RepositoryURL: "https://forgejo.test/tests/repo.git", RepositoryFullName: "tests/repo", BaseBranch: "main", WorkingBranch: "ku/BOX-1-" + jobID.String(), MaxRuntimeSeconds: 300}
		require.NoError(t, store.CreateJob(t.Context(), job))
		return job
	}
	previousJob := createJob()
	previousResult, err := json.Marshal(domain.DevBoxJobResult{Status: domain.DevBoxJobFailed, ExitCode: 1,
		TestStatus: "not_run", Branch: previousJob.WorkingBranch, Summary: "Existing result before upgrade"})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET status='failed',completed_at=NOW(),result=$2 WHERE id=$1`, previousJob.ID, previousResult)
	require.NoError(t, err)
	maximum := maximumDevBoxResult("\x01", previousJob.WorkingBranch)
	require.NoError(t, validateDevBoxResult(&maximum))
	require.NoError(t, validateDevBoxResultSize(&maximum))
	maximumJSON, err := json.Marshal(maximum)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET result=$2 WHERE id=$1`, previousJob.ID, maximumJSON)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	require.True(t, ok)
	require.Equal(t, "23514", pgErr.Code, "the deployed schema rejects this accepted result before the upgrade")
	require.NoError(t, migrator.Up())
	version, dirty, err := migrator.Version()
	require.NoError(t, err)
	require.GreaterOrEqual(t, version, uint(57), "the upgrade reaches at least the bounded-result migration")
	require.False(t, dirty)
	var preserved json.RawMessage
	require.NoError(t, db.GetContext(t.Context(), &preserved, `SELECT result FROM dev_box_jobs WHERE id=$1`, previousJob.ID))
	require.JSONEq(t, string(previousResult), string(preserved))
	for _, character := range []string{"<", "\x01"} {
		job := createJob()
		lease := "kul_" + strings.Repeat("a", 64)
		hash := hashDevBoxToken(lease)
		claimed, err := store.ClaimJob(t.Context(), box.ID, box.TokenHash, hash, time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		require.Equal(t, job.ID, claimed.ID)
		_, err = store.StartJob(t.Context(), box.ID, job.ID, box.TokenHash, hash, time.Minute)
		require.NoError(t, err)
		result := maximumDevBoxResult(character, job.WorkingBranch)
		completed, err := svc.Complete(t.Context(), box, job.ID, dto.DevBoxCompleteRequest{LeaseToken: lease, Result: result})
		require.NoError(t, err, "every accepted maximum result must persist through the real service and repository")
		require.Equal(t, domain.DevBoxJobFailed, completed.Status)
		require.NotNil(t, completed.Result)
		var stored domain.DevBoxJobResult
		require.NoError(t, json.Unmarshal(*completed.Result, &stored))
		require.Equal(t, result, stored)
		var serializedBytes int
		require.NoError(t, db.GetContext(t.Context(), &serializedBytes, `SELECT octet_length(result::text) FROM dev_box_jobs WHERE id=$1`, job.ID))
		require.Greater(t, serializedBytes, 2<<20)
		require.LessOrEqual(t, serializedBytes, DevBoxMaxResultBytes)
		replayed, err := svc.Complete(t.Context(), box, job.ID, dto.DevBoxCompleteRequest{LeaseToken: lease, Result: result})
		require.NoError(t, err)
		require.NotNil(t, replayed.Result)
		require.JSONEq(t, string(*completed.Result), string(*replayed.Result))
		oversized, err := json.Marshal(map[string]string{"summary": strings.Repeat("x", DevBoxMaxResultBytes)})
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET result=$2 WHERE id=$1`, job.ID, oversized)
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		require.True(t, ok, "the database must still reject results beyond its aggregate bound")
		require.Equal(t, "23514", pgErr.Code)
	}
	rollback, err := os.ReadFile("../../migrations/000057_dev_box_result_storage_bound.down.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), string(rollback))
	pgErr, ok = errors.AsType[*pgconn.PgError](err)
	require.True(t, ok)
	require.Equal(t, "23514", pgErr.Code, "rollback must refuse to discard or invalidate larger stored results")
	var constraint string
	require.NoError(t, db.GetContext(t.Context(), &constraint, `SELECT pg_get_constraintdef(oid)
		FROM pg_constraint WHERE conrelid='dev_box_jobs'::regclass AND conname='dev_box_jobs_result_check'`))
	require.Contains(t, constraint, "25165824", "failed rollback must retain the current constraint atomically")
}
