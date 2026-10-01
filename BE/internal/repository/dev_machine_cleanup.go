package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/ku/ku-backend/internal/domain"
)

// Claim one terminal cleanup at a time. SKIP LOCKED permits multiple managers;
// an expired lease is recoverable after death without losing the queue row.
func (r *DevMachineRepository) LeaseAgentRunCleanup(ctx context.Context, owner string, lease time.Duration) (*domain.DevMachineAgentRun, error) {
	if owner == "" || lease <= 0 {
		return nil, errors.New("agent cleanup lease requires an owner and positive duration")
	}
	var run domain.DevMachineAgentRun
	err := r.db.GetContext(ctx, &run, `WITH selected AS (
        SELECT q.agent_run_id FROM dev_machine_agent_run_cleanup q
        JOIN dev_machine_agent_runs r ON r.id=q.agent_run_id
        WHERE q.available_at <= NOW() AND (q.lease_expires_at IS NULL OR q.lease_expires_at <= NOW())
          AND r.status IN ('succeeded','failed','cancelled','timeout')
        ORDER BY q.available_at,q.agent_run_id LIMIT 1 FOR UPDATE OF q SKIP LOCKED
    ), leased AS (
        UPDATE dev_machine_agent_run_cleanup q SET lease_owner=$1,
            lease_expires_at=NOW()+make_interval(secs => $2), attempts=q.attempts+1
        FROM selected s WHERE q.agent_run_id=s.agent_run_id RETURNING q.agent_run_id
    ) SELECT r.* FROM dev_machine_agent_runs r JOIN leased l ON l.agent_run_id=r.id`, owner, lease.Seconds())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &run, err
}

func (r *DevMachineRepository) CompleteAgentRunCleanup(ctx context.Context, runID uuid.UUID, owner string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM dev_machine_agent_run_cleanup
        WHERE agent_run_id=$1 AND lease_owner=$2 AND lease_expires_at > NOW()`, runID, owner)
	return requireAgentCleanupLease(result, err)
}

func (r *DevMachineRepository) RetryAgentRunCleanup(ctx context.Context, runID uuid.UUID, owner, message string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE dev_machine_agent_run_cleanup
        SET available_at=NOW()+make_interval(secs => LEAST(3600,30*attempts)),
            lease_owner=NULL,lease_expires_at=NULL,last_error=$3
        WHERE agent_run_id=$1 AND lease_owner=$2 AND lease_expires_at > NOW()`, runID, owner, message)
	return requireAgentCleanupLease(result, err)
}

func requireAgentCleanupLease(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("agent cleanup lease changed or expired")
	}
	return nil
}
