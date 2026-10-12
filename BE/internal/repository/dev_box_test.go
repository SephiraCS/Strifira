package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestDevBoxLogTruncation(t *testing.T) {
	for _, test := range []struct {
		content   string
		cap       int64
		want      string
		truncated bool
	}{
		{"ab€cd", 4, "ab", true},
		{"ab€cd", 5, "ab€", true},
		{"ab€cd", 7, "ab€cd", false},
		{"abc", 0, "", true},
		{"abc", -1, "", true},
		{"", 0, "", false},
	} {
		got, truncated := truncateDevBoxLog(test.content, test.cap)
		require.Equal(t, test.want, got)
		require.Equal(t, test.truncated, truncated)
		require.True(t, utf8.ValidString(got))
	}
	require.ErrorIs(t, devBoxError(sql.ErrNoRows), ErrDevBoxNotFound)
}

func devBoxTestDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	databaseName := "ku_devbox_repository_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	migrator, err := migrate.New("file://../../migrations", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	// The dev-box fixtures speak the github_* names the repository layer
	// queries; the rename lands in 000062.
	require.NoError(t, migrator.Migrate(62))
	db, err := sqlx.ConnectContext(t.Context(), "pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type devBoxFixture struct {
	db                                   *sqlx.DB
	repo                                 *DevBoxRepository
	userID, workspaceID, issueID, repoID uuid.UUID
	box                                  *domain.DevBox
}

func newDevBoxFixture(t *testing.T, db *sqlx.DB) devBoxFixture {
	t.Helper()
	f := devBoxFixture{db: db, repo: NewDevBoxRepository(db), userID: uuid.New(), workspaceID: uuid.New(), issueID: uuid.New(), repoID: uuid.New()}
	teamID := uuid.New()
	statusID := uuid.New()
	suffix := strings.ReplaceAll(f.workspaceID.String(), "-", "")
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'Worker test','test')`, []any{f.userID, suffix + "@example.test"}},
		{`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Worker test',$2,$3)`, []any{f.workspaceID, suffix, f.userID}},
		{`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, []any{f.workspaceID, f.userID}},
		{`INSERT INTO teams(id,workspace_id,name,key) VALUES($1,$2,'Worker team','BOX')`, []any{teamID, f.workspaceID}},
		{`INSERT INTO team_statuses(id,team_id,name,slug,category) VALUES($1,$2,'Todo','todo','unstarted')`, []any{statusID, teamID}},
		{`INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES($1,$2,$3,1,'BOX-1','Implement a change',$4,$5)`, []any{f.issueID, f.workspaceID, teamID, f.userID, statusID}},
		{`INSERT INTO github_repos(id,workspace_id,github_repo_id,full_name,instance_url) VALUES($1,$2,1,$3,'https://github.test')`, []any{f.repoID, f.workspaceID, "tests/" + suffix}},
	} {
		_, err := db.ExecContext(t.Context(), stmt.query, stmt.args...)
		require.NoError(t, err)
	}
	f.box = &domain.DevBox{ID: uuid.New(), WorkspaceID: f.workspaceID, CreatedByUserID: f.userID, Name: "ku", Enabled: true, TokenHash: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""), TokenPrefix: "kub_test", Providers: json.RawMessage(`[{"id":"codex","version":"test"}]`)}
	require.NoError(t, f.repo.CreateBox(t.Context(), f.box))
	return f
}
func (f devBoxFixture) job() *domain.DevBoxJob {
	id := uuid.New()
	return &domain.DevBoxJob{ID: id, WorkspaceID: f.workspaceID, BoxID: f.box.ID, RequestedByUserID: f.userID, IssueID: f.issueID, IssueIdentifier: "BOX-1", GitHubRepoID: f.repoID,
		Provider: "codex", Prompt: "Implement a real change and test it", RepositoryURL: "https://github.test/test/repo.git", RepositoryFullName: "test/repo", BaseBranch: "main", WorkingBranch: "ku/BOX-1-" + id.String(), MaxRuntimeSeconds: 300}
}
func (f devBoxFixture) runningJob(t *testing.T) (*domain.DevBoxJob, string) {
	t.Helper()
	job := f.job()
	require.NoError(t, f.repo.CreateJob(t.Context(), job))
	hash := strings.Repeat("l", 64)
	claimed, err := f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, hash, time.Minute)
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)
	running, err := f.repo.StartJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
	require.NoError(t, err)
	require.Equal(t, domain.DevBoxJobRunning, running.Status)
	return running, hash
}

func TestDevBoxRepositoryPostgres(t *testing.T) {
	db := devBoxTestDatabase(t)
	t.Run("creator and workspace boundaries plus foreign keys", func(t *testing.T) {
		f, other := newDevBoxFixture(t, db), newDevBoxFixture(t, db)
		job := f.job()
		require.NoError(t, f.repo.CreateJob(t.Context(), job))
		_, err := f.repo.GetBox(t.Context(), f.workspaceID, f.box.ID, other.userID)
		require.ErrorIs(t, err, ErrDevBoxNotFound)
		boxes, err := f.repo.ListBoxes(t.Context(), f.workspaceID, other.userID)
		require.NoError(t, err)
		require.Empty(t, boxes)
		_, err = f.repo.GetJob(t.Context(), other.workspaceID, job.ID, f.userID)
		require.ErrorIs(t, err, ErrDevBoxNotFound)
		_, err = f.repo.CancelJob(t.Context(), f.workspaceID, job.ID, other.userID)
		require.ErrorIs(t, err, ErrDevBoxNotFound)
		_, err = f.repo.ListJobLogs(t.Context(), f.workspaceID, job.ID, other.userID, 0, 100)
		require.ErrorIs(t, err, ErrDevBoxNotFound)
		jobs, total, err := f.repo.ListJobs(t.Context(), f.workspaceID, other.userID, nil, nil, 1, 100)
		require.NoError(t, err)
		require.Empty(t, jobs)
		require.Zero(t, total)
		for _, field := range []string{"issue", "repo", "creator", "workspace"} {
			invalid := f.job()
			switch field {
			case "issue":
				invalid.IssueID = other.issueID
			case "repo":
				invalid.GitHubRepoID = other.repoID
			case "creator":
				invalid.RequestedByUserID = other.userID
			case "workspace":
				invalid.WorkspaceID = other.workspaceID
			}
			err = f.repo.CreateJob(t.Context(), invalid)
			require.Error(t, err, field)
		}
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET requested_by_user_id=$2 WHERE id=$1`, job.ID, other.userID)
		require.Error(t, err)
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET issue_id=$2 WHERE id=$1`, job.ID, other.issueID)
		require.Error(t, err)
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET github_repo_id=$2 WHERE id=$1`, job.ID, other.repoID)
		require.Error(t, err)
		_, err = db.ExecContext(t.Context(), `DELETE FROM issues WHERE id=$1`, f.issueID)
		require.NoError(t, err)
		_, err = f.repo.GetJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.ErrorIs(t, err, ErrDevBoxNotFound)
	})
	t.Run("only one concurrent claimant and stable start deadline", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		for range 3 {
			require.NoError(t, f.repo.CreateJob(t.Context(), f.job()))
		}
		type outcome struct {
			job  *domain.DevBoxJob
			err  error
			hash string
		}
		results := make(chan outcome, 24)
		var wg sync.WaitGroup
		for range 24 {
			wg.Go(func() {
				hash := uuid.NewString()
				job, err := f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, hash, time.Minute)
				results <- outcome{job, err, hash}
			})
		}
		wg.Wait()
		close(results)
		var winner outcome
		count := 0
		for result := range results {
			require.NoError(t, result.err)
			if result.job != nil {
				count++
				winner = result
			}
		}
		require.Equal(t, 1, count)
		started, err := f.repo.StartJob(t.Context(), f.box.ID, winner.job.ID, f.box.TokenHash, winner.hash, time.Minute)
		require.NoError(t, err)
		again, err := f.repo.StartJob(t.Context(), f.box.ID, winner.job.ID, f.box.TokenHash, winner.hash, time.Minute)
		require.NoError(t, err)
		require.Equal(t, started.StartedAt, again.StartedAt)
		require.Equal(t, started.DeadlineAt, again.DeadlineAt)
		_, err = f.repo.RenewJob(t.Context(), f.box.ID, winner.job.ID, f.box.TokenHash, "another-lease", time.Minute)
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET status='leased',lease_hash='other',lease_expires_at=NOW()+INTERVAL '1 minute' WHERE box_id=$1 AND status='queued'`, f.box.ID)
		require.Error(t, err, "partial unique index protects active job invariant")
	})
	t.Run("lease expiry fails work and cannot revive or replay it", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		_, err := db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID)
		require.NoError(t, err)
		_, err = f.repo.RenewJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		_, err = f.repo.GetWorkerJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash)
		require.NoError(t, err, "completion pre-read permits the original lease even after expiry")
		_, err = f.repo.GetWorkerJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, "wrong-lease")
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		_, _, err = f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobLog{Sequence: 1, Stream: "stdout", Content: "stale"}, 100)
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		completed, err := f.repo.CompleteJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobResult{Status: domain.DevBoxJobSucceeded})
		require.NoError(t, err)
		require.Equal(t, domain.DevBoxJobFailed, completed.Status)
		again, err := f.repo.CompleteJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobResult{Status: domain.DevBoxJobSucceeded, Summary: "overwrite failed"})
		require.NoError(t, err)
		require.Equal(t, completed.Result, again.Result)
		idle, err := f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, "new", time.Minute)
		require.NoError(t, err)
		require.Nil(t, idle)
	})
	t.Run("deadline and cancellation override worker success", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		_, err := db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET deadline_at=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID)
		require.NoError(t, err)
		done, err := f.repo.CompleteJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobResult{Status: domain.DevBoxJobSucceeded})
		require.NoError(t, err)
		require.Equal(t, domain.DevBoxJobTimeout, done.Status)
		job, hash = f.runningJob(t)
		cancelled, err := f.repo.CancelJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.NoError(t, err)
		require.True(t, cancelled.CancelRequested)
		renewed, err := f.repo.RenewJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
		require.NoError(t, err)
		require.True(t, renewed.CancelRequested)
		done, err = f.repo.CompleteJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobResult{Status: domain.DevBoxJobSucceeded})
		require.NoError(t, err)
		require.Equal(t, domain.DevBoxJobCancelled, done.Status)
		queued := f.job()
		require.NoError(t, f.repo.CreateJob(t.Context(), queued))
		done, err = f.repo.CancelJob(t.Context(), f.workspaceID, queued.ID, f.userID)
		require.NoError(t, err)
		require.Equal(t, domain.DevBoxJobCancelled, done.Status)
	})
	t.Run("duplicate logs and UTF8 bounded cursor stream", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		first := &domain.DevBoxJobLog{Sequence: 1, Stream: "stdout", Content: "ab€cd"}
		accepted, truncated, err := f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, first, 4)
		require.NoError(t, err)
		require.True(t, accepted)
		require.True(t, truncated)
		require.Equal(t, "ab", first.Content)
		accepted, _, err = f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobLog{Sequence: 1, Stream: "stdout", Content: "ab€cd"}, 4)
		require.NoError(t, err)
		require.True(t, accepted)
		accepted, _, err = f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobLog{Sequence: 3, Stream: "stderr", Content: "xyz"}, 4)
		require.NoError(t, err)
		require.True(t, accepted)
		accepted, truncated, err = f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobLog{Sequence: 2, Stream: "stdout", Content: "never"}, 4)
		require.NoError(t, err)
		require.False(t, accepted)
		require.True(t, truncated)
		current, err := f.repo.GetJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.NoError(t, err)
		require.Equal(t, int64(4), current.LogsBytes)
		require.True(t, current.LogsTruncated)
		logs, err := f.repo.ListJobLogs(t.Context(), f.workspaceID, job.ID, f.userID, 0, 1)
		require.NoError(t, err)
		require.Len(t, logs, 1)
		next, err := f.repo.ListJobLogs(t.Context(), f.workspaceID, job.ID, f.userID, logs[0].ID, 100)
		require.NoError(t, err)
		require.Len(t, next, 1)
		require.Equal(t, "xy", next[0].Content)
	})
	t.Run("rotation disable revoke and demotion invalidate authority", func(t *testing.T) {
		for _, action := range []string{"rotate", "disable", "revoke", "demote", "remove"} {
			t.Run(action, func(t *testing.T) {
				f := newDevBoxFixture(t, db)
				job, hash := f.runningJob(t)
				require.NoError(t, f.repo.CreateJob(t.Context(), f.job()))
				switch action {
				case "rotate":
					_, err := f.repo.RotateBoxToken(t.Context(), f.workspaceID, f.box.ID, f.userID, strings.Repeat("r", 64), "kub_new")
					require.NoError(t, err)
				case "disable":
					_, err := f.repo.UpdateBox(t.Context(), f.workspaceID, f.box.ID, f.userID, nil, new(false))
					require.NoError(t, err)
				case "revoke":
					require.NoError(t, f.repo.RevokeBox(t.Context(), f.workspaceID, f.box.ID, f.userID))
				case "demote":
					_, err := db.ExecContext(t.Context(), `UPDATE workspace_members SET role='guest' WHERE workspace_id=$1 AND user_id=$2`, f.workspaceID, f.userID)
					require.NoError(t, err)
				case "remove":
					_, err := db.ExecContext(t.Context(), `DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2`, f.workspaceID, f.userID)
					require.NoError(t, err)
				}
				_, err := f.repo.GetBoxByTokenHash(t.Context(), f.box.TokenHash)
				require.ErrorIs(t, err, ErrDevBoxNotFound)
				_, err = f.repo.RenewJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
				require.ErrorIs(t, err, ErrDevBoxLeaseLost)
				require.NoError(t, f.repo.SweepExpiredJobs(t.Context()))
				jobs, _, err := f.repo.ListJobs(t.Context(), f.workspaceID, f.userID, nil, nil, 1, 100)
				require.NoError(t, err)
				for _, current := range jobs {
					require.Equal(t, domain.DevBoxJobCancelled, current.Status)
					require.Nil(t, current.LeaseExpiresAt)
				}
			})
		}
	})
	t.Run("sweeper timeout cancellation and no started replay", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		_, err := db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET deadline_at=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID)
		require.NoError(t, err)
		_, err = f.repo.CancelJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.NoError(t, err)
		require.NoError(t, f.repo.SweepExpiredJobs(t.Context()))
		expired, err := f.repo.GetJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.NoError(t, err)
		require.Equal(t, domain.DevBoxJobCancelled, expired.Status)
		_, err = f.repo.StartJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		idle, err := f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, "another", time.Minute)
		require.NoError(t, err)
		require.Nil(t, idle)
	})
	t.Run("concurrent duplicate log ingestion counts bytes once", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		var wg sync.WaitGroup
		results := make(chan error, 16)
		for range 16 {
			wg.Go(func() {
				accepted, _, err := f.repo.AppendLog(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, &domain.DevBoxJobLog{Sequence: 1, Stream: "stdout", Content: "once"}, 100)
				if err == nil && !accepted {
					err = errors.New("log not accepted")
				}
				results <- err
			})
		}
		wg.Wait()
		close(results)
		for err := range results {
			require.NoError(t, err)
		}
		current, err := f.repo.GetJob(t.Context(), f.workspaceID, job.ID, f.userID)
		require.NoError(t, err)
		require.Equal(t, int64(4), current.LogsBytes)
		logs, err := f.repo.ListJobLogs(t.Context(), f.workspaceID, job.ID, f.userID, 0, 100)
		require.NoError(t, err)
		require.Len(t, logs, 1)
	})
	t.Run("concurrent queue creation enforces limit under box lock", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		for range 98 {
			require.NoError(t, f.repo.CreateJob(t.Context(), f.job()))
		}
		var wg sync.WaitGroup
		results := make(chan error, 16)
		for range 16 {
			wg.Go(func() { results <- f.repo.CreateJob(t.Context(), f.job()) })
		}
		wg.Wait()
		close(results)
		created := 0
		for err := range results {
			if err == nil {
				created++
			} else {
				require.ErrorIs(t, err, ErrDevBoxConflict)
			}
		}
		require.Equal(t, 2, created)
		_, total, err := f.repo.ListJobs(t.Context(), f.workspaceID, f.userID, nil, nil, 1, 100)
		require.NoError(t, err)
		require.Equal(t, 100, total)
	})
	t.Run("renewal waiting for lock cannot revive an expired lease", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		job, hash := f.runningJob(t)
		tx, err := db.BeginTxx(t.Context(), nil)
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = lockDevBox(t.Context(), tx, f.box.ID)
		require.NoError(t, err)
		result := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := f.repo.RenewJob(t.Context(), f.box.ID, job.ID, f.box.TokenHash, hash, time.Minute)
			result <- err
		})
		require.Eventually(t, func() bool {
			var waiting bool
			err := db.GetContext(t.Context(), &waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
				WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR UPDATE OF b%')`)
			return err == nil && waiting
		}, 3*time.Second, 10*time.Millisecond)
		_, err = db.ExecContext(t.Context(), `UPDATE dev_box_jobs SET lease_expires_at=clock_timestamp()+INTERVAL '50 milliseconds' WHERE id=$1`, job.ID)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `SELECT pg_sleep(0.1)`)
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		wg.Wait()
		require.ErrorIs(t, <-result, ErrDevBoxLeaseLost)
	})
	t.Run("authenticated claim waiting behind token rotation is rejected", func(t *testing.T) {
		f := newDevBoxFixture(t, db)
		require.NoError(t, f.repo.CreateJob(t.Context(), f.job()))
		_, err := f.repo.GetBoxByTokenHash(t.Context(), f.box.TokenHash)
		require.NoError(t, err, "simulate HTTP authentication before rotation")
		tx, err := db.BeginTxx(t.Context(), nil)
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = lockDevBox(t.Context(), tx, f.box.ID)
		require.NoError(t, err)
		rotated := make(chan error, 1)
		claimed := make(chan error, 1)
		newHash := strings.Repeat("n", 64)
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := f.repo.RotateBoxToken(t.Context(), f.workspaceID, f.box.ID, f.userID, newHash, "kub_rotated")
			rotated <- err
		})
		waitForBoxLock := func(queryPrefix string) {
			t.Helper()
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.GetContext(t.Context(), &waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)`, queryPrefix+"%")
				return err == nil && waiting
			}, 3*time.Second, 10*time.Millisecond)
		}
		waitForBoxLock("SELECT * FROM dev_boxes")
		wg.Go(func() {
			_, err := f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, "lease-from-old-token", time.Minute)
			claimed <- err
		})
		waitForBoxLock("SELECT b.* FROM dev_boxes")
		require.NoError(t, tx.Commit())
		wg.Wait()
		require.NoError(t, <-rotated)
		require.ErrorIs(t, <-claimed, ErrDevBoxLeaseLost)
		require.NoError(t, f.repo.CreateJob(t.Context(), f.job()))
		_, err = f.repo.ClaimJob(t.Context(), f.box.ID, f.box.TokenHash, "old-token-after-rotation", time.Minute)
		require.ErrorIs(t, err, ErrDevBoxLeaseLost)
		job, err := f.repo.ClaimJob(t.Context(), f.box.ID, newHash, "valid-token-after-rotation", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, job)
	})
}
