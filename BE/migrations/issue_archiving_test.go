package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestIssueArchivingMigrationAndQueries(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}

	ctx := t.Context()
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.PingContext(ctx))

	databaseName := "ku_archive_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})

	testURL := databaseURLWithName(t, databaseURL, databaseName)
	migrator, err := migrate.New("file://.", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Up())

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := repository.NewIssueRepository(db)

	userID := uuid.New()
	workspaceA, workspaceB := uuid.New(), uuid.New()
	teamA, teamB := uuid.New(), uuid.New()
	doneA, todoA, doneB := uuid.New(), uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO users (id,email,name,password_hash) VALUES ($1,'archive@example.test','Archive Tester','test')`, userID)
	require.NoError(t, err)
	for _, fixture := range []struct {
		workspaceID uuid.UUID
		teamID      uuid.UUID
		slug        string
		key         string
		enabled     bool
	}{
		{workspaceA, teamA, "archive-a", "AA", true},
		{workspaceB, teamB, "archive-b", "BB", false},
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO workspaces (id,name,slug,owner_id,auto_archive_done_issues) VALUES ($1,$2,$2,$3,$4)`, fixture.workspaceID, fixture.slug, userID, fixture.enabled)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, fixture.workspaceID, userID)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,$3,$4)`, fixture.teamID, fixture.workspaceID, fixture.slug, fixture.key)
		require.NoError(t, err)
	}
	for _, status := range []struct {
		id       uuid.UUID
		teamID   uuid.UUID
		name     string
		slug     string
		category string
	}{
		{doneA, teamA, "Done", "done", "completed"},
		{todoA, teamA, "Todo", "todo", "unstarted"},
		{doneB, teamB, "Done", "done", "completed"},
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO team_statuses (id,team_id,name,slug,category) VALUES ($1,$2,$3,$4,$5)`, status.id, status.teamID, status.name, status.slug, status.category)
		require.NoError(t, err)
	}
	oldDone, recentDone, openIssue, disabledDone := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, issue := range []struct {
		id          uuid.UUID
		workspaceID uuid.UUID
		teamID      uuid.UUID
		statusID    uuid.UUID
		number      int
		identifier  string
		status      string
		age         string
	}{
		{oldDone, workspaceA, teamA, doneA, 1, "AA-1", "done", "31 days"},
		{recentDone, workspaceA, teamA, doneA, 2, "AA-2", "done", "29 days"},
		{openIssue, workspaceA, teamA, todoA, 3, "AA-3", "todo", "31 days"},
		{disabledDone, workspaceB, teamB, doneB, 1, "BB-1", "done", "31 days"},
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO issues (id,workspace_id,team_id,status_id,number,identifier_text,title,creator_id,status,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$6,$7,$8,NOW()-$9::interval)`, issue.id, issue.workspaceID, issue.teamID, issue.statusID, issue.number, issue.identifier, userID, issue.status, issue.age)
		require.NoError(t, err)
	}

	archived, err := repo.AutoArchiveCompleted(ctx)
	require.NoError(t, err)
	require.Equal(t, []repository.ArchivedWorkspaceCount{{WorkspaceID: workspaceA, IssueCount: 1}}, archived)
	active, total, err := repo.List(ctx, workspaceA, dto.IssueFilterParams{})
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, active, 2)
	archivedIssues, total, err := repo.List(ctx, workspaceA, dto.IssueFilterParams{Archived: "true"})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, oldDone, archivedIssues[0].ID)

	refused, err := repo.SetArchived(ctx, workspaceA, openIssue, true)
	require.NoError(t, err)
	require.Nil(t, refused)
	manual, err := repo.SetArchived(ctx, workspaceA, recentDone, true)
	require.NoError(t, err)
	require.NotNil(t, manual.ArchivedAt)
	manual, err = repo.SetArchived(ctx, workspaceA, recentDone, false)
	require.NoError(t, err)
	require.True(t, manual.AutoArchiveExempt)
	restored, err := repo.SetArchived(ctx, workspaceA, oldDone, false)
	require.NoError(t, err)
	require.Nil(t, restored.ArchivedAt)
	require.True(t, restored.AutoArchiveExempt)
	archived, err = repo.AutoArchiveCompleted(ctx)
	require.NoError(t, err)
	require.Empty(t, archived)

	_, err = db.ExecContext(ctx, `UPDATE issues SET status='todo',status_id=$1 WHERE id=$2`, todoA, oldDone)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE issues SET status='done',status_id=$1 WHERE id=$2`, doneA, oldDone)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE issues SET completed_at=NOW()-INTERVAL '31 days' WHERE id=$1`, oldDone)
	require.NoError(t, err)
	archived, err = repo.AutoArchiveCompleted(ctx)
	require.NoError(t, err)
	require.Equal(t, []repository.ArchivedWorkspaceCount{{WorkspaceID: workspaceA, IssueCount: 1}}, archived)
	reopened, err := repo.GetByID(ctx, oldDone)
	require.NoError(t, err)
	require.NotNil(t, reopened.ArchivedAt)
	reopened.Status = "todo"
	reopened.StatusID = &todoA
	require.NoError(t, repo.Update(ctx, reopened))
	require.Nil(t, reopened.ArchivedAt)
	require.Nil(t, reopened.CompletedAt)
	require.False(t, reopened.AutoArchiveExempt)

	other, err := repo.GetByID(ctx, disabledDone)
	require.NoError(t, err)
	require.Nil(t, other.ArchivedAt)
}
