package service

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/internal/dto"
	"github.com/ku/ku-backend/internal/repository"
	"github.com/ku/ku-backend/pkg/storage"
	"github.com/stretchr/testify/require"
)

func workspaceIdentifierHistoryTransferDatabase(t *testing.T) *sqlx.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	admin, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	name := "ku_identifier_transfer_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
	})
	parsed, err := url.Parse(databaseURL)
	require.NoError(t, err)
	parsed.Path = "/" + name
	parsed.RawPath = ""
	migrator, err := migrate.New("file://../../migrations", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = migrator.Close() })
	require.NoError(t, migrator.Up())
	db, err := sqlx.ConnectContext(t.Context(), "pgx", parsed.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestWorkspaceIdentifierHistoryTransferPostgres(t *testing.T) {
	db := workspaceIdentifierHistoryTransferDatabase(t)
	ctx := t.Context()
	userID, workspaceID, teamID, issueID, otherIssueID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	foreignWorkspaceID, foreignTeamID := uuid.New(), uuid.New()
	statusID, foreignStatusID := uuid.New(), uuid.New()
	githubRepoID, githubPRID := uuid.New(), uuid.New()
	createdAt := time.Date(2025, time.March, 4, 12, 13, 14, 0, time.UTC)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users(id,email,name,password_hash) VALUES($1,'identifier-transfer@example.test','Transfer owner','test')`, []any{userID}},
		{`INSERT INTO workspaces(id,name,slug,owner_id) VALUES($1,'Source','identifier-source',$2),($3,'Foreign','identifier-foreign',$2)`, []any{workspaceID, userID, foreignWorkspaceID}},
		{`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES($1,$2,'owner')`, []any{workspaceID, userID}},
		{`INSERT INTO teams(id,workspace_id,name,key) VALUES($1,$2,'Source team','NEW'),($3,$4,'Foreign team','OLD')`, []any{teamID, workspaceID, foreignTeamID, foreignWorkspaceID}},
		{`INSERT INTO team_statuses(id,team_id,name,slug,category) VALUES($1,$2,'Todo','todo','unstarted'),($3,$4,'Todo','todo','unstarted')`, []any{statusID, teamID, foreignStatusID, foreignTeamID}},
		{`INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES($1,$2,$3,1,'OLD-1','Renamed issue',$4,$6),($5,$2,$3,2,'NEW-2','Second issue',$4,$6)`, []any{issueID, workspaceID, teamID, userID, otherIssueID, statusID}},
		{`UPDATE issues SET identifier_text='NEW-1' WHERE id=$1`, []any{issueID}},
		{`UPDATE issues SET identifier_text=$2 WHERE id=$1`, []any{otherIssueID, otherIssueID.String()}},
		// Exact aliases are domain data, even when they resemble a remapped ID.
		{`INSERT INTO issue_identifier_history(workspace_id,identifier_text,issue_id) VALUES($1,$2,$3)`, []any{workspaceID, issueID.String(), issueID}},
		{`UPDATE issue_identifier_history SET created_at=$1 WHERE workspace_id=$2`, []any{createdAt, workspaceID}},
		{`INSERT INTO issues(id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES(gen_random_uuid(),$1,$2,1,'OLD-1','Foreign issue',$3,$4)`, []any{foreignWorkspaceID, foreignTeamID, userID, foreignStatusID}},
		{`INSERT INTO github_repos(id,workspace_id,github_repo_id,full_name,instance_url) VALUES($1,$2,7,'transfer/repo','https://git.example.test')`, []any{githubRepoID, workspaceID}},
		{`INSERT INTO github_pull_requests(id,workspace_id,github_repo_id,number,title,body,state,author_login,html_url) VALUES($1,$2,$3,7,'UUID-only body',$4,'open','tester','https://git.example.test/transfer/repo/pulls/7')`, []any{githubPRID, workspaceID, githubRepoID, issueID.String()}},
		{`INSERT INTO github_pull_request_issues(pr_id,issue_id) VALUES($1,$2)`, []any{githubPRID, issueID}},
	} {
		_, err := db.ExecContext(ctx, statement.query, statement.args...)
		require.NoError(t, err)
	}
	backend, err := storage.NewLocalBackend(t.TempDir(), "")
	require.NoError(t, err)
	transfer := NewWorkspaceTransferService(repository.NewWorkspaceTransferRepository(db), backend)
	archive, err := transfer.Export(ctx, &domain.Workspace{ID: workspaceID, Name: "Source", Slug: "identifier-source", OwnerID: userID}, userID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Remove(archive.Path) })
	parsed, err := parseWorkspaceArchive(archive.Path)
	require.NoError(t, err)
	require.Equal(t, 2, parsed.manifest.Version)
	require.Equal(t, 5, parsed.manifest.Counts["issue_identifier_history"])
	require.Len(t, parsed.data.Tables["issue_identifier_history"], 5)
	for _, row := range parsed.data.Tables["issue_identifier_history"] {
		require.Equal(t, workspaceID.String(), transferString(row["workspace_id"]), "foreign aliases must not enter the archive")
	}
	require.NoError(t, parsed.zip.Close())

	t.Run("round trip retains exact aliases owners and timestamps", func(t *testing.T) {
		result, err := transfer.Import(t.Context(), archive.Path, "Target", "identifier-target", userID)
		require.NoError(t, err)
		require.Equal(t, 5, result.Counts["issue_identifier_history"])
		targetID := uuid.MustParse(result.ID)
		var importedIssueID, importedOtherIssueID uuid.UUID
		require.NoError(t, db.GetContext(t.Context(), &importedIssueID, `SELECT id FROM issues WHERE workspace_id=$1 AND identifier_text='NEW-1'`, targetID))
		require.NoError(t, db.GetContext(t.Context(), &importedOtherIssueID, `SELECT id FROM issues WHERE workspace_id=$1 AND identifier_text=$2`, targetID, otherIssueID.String()))
		require.NotEqual(t, issueID, importedIssueID)
		require.NotEqual(t, otherIssueID, importedOtherIssueID)
		var importedPRID, importedPRRepoID, importedRepoID, linkedIssueID uuid.UUID
		var importedBody string
		require.NoError(t, db.QueryRowContext(t.Context(), `SELECT id,github_repo_id,body FROM github_pull_requests WHERE workspace_id=$1 AND number=7`, targetID).Scan(&importedPRID, &importedPRRepoID, &importedBody))
		require.NoError(t, db.GetContext(t.Context(), &importedRepoID, `SELECT id FROM github_repos WHERE workspace_id=$1 AND full_name='transfer/repo'`, targetID))
		require.NoError(t, db.GetContext(t.Context(), &linkedIssueID, `SELECT issue_id FROM github_pull_request_issues WHERE pr_id=$1`, importedPRID))
		require.NotEqual(t, githubPRID, importedPRID)
		require.NotEqual(t, githubRepoID, importedRepoID)
		require.Equal(t, importedRepoID, importedPRRepoID)
		require.Equal(t, importedIssueID, linkedIssueID)
		require.Equal(t, issueID.String(), importedBody, "external PR body must remain verbatim while references are remapped")
		var history []struct {
			Identifier string    `db:"identifier_text"`
			IssueID    uuid.UUID `db:"issue_id"`
			CreatedAt  time.Time `db:"created_at"`
		}
		require.NoError(t, db.SelectContext(t.Context(), &history, `SELECT identifier_text,issue_id,created_at FROM issue_identifier_history WHERE workspace_id=$1`, targetID))
		require.Len(t, history, 5, "current aliases registered by the insert trigger must import idempotently")
		owners := make(map[string]uuid.UUID, len(history))
		for _, row := range history {
			owners[row.Identifier] = row.IssueID
			require.True(t, createdAt.Equal(row.CreatedAt), "archived alias timestamp must survive the generated current row")
		}
		require.Equal(t, map[string]uuid.UUID{"OLD-1": importedIssueID, "NEW-1": importedIssueID, "NEW-2": importedOtherIssueID, issueID.String(): importedIssueID, otherIssueID.String(): importedOtherIssueID}, owners)
	})

	t.Run("absent PR body uses the database default", func(t *testing.T) {
		withoutBody := mutateWorkspaceArchive(t, archive.Path, func(_ *dto.WorkspaceExportManifest, data *dto.WorkspaceTransferData) {
			delete(data.Tables["github_pull_requests"][0], "body")
		})
		result, err := transfer.Import(t.Context(), withoutBody, "Without body", "identifier-without-body", userID)
		require.NoError(t, err)
		var importedBody string
		require.NoError(t, db.GetContext(t.Context(), &importedBody, `SELECT body FROM github_pull_requests WHERE workspace_id=$1 AND number=7`, result.ID))
		require.Empty(t, importedBody)
	})

	for _, collision := range []struct {
		name       string
		identifier string
	}{
		{"current identifier", "NEW-1"},
		{"historical identifier", "OLD-1"},
	} {
		t.Run("rejects another owner of "+collision.name, func(t *testing.T) {
			mutated := mutateWorkspaceArchive(t, archive.Path, func(manifest *dto.WorkspaceExportManifest, data *dto.WorkspaceTransferData) {
				data.Tables["issue_identifier_history"] = append(data.Tables["issue_identifier_history"], dto.WorkspaceTransferRow{
					"workspace_id": workspaceID.String(), "identifier_text": collision.identifier, "issue_id": otherIssueID.String(), "created_at": createdAt,
				})
				manifest.Counts["issue_identifier_history"]++
			})
			slug := "collision-" + strings.ReplaceAll(uuid.NewString(), "-", "")
			_, err := transfer.Import(t.Context(), mutated, "Collision", slug, userID)
			require.ErrorIs(t, err, ErrInvalidWorkspaceArchive)
			require.ErrorContains(t, err, "issue identifier belongs to a different imported issue")
			var count int
			require.NoError(t, db.GetContext(t.Context(), &count, `SELECT COUNT(*) FROM workspaces WHERE slug=$1`, slug))
			require.Zero(t, count, "an alias conflict must roll back the entire import")
		})
	}

	t.Run("format 1 must be re-exported", func(t *testing.T) {
		oldArchive := mutateWorkspaceArchive(t, archive.Path, func(manifest *dto.WorkspaceExportManifest, _ *dto.WorkspaceTransferData) {
			manifest.Version = 1
		})
		_, err := parseWorkspaceArchive(oldArchive)
		require.ErrorIs(t, err, ErrUnsupportedArchive)
	})
}
