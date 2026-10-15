package service

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/dto"
	"github.com/SephiraCS/strifira-backend/internal/realtime"
	"github.com/SephiraCS/strifira-backend/internal/repository"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func teamRenameTestURL(t *testing.T, databaseURL, databaseName string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	return parsed.String()
}

type teamRenameEnv struct {
	db  *sqlx.DB
	svc *TeamService
}

func setupTeamRename(t *testing.T) *teamRenameEnv {
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

	databaseName := "strifira_team_rename_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})

	testURL := teamRenameTestURL(t, databaseURL, databaseName)
	migrator, err := migrate.New("file://../../migrations", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Up())

	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	teamRepo := repository.NewTeamRepository(db)
	return &teamRenameEnv{
		db:  db,
		svc: NewTeamService(teamRepo, repository.NewTeamStatusRepository(db), realtime.NewHub()),
	}
}

type teamRenameFixtures struct {
	workspace  uuid.UUID
	owner      uuid.UUID
	teamID     uuid.UUID
	otherTeam  uuid.UUID
	statusTodo uuid.UUID
}

// seedTwoTeams creates a workspace with team ENG (three issues ENG-1..3) and
// team OTH holding the identifier OTH-1 plus a blocking identifier "NEW-2"
// (for the collision case: renaming ENG to NEW would collide on number 2).
func seedTwoTeams(t *testing.T, db *sqlx.DB, blockCollision bool) teamRenameFixtures {
	t.Helper()
	ctx := context.Background()
	f := teamRenameFixtures{
		workspace:  uuid.New(),
		owner:      uuid.New(),
		teamID:     uuid.New(),
		otherTeam:  uuid.New(),
		statusTodo: uuid.New(),
	}

	_, err := db.ExecContext(ctx, `INSERT INTO users (id,email,name,password_hash) VALUES ($1,'rename@example.test','Renamer','x')`, f.owner)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspaces (id,name,slug,owner_id) VALUES ($1,'Rename WS','rename-ws',$2)`, f.workspace, f.owner)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role) VALUES ($1,$2,'owner')`, f.workspace, f.owner)
	require.NoError(t, err)
	for _, tm := range []struct {
		id  uuid.UUID
		key string
	}{{f.teamID, "ENG"}, {f.otherTeam, "OTH"}} {
		_, err = db.ExecContext(ctx, `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,$3,$4)`, tm.id, f.workspace, "Team "+tm.key, tm.key)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO team_statuses (id,team_id,name,slug,category,position,is_default) VALUES ($1,$2,'Todo','todo','unstarted',0,true)`, f.statusTodo, f.teamID)
	require.NoError(t, err)

	insertIssue := func(teamID uuid.UUID, number int, identifier string) {
		_, err := db.ExecContext(ctx, `INSERT INTO issues (id,workspace_id,team_id,number,identifier_text,title,status,status_id,priority,creator_id)
			VALUES ($1,$2,$3,$4,$5,$6,'todo',$7,0,$8)`, uuid.New(), f.workspace, teamID, number, identifier, "issue "+identifier, f.statusTodo, f.owner)
		require.NoError(t, err)
	}
	insertIssue(f.teamID, 1, "ENG-1")
	insertIssue(f.teamID, 2, "ENG-2")
	insertIssue(f.teamID, 3, "ENG-3")
	insertIssue(f.otherTeam, 1, "OTH-1")
	if blockCollision {
		insertIssue(f.otherTeam, 2, "NEW-2")
	}
	return f
}

func (e *teamRenameEnv) identifiers(t *testing.T, teamID uuid.UUID) []string {
	t.Helper()
	var ids []string
	err := e.db.Select(&ids, `SELECT identifier_text FROM issues WHERE team_id = $1 ORDER BY number`, teamID)
	require.NoError(t, err)
	return ids
}

// Renaming the key rewrites every ticket identifier, atomically.
func TestTeamRename_RewritesAllIssueIdentifiers(t *testing.T) {
	env := setupTeamRename(t)
	f := seedTwoTeams(t, env.db, false)
	ctx := context.Background()

	team, err := env.svc.Update(ctx, f.teamID, dto.UpdateTeamRequest{
		Name: ptr("Renamed Team"),
		Key:  ptr("new"),
	})
	require.NoError(t, err)
	assert.Equal(t, "NEW", team.Key)
	assert.Equal(t, "Renamed Team", team.Name)

	assert.Equal(t, []string{"NEW-1", "NEW-2", "NEW-3"}, env.identifiers(t, f.teamID))
	// The other team is untouched.
	assert.Equal(t, []string{"OTH-1"}, env.identifiers(t, f.otherTeam))

	// Old identifiers no longer resolve; new ones do.
	var resolved uuid.UUID
	err = env.db.Get(&resolved, `SELECT id FROM issues WHERE workspace_id = $1 AND identifier_text = 'ENG-1'`, f.workspace)
	assert.Error(t, err)
	err = env.db.Get(&resolved, `SELECT id FROM issues WHERE workspace_id = $1 AND identifier_text = 'NEW-1'`, f.workspace)
	assert.NoError(t, err)

	// The team key itself persisted.
	var key string
	require.NoError(t, env.db.Get(&key, `SELECT key FROM teams WHERE id = $1`, f.teamID))
	assert.Equal(t, "NEW", key)
}

// A collision with another team's identifiers rejects the whole change:
// neither the team (name) nor any ticket is modified.
func TestTeamRename_CollisionChangesNothing(t *testing.T) {
	env := setupTeamRename(t)
	f := seedTwoTeams(t, env.db, true) // other team owns NEW-2
	ctx := context.Background()

	_, err := env.svc.Update(ctx, f.teamID, dto.UpdateTeamRequest{
		Name: ptr("Should Not Apply"),
		Key:  ptr("NEW"),
	})
	assert.ErrorIs(t, err, repository.ErrTeamKeyCollision)

	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, env.identifiers(t, f.teamID))
	var name string
	require.NoError(t, env.db.Get(&name, `SELECT name FROM teams WHERE id = $1`, f.teamID))
	assert.Equal(t, "Team ENG", name)
}

// Renaming only the name never touches identifiers; sending the current key
// is a harmless no-op for identifiers.
func TestTeamRename_NameOnlyAndSameKeyNoOp(t *testing.T) {
	env := setupTeamRename(t)
	f := seedTwoTeams(t, env.db, false)
	ctx := context.Background()

	_, err := env.svc.Update(ctx, f.teamID, dto.UpdateTeamRequest{Name: ptr("Just A Name")})
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, env.identifiers(t, f.teamID))

	_, err = env.svc.Update(ctx, f.teamID, dto.UpdateTeamRequest{Key: ptr("ENG")})
	require.NoError(t, err)
	assert.Equal(t, []string{"ENG-1", "ENG-2", "ENG-3"}, env.identifiers(t, f.teamID))
}

// After a rename, newly created issues continue the numbering under the new
// key.
func TestTeamRename_NewIssuesContinueNumbering(t *testing.T) {
	env := setupTeamRename(t)
	f := seedTwoTeams(t, env.db, false)
	ctx := context.Background()

	_, err := env.svc.Update(ctx, f.teamID, dto.UpdateTeamRequest{Key: ptr("NEW")})
	require.NoError(t, err)

	var next int
	require.NoError(t, env.db.Get(&next, `SELECT COALESCE(MAX(number), 0) + 1 FROM issues WHERE team_id = $1`, f.teamID))
	assert.Equal(t, 4, next)
}
