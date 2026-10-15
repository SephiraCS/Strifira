package repository

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
)

func TestAgentRunCleanupDatabaseQueueSurvivesRestartAndDoesNotRescanHistory(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not configured")
	}
	db, err := sqlx.ConnectContext(t.Context(), "pgx", databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	fixture := newEnvironmentRaceFixture(t, db)
	repo := NewDevMachineRepository(db)
	machineID := fixture.insertMachine(t, domain.DevMachineStatusRunning, domain.DevMachineStatusRunning, false)
	runID := uuid.New()
	_, err = db.ExecContext(t.Context(), `INSERT INTO dev_machine_agent_runs (id,machine_id,workspace_id,requested_by_user_id,provider_id,mode,status,prompt,command_argv,max_runtime_seconds)
        VALUES ($1,$2,$3,$4,'opencode','autonomous','queued','cleanup restart','["true"]',120)`, runID, machineID, fixture.workspaceID, fixture.userID)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.GetContext(t.Context(), &count, `SELECT count(*) FROM dev_machine_agent_run_cleanup WHERE agent_run_id=$1`, runID))
	require.Zero(t, count)
	_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_runs SET status='timeout' WHERE id=$1`, runID)
	require.NoError(t, err)
	require.NoError(t, db.GetContext(t.Context(), &count, `SELECT count(*) FROM dev_machine_agent_run_cleanup WHERE agent_run_id=$1`, runID))
	require.Equal(t, 1, count)

	// Simultaneous claimers must never both execute the same cleanup.
	type claim struct {
		owner string
		run   *domain.DevMachineAgentRun
		err   error
	}
	claims := make(chan claim, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			owner := uuid.NewString()
			run, err := repo.LeaseAgentRunCleanup(t.Context(), owner, time.Minute)
			claims <- claim{owner: owner, run: run, err: err}
		})
	}
	wg.Wait()
	close(claims)
	var winner claim
	var claimed int
	for result := range claims {
		require.NoError(t, result.err)
		if result.run != nil {
			claimed++
			winner = result
		}
	}
	require.Equal(t, 1, claimed)
	require.Equal(t, runID, winner.run.ID)

	// A vanished backend leaves its row leased. Expiry permits a new owner;
	// the old owner can neither delete nor postpone the new owner's work.
	_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_run_cleanup SET lease_expires_at=NOW()-interval '1 second' WHERE agent_run_id=$1`, runID)
	require.NoError(t, err)
	restarted := NewDevMachineRepository(db)
	reclaimed, err := restarted.LeaseAgentRunCleanup(t.Context(), "new-process-claim", time.Minute)
	require.NoError(t, err)
	require.Equal(t, runID, reclaimed.ID)
	require.Error(t, repo.CompleteAgentRunCleanup(t.Context(), runID, winner.owner))
	require.Error(t, repo.RetryAgentRunCleanup(t.Context(), runID, winner.owner, "stale failure"))
	require.NoError(t, restarted.RetryAgentRunCleanup(t.Context(), runID, "new-process-claim", "cleanup temporarily unavailable"))
	run, err := restarted.LeaseAgentRunCleanup(t.Context(), "early-retry", time.Minute)
	require.NoError(t, err)
	require.Nil(t, run, "failed work is delayed rather than hot-looped")
	_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_run_cleanup SET available_at=NOW() WHERE agent_run_id=$1`, runID)
	require.NoError(t, err)
	run, err = restarted.LeaseAgentRunCleanup(t.Context(), "final-claim", time.Minute)
	require.NoError(t, err)
	require.Equal(t, runID, run.ID)
	require.NoError(t, restarted.CompleteAgentRunCleanup(t.Context(), runID, "final-claim"))
	_, err = db.ExecContext(t.Context(), `UPDATE dev_machine_agent_runs SET status='timeout' WHERE id=$1`, runID)
	require.NoError(t, err)
	run, err = restarted.LeaseAgentRunCleanup(t.Context(), "after-success", time.Minute)
	require.NoError(t, err)
	require.Nil(t, run, "already-cleaned history is never rescanned")
}
