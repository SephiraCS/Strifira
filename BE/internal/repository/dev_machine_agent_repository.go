package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

// Pin before any protected Git helper or provider starts. Replays may observe
// the identical selection but cannot replace an earlier run's authority.
func (r *DevMachineRepository) PinAgentRunRepository(ctx context.Context, runID uuid.UUID, checkout *domain.DevMachineCheckout) error {
	if checkout == nil || runID == uuid.Nil {
		return errors.New("agent repository selection is unavailable")
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO dev_machine_agent_run_repositories
        (agent_run_id,machine_id,workspace_id,selection_id,repository_full_name,repository_url,base_branch,working_branch,workspace_path)
        SELECT r.id,r.machine_id,r.workspace_id,$4,$5,$6,$7,$8,$9 FROM dev_machine_agent_runs r
        WHERE r.id=$1 AND r.machine_id=$2 AND r.workspace_id=$3
          AND r.status IN ('starting','running','waiting_input')
          AND ((r.checkout_id IS NULL AND $4=r.machine_id AND $9='/workspace/repository') OR r.checkout_id=$4)
        ON CONFLICT (agent_run_id) DO NOTHING`, runID, checkout.MachineID, checkout.WorkspaceID, checkout.ID,
		checkout.RepositoryFullName, checkout.RepositoryURL, checkout.BaseBranch, checkout.WorkingBranch, checkout.WorkspacePath)
	if err != nil {
		return err
	}
	selected, err := r.GetAgentRunRepository(ctx, runID)
	if err != nil {
		return err
	}
	if selected == nil || !sameAgentRepository(selected, checkout) {
		return errors.New("agent repository selection changed or run authority is unavailable")
	}
	return nil
}

func (r *DevMachineRepository) GetAgentRunRepository(ctx context.Context, runID uuid.UUID) (*domain.DevMachineCheckout, error) {
	var row struct {
		domain.DevMachineCheckout
		RepositoryURL string `db:"repository_url"`
	}
	err := r.db.GetContext(ctx, &row, `SELECT selection_id AS id,machine_id,workspace_id,
        repository_full_name,repository_url,base_branch,working_branch,workspace_path
        FROM dev_machine_agent_run_repositories WHERE agent_run_id=$1`, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row.DevMachineCheckout.RepositoryURL = row.RepositoryURL
	return &row.DevMachineCheckout, err
}

func sameAgentRepository(a, b *domain.DevMachineCheckout) bool {
	return a.ID == b.ID && a.MachineID == b.MachineID && a.WorkspaceID == b.WorkspaceID &&
		a.RepositoryFullName == b.RepositoryFullName && a.RepositoryURL == b.RepositoryURL &&
		a.BaseBranch == b.BaseBranch && a.WorkingBranch == b.WorkingBranch && a.WorkspacePath == b.WorkspacePath
}
