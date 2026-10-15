package repository

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func identifierHistoryTestDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "strifira_identifier_history_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	require.NoError(t, migrator.Migrate(52))
	db, err := sqlx.ConnectContext(t.Context(), "pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type identifierHistoryFixture struct {
	db                          *sqlx.DB
	owner, workspaceID, issueID uuid.UUID
	team                        *domain.Team
}

func newIdentifierHistoryFixture(t *testing.T, db *sqlx.DB) identifierHistoryFixture {
	t.Helper()
	f := identifierHistoryFixture{db: db, owner: uuid.New(), workspaceID: uuid.New(), issueID: uuid.New()}
	suffix := strings.ReplaceAll(f.workspaceID.String(), "-", "")
	f.team = &domain.Team{ID: uuid.New(), WorkspaceID: f.workspaceID, Name: "History team", Key: "DSH"}
	_, err := db.ExecContext(t.Context(), `INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,'History','test')`, f.owner, suffix+"@example.test")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'History',$2,$3)`, f.workspaceID, suffix, f.owner)
	require.NoError(t, err)
	require.NoError(t, NewTeamRepository(db).Create(t.Context(), f.team))
	require.NoError(t, f.insertIssue(t.Context(), f.issueID, f.team.ID, 1, "DSH-1"))
	return f
}

func (f identifierHistoryFixture) insertIssue(ctx context.Context, issueID, teamID uuid.UUID, number int, identifier string) error {
	var statusID uuid.UUID
	err := f.db.QueryRowContext(ctx, `INSERT INTO team_statuses(team_id,name,slug,category) VALUES($1,'Todo','todo','unstarted')
		ON CONFLICT(team_id,slug) DO UPDATE SET name=EXCLUDED.name RETURNING id`, teamID).Scan(&statusID)
	if err != nil {
		return err
	}
	_, err = f.db.ExecContext(ctx, `INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id)
		VALUES($1,$2,$3,$4,$5,'History issue',$6,$7)`, issueID, f.workspaceID, teamID, number, identifier, f.owner, statusID)
	return err
}

func (f identifierHistoryFixture) rename(t *testing.T, key string) {
	t.Helper()
	count, err := NewTeamRepository(f.db).UpdateAndRenameKey(t.Context(), f.team, key)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	f.team.Key = key
}

func (f identifierHistoryFixture) resolve(t *testing.T, identifier, canonical string) {
	t.Helper()
	issue, err := NewIssueRepository(f.db).GetByIdentifier(t.Context(), f.workspaceID, identifier)
	require.NoError(t, err)
	require.NotNil(t, issue, identifier)
	require.Equal(t, f.issueID, issue.ID)
	require.Equal(t, canonical, issue.Identifier)
}

func TestIssueIdentifierHistoryPostgres(t *testing.T) {
	db := identifierHistoryTestDatabase(t)
	t.Run("repeated renames preserve exact stable identity and allow rename back", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		f.rename(t, "TS")
		f.resolve(t, "DSH-1", "TS-1")
		f.resolve(t, "TS-1", "TS-1")
		f.rename(t, "STRIFIRA")
		for _, identifier := range []string{"DSH-1", "TS-1", "STRIFIRA-1"} {
			f.resolve(t, identifier, "STRIFIRA-1")
		}
		f.rename(t, "DSH")
		for _, identifier := range []string{"DSH-1", "TS-1", "STRIFIRA-1"} {
			f.resolve(t, identifier, "DSH-1")
		}
		var count int
		require.NoError(t, db.GetContext(t.Context(), &count, `SELECT COUNT(*) FROM issue_identifier_history WHERE issue_id=$1`, f.issueID))
		require.Equal(t, 3, count, "rename-back reuses its own alias")
	})
	t.Run("workspace isolation and no guessed prefix or number lookup", func(t *testing.T) {
		f, other := newIdentifierHistoryFixture(t, db), newIdentifierHistoryFixture(t, db)
		f.rename(t, "TS")
		f.resolve(t, "DSH-1", "TS-1")
		other.resolve(t, "DSH-1", "DSH-1")
		for _, identifier := range []string{"OLD-1", "DSH-2", "TS-01", "dsh-1"} {
			issue, err := NewIssueRepository(db).GetByIdentifier(t.Context(), f.workspaceID, identifier)
			require.NoError(t, err)
			require.Nil(t, issue, identifier)
		}
		issue, err := NewIssueRepository(db).GetByIdentifier(t.Context(), other.workspaceID, "TS-1")
		require.NoError(t, err)
		require.Nil(t, issue)
		_, err = db.ExecContext(t.Context(), `INSERT INTO issue_identifier_history(workspace_id,identifier_text,issue_id) VALUES($1,'OTHER-1',$2)`, other.workspaceID, f.issueID)
		require.Error(t, err, "history cannot point across workspaces")
	})
	t.Run("historical prefix reuse and same-team destination collision fail atomically", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		f.rename(t, "TS")
		teamRepo := NewTeamRepository(db)
		other := &domain.Team{ID: uuid.New(), WorkspaceID: f.workspaceID, Name: "Other", Key: "OTH"}
		require.NoError(t, teamRepo.Create(t.Context(), other))
		blocked := &domain.Team{ID: uuid.New(), WorkspaceID: f.workspaceID, Name: "Cannot reuse", Key: "DSH"}
		require.ErrorIs(t, teamRepo.Create(t.Context(), blocked), ErrTeamKeyCollision)
		other.Name = "Must not persist"
		_, err := teamRepo.UpdateAndRenameKey(t.Context(), other, "DSH")
		require.ErrorIs(t, err, ErrTeamKeyCollision, "prefix is reserved even when issue numbers would not overlap")
		persisted, err := teamRepo.GetByID(t.Context(), other.ID)
		require.NoError(t, err)
		require.Equal(t, "Other", persisted.Name)
		require.Equal(t, "OTH", persisted.Key)
		_, err = db.ExecContext(t.Context(), `INSERT INTO issue_identifier_history(workspace_id,identifier_text,issue_id) VALUES($1,'NEW-2',$2)`, f.workspaceID, f.issueID)
		require.NoError(t, err)
		require.NoError(t, f.insertIssue(t.Context(), uuid.New(), f.team.ID, 2, "TS-2"))
		_, err = teamRepo.UpdateAndRenameKey(t.Context(), f.team, "NEW")
		require.ErrorIs(t, err, ErrTeamKeyCollision, "an alias cannot move to a different issue in the same team")
		f.resolve(t, "DSH-1", "TS-1")
		f.resolve(t, "NEW-2", "TS-1")
	})
	t.Run("raw insert and update cannot steal an alias", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		f.rename(t, "TS")
		otherID := uuid.New()
		require.Error(t, f.insertIssue(t.Context(), otherID, f.team.ID, 2, "DSH-1"))
		other, err := NewIssueRepository(db).GetByID(t.Context(), otherID)
		require.NoError(t, err)
		require.Nil(t, other, "failed history recording rolls back issue insert")
		require.NoError(t, f.insertIssue(t.Context(), otherID, f.team.ID, 2, "TS-2"))
		_, err = db.ExecContext(t.Context(), `UPDATE issues SET identifier_text='DSH-1' WHERE id=$1`, otherID)
		require.Error(t, err)
		other, err = NewIssueRepository(db).GetByID(t.Context(), otherID)
		require.NoError(t, err)
		require.Equal(t, "TS-2", other.Identifier)
		f.resolve(t, "DSH-1", "TS-1")
	})
	t.Run("rolled back updates do not leave aliases", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		tx, err := db.BeginTxx(t.Context(), nil)
		require.NoError(t, err)
		defer tx.Rollback()
		_, err = tx.ExecContext(t.Context(), `UPDATE issues SET identifier_text='ROLLBACK-1' WHERE id=$1`, f.issueID)
		require.NoError(t, err)
		require.NoError(t, tx.Rollback())
		issue, err := NewIssueRepository(db).GetByIdentifier(t.Context(), f.workspaceID, "ROLLBACK-1")
		require.NoError(t, err)
		require.Nil(t, issue)
		f.resolve(t, "DSH-1", "DSH-1")
	})
	t.Run("hard deletion releases history and permits explicit reuse", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		f.rename(t, "TS")
		_, err := db.ExecContext(t.Context(), `DELETE FROM issues WHERE id=$1`, f.issueID)
		require.NoError(t, err)
		for _, identifier := range []string{"DSH-1", "TS-1"} {
			issue, err := NewIssueRepository(db).GetByIdentifier(t.Context(), f.workspaceID, identifier)
			require.NoError(t, err)
			require.Nil(t, issue)
		}
		var count int
		require.NoError(t, db.GetContext(t.Context(), &count, `SELECT COUNT(*) FROM issue_identifier_history WHERE issue_id=$1`, f.issueID))
		require.Zero(t, count)
		team := &domain.Team{ID: uuid.New(), WorkspaceID: f.workspaceID, Name: "New DSH", Key: "DSH"}
		require.NoError(t, NewTeamRepository(db).Create(t.Context(), team))
		f.issueID = uuid.New()
		require.NoError(t, f.insertIssue(t.Context(), f.issueID, team.ID, 1, "DSH-1"))
		f.resolve(t, "DSH-1", "DSH-1")
	})
	t.Run("concurrent teams cannot both claim one key", func(t *testing.T) {
		f := newIdentifierHistoryFixture(t, db)
		results := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				results <- NewTeamRepository(db).Create(t.Context(), &domain.Team{
					ID: uuid.New(), WorkspaceID: f.workspaceID, Name: "Concurrent", Key: "NEW",
				})
			})
		}
		wg.Wait()
		close(results)
		created := 0
		for err := range results {
			if err == nil {
				created++
			} else {
				require.ErrorIs(t, err, ErrTeamKeyCollision)
			}
		}
		require.Equal(t, 1, created)
	})
}
