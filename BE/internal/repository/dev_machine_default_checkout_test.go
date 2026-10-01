package repository

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCreateCheckoutDatabasePreservesDefaultRepositoryAndPrimaryIssue(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.Connect("pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	f := newEnvironmentRaceFixture(t, db)
	teamID, statusID := uuid.New(), uuid.New()
	_, err = db.Exec(`INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,'Default Checkout','DEF')`, teamID, f.workspaceID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO team_statuses (id,team_id,name,slug,category,is_default)
		VALUES ($1,$2,'Todo','todo','unstarted',TRUE)`, statusID, teamID)
	require.NoError(t, err)
	repoIDs := []uuid.UUID{uuid.New(), uuid.New()}
	for index, fullName := range []string{"ku/default", "ku/other"} {
		_, err = db.Exec(`INSERT INTO forgejo_repos (id,workspace_id,forgejo_repo_id,full_name,instance_url,default_branch)
			VALUES ($1,$2,$3,$4,'https://git.example.com','main')`, repoIDs[index], f.workspaceID, time.Now().UnixNano(), fullName)
		require.NoError(t, err)
	}
	issueNumber := 0
	createIssue := func() uuid.UUID {
		issueNumber++
		issueID := uuid.New()
		_, err := db.Exec(`INSERT INTO issues (id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id)
			VALUES ($1,$2,$3,$4,$5,'Default checkout test',$6,$7)`, issueID, f.workspaceID, teamID, issueNumber,
			fmt.Sprintf("DEF-%d", issueNumber), f.userID, statusID)
		require.NoError(t, err)
		return issueID
	}
	createCheckout := func(machineID, repoID uuid.UUID, fullName string) (*domain.DevMachineCheckout, *domain.DevMachineOperation) {
		issueID := createIssue()
		checkoutID := uuid.New()
		checkout := &domain.DevMachineCheckout{ID: checkoutID, WorkspaceID: f.workspaceID, MachineID: machineID,
			IssueID: issueID, ForgejoRepoID: repoID, RepositoryFullName: fullName, RepositoryURL: "https://git.example.com/" + fullName,
			BaseBranch: "release", WorkingBranch: "ku/issue-" + checkoutID.String(), WorkspacePath: "/workspace/tasks/" + checkoutID.String(), Status: "queued"}
		operation := &domain.DevMachineOperation{ID: uuid.New(), WorkspaceID: f.workspaceID, MachineID: machineID,
			Action: domain.DevMachineOpCheckoutIssue, Status: domain.DevMachineOpStatusPending, Generation: 1,
			IdempotencyKey: "checkout:" + checkoutID.String(), RequestedByUserID: &f.userID, MaxAttempts: 3}
		return checkout, operation
	}
	assertDefault := func(t *testing.T, before, after *domain.DevMachine) {
		t.Helper()
		require.Equal(t, before.IssueID, after.IssueID)
		require.Equal(t, before.RepoURL, after.RepoURL)
		require.Equal(t, before.RepoProvider, after.RepoProvider)
		require.Equal(t, before.RepoOwner, after.RepoOwner)
		require.Equal(t, before.RepoName, after.RepoName)
		require.Equal(t, before.BaseBranch, after.BaseBranch)
		require.Equal(t, before.WorkingBranch, after.WorkingBranch)
	}
	for _, mode := range []string{"configured-default", "generic", "disconnected-default", "concurrent-affinity"} {
		t.Run(mode, func(t *testing.T) {
			machineID := f.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
			_, err := db.Exec(`UPDATE dev_machines SET repo_provider='forgejo' WHERE id=$1`, machineID)
			require.NoError(t, err)
			if mode == "configured-default" || mode == "disconnected-default" {
				primaryIssueID := createIssue()
				_, err = db.Exec(`UPDATE dev_machines SET repository_affinity_id=$2,issue_id=$3,
					repo_url='https://git.example.com/ku/default',repo_owner='ku',repo_name='default',
					base_branch='main',working_branch='owner/default' WHERE id=$1`, machineID, repoIDs[0], primaryIssueID)
				require.NoError(t, err)
				if mode == "disconnected-default" {
					_, err = db.Exec(`UPDATE dev_machines SET repository_affinity_id=NULL WHERE id=$1`, machineID)
					require.NoError(t, err)
				}
			}
			before, err := f.repository.GetMachine(t.Context(), f.workspaceID, machineID)
			require.NoError(t, err)
			require.NotNil(t, before)
			if mode == "concurrent-affinity" {
				first, firstOperation := createCheckout(machineID, repoIDs[0], "ku/default")
				second, secondOperation := createCheckout(machineID, repoIDs[1], "ku/other")
				firstErr, secondErr := runEnvironmentRace(
					func() error { return f.repository.CreateCheckout(t.Context(), first, firstOperation) },
					func() error { return f.repository.CreateCheckout(t.Context(), second, secondOperation) },
				)
				require.NotEqual(t, firstErr == nil, secondErr == nil)
				if firstErr != nil {
					require.ErrorIs(t, firstErr, ErrCheckoutMachineConflict)
				} else {
					require.ErrorIs(t, secondErr, ErrCheckoutMachineConflict)
				}
				checkouts, err := f.repository.ListCheckouts(t.Context(), f.workspaceID, machineID)
				require.NoError(t, err)
				require.Len(t, checkouts, 1)
				after, err := f.repository.GetMachine(t.Context(), f.workspaceID, machineID)
				require.NoError(t, err)
				assertDefault(t, before, after)
				require.Equal(t, checkouts[0].ForgejoRepoID, *after.RepositoryAffinityID)
				var operations int
				require.NoError(t, db.Get(&operations, `SELECT COUNT(*) FROM dev_machine_operations WHERE machine_id=$1`, machineID))
				require.Equal(t, 1, operations, "the losing affinity claim must roll back its checkout and operation")
				return
			}
			if mode == "disconnected-default" {
				checkout, operation := createCheckout(machineID, repoIDs[1], "ku/other")
				require.ErrorIs(t, f.repository.CreateCheckout(t.Context(), checkout, operation), ErrCheckoutMachineConflict)
				checkouts, err := f.repository.ListCheckouts(t.Context(), f.workspaceID, machineID)
				require.NoError(t, err)
				require.Empty(t, checkouts)
			} else {
				for range 2 {
					checkout, operation := createCheckout(machineID, repoIDs[0], "ku/default")
					require.NoError(t, f.repository.CreateCheckout(t.Context(), checkout, operation))
					require.Equal(t, checkout.ID, *operation.CheckoutID)
					persisted, err := f.repository.GetCheckout(t.Context(), f.workspaceID, machineID, checkout.ID)
					require.NoError(t, err)
					require.Equal(t, checkout.WorkingBranch, persisted.WorkingBranch)
					require.Equal(t, checkout.BaseBranch, persisted.BaseBranch)
				}
				checkouts, err := f.repository.ListCheckouts(t.Context(), f.workspaceID, machineID)
				require.NoError(t, err)
				require.Len(t, checkouts, 2)
			}
			after, err := f.repository.GetMachine(t.Context(), f.workspaceID, machineID)
			require.NoError(t, err)
			assertDefault(t, before, after)
			if mode == "disconnected-default" {
				require.Nil(t, after.RepositoryAffinityID)
			} else {
				require.Equal(t, repoIDs[0], *after.RepositoryAffinityID)
			}
		})
	}
}
