package repository

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAgentRunRepositorySelectionDatabaseIsImmutableAcrossRestartAndMachineChanges(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fixture := newEnvironmentRaceFixture(t, db)
	repo := NewDevMachineRepository(db)
	for _, mode := range []string{"default", "explicit", "wrong-machine", "wrong-workspace", "wrong-checkout", "wrong-default-path", "terminal", "concurrent-different-selection"} {
		t.Run(mode, func(t *testing.T) {
			machineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
			runID := uuid.New()
			checkout := &domain.DevMachineCheckout{ID: machineID, MachineID: machineID, WorkspaceID: fixture.workspaceID,
				RepositoryFullName: "ku/api", RepositoryURL: "https://git.example.com/ku/api", BaseBranch: "main", WorkingBranch: "ku/task", WorkspacePath: "/workspace/repository"}
			_, err := db.ExecContext(t.Context(), `INSERT INTO dev_machine_agent_runs
                (id,machine_id,workspace_id,requested_by_user_id,provider_id,mode,status,prompt,command_argv,max_runtime_seconds)
                VALUES ($1,$2,$3,$4,'opencode','autonomous','running','repository selection','["true"]',120)`, runID, machineID, fixture.workspaceID, fixture.userID)
			require.NoError(t, err)
			switch mode {
			case "explicit":
				// An existing issue checkout is the explicit run identity.
				checkout.ID = uuid.New()
				teamID, statusID, issueID, repositoryID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
				_, err = db.ExecContext(t.Context(), `INSERT INTO teams (id,workspace_id,name,key) VALUES ($1,$2,'Repository selection','PIN')`, teamID, fixture.workspaceID)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO team_statuses (id,team_id,name,slug,category,is_default) VALUES ($1,$2,'Todo','todo','unstarted',TRUE)`, statusID, teamID)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO issues (id,workspace_id,team_id,number,identifier_text,title,creator_id,status_id) VALUES ($1,$2,$3,1,'PIN-1','Repository selection',$4,$5)`, issueID, fixture.workspaceID, teamID, fixture.userID, statusID)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO github_repos (id,workspace_id,github_repo_id,full_name,instance_url,default_branch) VALUES ($1,$2,$3,'ku/api','https://git.example.com','main')`, repositoryID, fixture.workspaceID, time.Now().UnixNano())
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO dev_machine_checkouts (id,workspace_id,machine_id,issue_id,github_repo_id,repository_full_name,base_branch,working_branch,workspace_path,status) VALUES ($1,$2,$3,$4,$5,'ku/api','main','ku/task','/workspace/tasks/issue','ready')`, checkout.ID, fixture.workspaceID, machineID, issueID, repositoryID)
				require.NoError(t, err)
				checkout.WorkspacePath = "/workspace/tasks/issue"
				_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_runs SET checkout_id=$2 WHERE id=$1`, runID, checkout.ID)
				require.NoError(t, err)
			case "wrong-machine":
				checkout.MachineID = uuid.New()
			case "wrong-workspace":
				checkout.WorkspaceID = uuid.New()
			case "wrong-checkout":
				checkout.ID = uuid.New()
			case "wrong-default-path":
				checkout.WorkspacePath = "/workspace/tasks/foreign"
			case "terminal":
				_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_runs SET status='timeout' WHERE id=$1`, runID)
				require.NoError(t, err)
			}
			if mode == "concurrent-different-selection" {
				other := *checkout
				other.WorkingBranch = "other-task"
				results := make(chan error, 2)
				var wg sync.WaitGroup
				for _, selection := range []*domain.DevMachineCheckout{checkout, &other} {
					wg.Go(func() { results <- repo.PinAgentRunRepository(t.Context(), runID, selection) })
				}
				wg.Wait()
				close(results)
				successful := 0
				for err := range results {
					if err == nil {
						successful++
					}
				}
				require.Equal(t, 1, successful, "only the first immutable selection can win")
				selected, err := repo.GetAgentRunRepository(t.Context(), runID)
				require.NoError(t, err)
				require.Contains(t, []string{"ku/task", "other-task"}, selected.WorkingBranch)
				return
			}
			err = repo.PinAgentRunRepository(t.Context(), runID, checkout)
			if mode != "default" && mode != "explicit" {
				require.Error(t, err)
				selected, err := repo.GetAgentRunRepository(t.Context(), runID)
				require.NoError(t, err)
				require.Nil(t, selected)
				return
			}
			require.NoError(t, err)
			require.NoError(t, repo.PinAgentRunRepository(t.Context(), runID, checkout), "an identical replay is idempotent")
			_, err = db.ExecContext(t.Context(), `UPDATE dev_machines SET repo_owner='later',repo_name='repo',base_branch='later-base',working_branch='later-issue' WHERE id=$1`, machineID)
			require.NoError(t, err)
			restarted := NewDevMachineRepository(db)
			selected, err := restarted.GetAgentRunRepository(t.Context(), runID)
			require.NoError(t, err)
			require.Equal(t, checkout, selected)
			changed := *checkout
			changed.WorkingBranch = "replacement"
			require.Error(t, restarted.PinAgentRunRepository(t.Context(), runID, &changed))
			selected, err = restarted.GetAgentRunRepository(t.Context(), runID)
			require.NoError(t, err)
			require.Equal(t, checkout, selected)
		})
	}
}
