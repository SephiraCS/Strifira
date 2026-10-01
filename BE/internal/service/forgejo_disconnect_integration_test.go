package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ku/ku-backend/internal/domain"
	"github.com/ku/ku-backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForgejoDisconnectRetainsCredentialsForReferencedDevMachineCheckout(t *testing.T) {
	env := setupForgejoAutomation(t)
	f := seedAutomationWorkspace(t, env.db, true)
	ctx := t.Context()
	env.svc.encryptionKey = crypto.DeriveKey("forgejo-disconnect-database-fixture")
	encrypted, err := crypto.Encrypt("workspace-token", env.svc.encryptionKey)
	require.NoError(t, err)
	conn := &domain.ForgejoConnection{
		ID: uuid.New(), WorkspaceID: f.workspace, InstanceURL: "https://forgejo.example.test",
		Token: encrypted, AccountLogin: "owner",
	}
	require.NoError(t, env.svc.fRepo.UpsertConnection(ctx, conn))
	repo := &domain.ForgejoRepoModel{
		ID: uuid.New(), WorkspaceID: f.workspace, ForgejoRepoID: 42,
		FullName: "org/project", InstanceURL: conn.InstanceURL, DefaultBranch: "main", IsActive: true,
	}
	require.NoError(t, env.svc.fRepo.CreateRepo(ctx, repo))
	machineID, checkoutID := uuid.New(), uuid.New()
	_, err = env.db.ExecContext(ctx, `INSERT INTO dev_machines
		(id,workspace_id,issue_id,created_by_user_id,routing_key,name,machine_size,
		 cpu_millis,memory_mb,disk_gb,max_runtime_minutes,expires_at,repository_affinity_id)
		VALUES ($1,$2,$3,$4,'disconnectfixture','Disconnect fixture','small',2000,4096,20,120,
		 NOW()+INTERVAL '1 hour',$5)`, machineID, f.workspace, f.issueID, f.aliceUserID, repo.ID)
	require.NoError(t, err)
	_, err = env.db.ExecContext(ctx, `INSERT INTO dev_machine_checkouts
		(id,workspace_id,machine_id,issue_id,forgejo_repo_id,repository_full_name,
		 base_branch,working_branch,workspace_path,status)
		VALUES ($1,$2,$3,$4,$5,'org/project','main','feature/FA-1','/workspace/project','ready')`,
		checkoutID, f.workspace, machineID, f.issueID, repo.ID)
	require.NoError(t, err)

	err = env.svc.Disconnect(ctx, f.workspace)

	require.Error(t, err)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	require.True(t, ok, "disconnect must propagate the actual database restriction")
	assert.Equal(t, "23503", pgErr.Code)
	assert.Equal(t, "fk_dev_machine_checkouts_repository", pgErr.ConstraintName)
	retainedConn, err := env.svc.fRepo.GetConnectionByWorkspace(ctx, f.workspace)
	require.NoError(t, err)
	require.NotNil(t, retainedConn, "the failed disconnect must not remove credentials")
	assert.Equal(t, conn.ID, retainedConn.ID)
	assert.Equal(t, encrypted, retainedConn.Token)
	retainedRepo, err := env.svc.fRepo.GetRepoByID(ctx, repo.ID)
	require.NoError(t, err)
	require.NotNil(t, retainedRepo)
	var checkoutCount int
	require.NoError(t, env.db.GetContext(ctx, &checkoutCount, `SELECT count(*) FROM dev_machine_checkouts WHERE id=$1`, checkoutID))
	assert.Equal(t, 1, checkoutCount, "disconnect must preserve the existing checkout")

	// Once the owner removes the dependent checkout, the retained connection
	// allows the same disconnect request to finish its local cleanup.
	_, err = env.db.ExecContext(ctx, `DELETE FROM dev_machine_checkouts WHERE id=$1`, checkoutID)
	require.NoError(t, err)
	require.NoError(t, env.svc.Disconnect(ctx, f.workspace))
	removedConn, err := env.svc.fRepo.GetConnectionByWorkspace(ctx, f.workspace)
	require.NoError(t, err)
	assert.Nil(t, removedConn)
	removedRepo, err := env.svc.fRepo.GetRepoByID(ctx, repo.ID)
	require.NoError(t, err)
	assert.Nil(t, removedRepo)
	var machineCount int
	require.NoError(t, env.db.GetContext(ctx, &machineCount,
		`SELECT count(*) FROM dev_machines WHERE id=$1 AND repository_affinity_id IS NULL`, machineID))
	assert.Equal(t, 1, machineCount, "successful unlink must keep the development machine and clear its affinity")
}
