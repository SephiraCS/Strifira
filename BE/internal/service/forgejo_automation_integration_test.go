package service

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
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forgejoAutomationEnv provisions a throwaway migrated database and returns
// the service under test plus helpers for assertions.
type forgejoAutomationEnv struct {
	db  *sqlx.DB
	svc *ForgejoService
}

func forgejoAutomationTestURL(t *testing.T, databaseURL, databaseName string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	return parsed.String()
}

func setupForgejoAutomation(t *testing.T) *forgejoAutomationEnv {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}

	ctx := context.Background()
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.PingContext(ctx))

	databaseName := "ku_forgejo_auto_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})

	testURL := forgejoAutomationTestURL(t, databaseURL, databaseName)
	migrator, err := migrate.New("file://../../migrations", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Up())

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return &forgejoAutomationEnv{
		db: db,
		svc: &ForgejoService{
			fRepo:          repository.NewForgejoRepository(db),
			issueRepo:      repository.NewIssueRepository(db),
			teamStatusRepo: repository.NewTeamStatusRepository(db),
			historyRepo:    repository.NewIssueHistoryRepository(db),
		},
	}
}

type forgejoAutomationFixtures struct {
	workspace   uuid.UUID
	team        uuid.UUID
	todoID      uuid.UUID
	progressID  uuid.UUID
	issueID     uuid.UUID
	aliceUserID uuid.UUID
	bobUserID   uuid.UUID
	pusherID    uuid.UUID
}

// seedAutomationWorkspace creates alice (creator + assignee), bob (second
// assignee), pusher (the Forgejo login to match), an issue assigned to both
// alice and bob, and a branch_created → in_progress rule with
// assign_forgejo_user enabled — the exact production shape of the bug.
func seedAutomationWorkspace(t *testing.T, db *sqlx.DB, pusherIsMember bool) forgejoAutomationFixtures {
	t.Helper()
	ctx := context.Background()
	f := forgejoAutomationFixtures{
		workspace:   uuid.New(),
		team:        uuid.New(),
		aliceUserID: uuid.New(),
		bobUserID:   uuid.New(),
		pusherID:    uuid.New(),
	}

	for _, u := range []struct {
		id    uuid.UUID
		email string
		name  string
	}{
		{f.aliceUserID, "alice@example.test", "Alice"},
		{f.bobUserID, "bob@example.test", "Bob"},
		{f.pusherID, "pusher@example.test", "Pusher"},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO users (id,email,name,password_hash) VALUES ($1,$2,$3,'x')`, u.id, u.email, u.name)
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Forgejo Auto','forgejo-auto',$2)`, f.workspace, f.aliceUserID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'owner'), ($1,$3,'member')`, f.workspace, f.aliceUserID, f.bobUserID)
	require.NoError(t, err)
	if pusherIsMember {
		_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'member')`, f.workspace, f.pusherID)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,'Core','FA')`, f.team, f.workspace)
	require.NoError(t, err)

	f.todoID, f.progressID = uuid.New(), uuid.New()
	for _, s := range []struct {
		id       uuid.UUID
		name     string
		slug     string
		category string
		position int
	}{
		{f.todoID, "Todo", "todo", "unstarted", 0},
		{f.progressID, "In Progress", "in_progress", "started", 1},
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO team_statuses (id,team_id,name,slug,category,position,is_default)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, s.id, f.team, s.name, s.slug, s.category, s.position, s.slug == "todo")
		require.NoError(t, err)
	}

	f.issueID = uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO issues (id,workspace_id,team_id,number,identifier_text,title,status,status_id,priority,creator_id,assignee_id)
		VALUES ($1,$2,$3,1,'FA-1','Multi-assignee issue','todo',$4,0,$5,$6)`, f.issueID, f.workspace, f.team, f.todoID, f.aliceUserID, f.aliceUserID)
	require.NoError(t, err)
	for _, uid := range []uuid.UUID{f.aliceUserID, f.bobUserID} {
		_, err = db.ExecContext(ctx, `INSERT INTO issue_assignees (issue_id,user_id) VALUES ($1,$2)`, f.issueID, uid)
		require.NoError(t, err)
	}

	_, err = db.ExecContext(ctx, `INSERT INTO forgejo_auto_transitions (workspace_id,event,target_status,target_status_id,assign_forgejo_user,is_active)
		VALUES ($1,'branch_created','in_progress',$2,TRUE,TRUE)`, f.workspace, f.progressID)
	require.NoError(t, err)
	if pusherIsMember {
		// The mapping table FKs workspace_members, so a former member simply
		// has no mapping left — exactly the stale-login production shape.
		_, err = db.ExecContext(ctx, `INSERT INTO forgejo_user_mappings (workspace_id,forgejo_login,user_id) VALUES ($1,'pusher',$2)`, f.workspace, f.pusherID)
		require.NoError(t, err)
	}

	return f
}

func (e *forgejoAutomationEnv) issueAssigneeIDs(t *testing.T, issueID uuid.UUID) []string {
	t.Helper()
	var ids []string
	err := e.db.Select(&ids, `SELECT user_id::text FROM issue_assignees WHERE issue_id = $1 ORDER BY user_id`, issueID)
	require.NoError(t, err)
	return ids
}

func (e *forgejoAutomationEnv) issueStatusSlug(t *testing.T, issueID uuid.UUID) string {
	t.Helper()
	var slug string
	err := e.db.Get(&slug, `SELECT ts.slug FROM issues i JOIN team_statuses ts ON ts.id = i.status_id WHERE i.id = $1`, issueID)
	require.NoError(t, err)
	return slug
}

// The rule target joins the assignee set; existing assignees are kept. This
// is the regression test for tickets vanishing from assignee-filtered views
// when the automation replaced the whole assignee list.
func TestForgejoAutoTransition_AddsAssigneeWithoutRemovingOthers(t *testing.T) {
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, true)
	ctx := context.Background()

	issue, err := env.svc.issueRepo.GetByID(ctx, f.issueID)
	require.NoError(t, err)
	require.NotNil(t, issue)

	env.svc.applyAutoTransition(ctx, f.workspace, "branch_created", issue, "pusher")

	assert.Equal(t, "in_progress", env.issueStatusSlug(t, f.issueID))
	assignees := env.issueAssigneeIDs(t, f.issueID)
	assert.ElementsMatch(t, []string{f.aliceUserID.String(), f.bobUserID.String(), f.pusherID.String()}, assignees)

	// The legacy primary assignee stays untouched.
	var primary *string
	err = env.db.Get(&primary, `SELECT assignee_id::text FROM issues WHERE id = $1`, f.issueID)
	require.NoError(t, err)
	require.NotNil(t, primary)
	assert.Equal(t, f.aliceUserID.String(), *primary)
}

// An unresolvable Forgejo login (former member, ambiguous name) must not void
// the transition: the status still moves and the assignee set is left alone.
func TestForgejoAutoTransition_UnresolvableLoginStillMovesStatus(t *testing.T) {
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, false) // pusher has no membership
	ctx := context.Background()

	issue, err := env.svc.issueRepo.GetByID(ctx, f.issueID)
	require.NoError(t, err)
	require.NotNil(t, issue)

	env.svc.applyAutoTransition(ctx, f.workspace, "branch_created", issue, "pusher")

	assert.Equal(t, "in_progress", env.issueStatusSlug(t, f.issueID))
	assignees := env.issueAssigneeIDs(t, f.issueID)
	assert.ElementsMatch(t, []string{f.aliceUserID.String(), f.bobUserID.String()}, assignees)
}

// Re-running the rule for an already-assigned target is a stable no-op on the
// assignee set (status part stays idempotent too).
func TestForgejoAutoTransition_AlreadyAssignedIsIdempotent(t *testing.T) {
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, true)
	ctx := context.Background()

	issue, err := env.svc.issueRepo.GetByID(ctx, f.issueID)
	require.NoError(t, err)
	require.NotNil(t, issue)

	env.svc.applyAutoTransition(ctx, f.workspace, "branch_created", issue, "pusher")
	env.svc.applyAutoTransition(ctx, f.workspace, "branch_created", issue, "pusher")

	assignees := env.issueAssigneeIDs(t, f.issueID)
	assert.Len(t, assignees, 3)
	assert.Equal(t, "in_progress", env.issueStatusSlug(t, f.issueID))
}

// An issue with no primary assignee gets the target as primary when the rule
// adds them.
func TestForgejoAutoTransition_FillsMissingPrimaryAssignee(t *testing.T) {
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, true)
	ctx := context.Background()

	// Unassigned issue.
	unassignedID := uuid.New()
	_, err := env.db.ExecContext(ctx, `INSERT INTO issues (id,workspace_id,team_id,number,identifier_text,title,status,status_id,priority,creator_id)
		VALUES ($1,$2,$3,2,'FA-2','Unassigned','todo',$4,0,$5)`, unassignedID, f.workspace, f.team, f.todoID, f.aliceUserID)
	require.NoError(t, err)

	issue, err := env.svc.issueRepo.GetByID(ctx, unassignedID)
	require.NoError(t, err)
	require.NotNil(t, issue)

	env.svc.applyAutoTransition(ctx, f.workspace, "branch_created", issue, "pusher")

	assert.Equal(t, "in_progress", env.issueStatusSlug(t, unassignedID))
	assignees := env.issueAssigneeIDs(t, unassignedID)
	assert.Equal(t, []string{f.pusherID.String()}, assignees)

	var primary *string
	err = env.db.Get(&primary, `SELECT assignee_id::text FROM issues WHERE id = $1`, unassignedID)
	require.NoError(t, err)
	require.NotNil(t, primary)
	assert.Equal(t, f.pusherID.String(), *primary)
}
