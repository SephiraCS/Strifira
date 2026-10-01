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
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestScreenShareSessionMigrationAndConcurrentCaptureConstraint(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	ctx := t.Context()
	admin, err := sql.Open("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	databaseName := "ku_share_sessions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`" WITH (FORCE)`)
	})
	testURL := databaseURLWithName(t, databaseURL, databaseName)
	migrator, err := migrate.New("file://.", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Migrate(48))
	db, err := sqlx.Connect("pgx", testURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	userID, workspaceID := uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO users(id,email,name,password_hash) VALUES($1,'share-sessions@example.test','Presenter','test')`, userID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Screen Share Sessions','share-sessions',$2)`, workspaceID, userID)
	require.NoError(t, err)
	oldID, currentID := uuid.New(), uuid.New()
	_, err = db.ExecContext(ctx, `INSERT INTO screen_shares(id,workspace_id,started_by,status,created_at) VALUES($1,$3,$4,'active',NOW()-INTERVAL '1 minute'),($2,$3,$4,'active',NOW())`, oldID, currentID, workspaceID, userID)
	require.NoError(t, err)
	require.NoError(t, migrator.Steps(1))
	repo := repository.NewScreenShareRepository(db)
	oldShare, err := repo.GetByID(ctx, oldID)
	require.NoError(t, err)
	require.Equal(t, domain.ScreenShareStatusEnded, oldShare.Status)
	require.NotNil(t, oldShare.EndedAt)
	currentShare, err := repo.GetByID(ctx, currentID)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, currentShare.PresenterToken)
	newShare := &domain.ScreenShare{ID: uuid.New(), WorkspaceID: workspaceID, StartedBy: userID, PresenterToken: uuid.New(), Status: domain.ScreenShareStatusActive}
	require.ErrorIs(t, repo.Create(ctx, newShare), repository.ErrScreenShareAlreadyActive, "two tabs racing to start cannot create two captures")
	require.NoError(t, repo.MarkEnded(ctx, currentID))
	require.NoError(t, repo.Create(ctx, newShare))
	created, err := repo.GetByID(ctx, newShare.ID)
	require.NoError(t, err)
	require.Equal(t, newShare.PresenterToken, created.PresenterToken)
	require.NoError(t, migrator.Steps(-1))
	var tokenColumns int
	require.NoError(t, db.GetContext(ctx, &tokenColumns, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name='screen_shares' AND column_name='presenter_token'`))
	require.Zero(t, tokenColumns)
}
