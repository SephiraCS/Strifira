package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
)

// WithAccountLock holds a shared provider-access lock, or an exclusive unlink
// lock. Re-read the account after acquiring it: a syncer's earlier ListAll
// snapshot must never grant access to credentials after unlinking.
func (r *MailAccountRepository) WithAccountLock(ctx context.Context, workspaceID, id uuid.UUID, exclusive bool, fn func(*domain.MailAccount) error) error {
	if r.operationSlots != nil {
		select {
		case r.operationSlots <- struct{}{}:
			defer func() { <-r.operationSlots }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Keep the lock until the callback actually stops, including when its
	// provider connection is being cancelled. database/sql must not release
	// the lock early on cancellation of the operation context.
	conn, err := r.db.Connx(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTxx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lock := "pg_advisory_xact_lock_shared"
	if exclusive {
		lock = "pg_advisory_xact_lock"
	}
	if _, err := tx.ExecContext(ctx, `SELECT `+lock+`(hashtextextended($1::text, 310031))`, id); err != nil {
		return err
	}
	var row mailAccountRow
	if err := tx.GetContext(ctx, &row, `SELECT * FROM mail_accounts WHERE id=$1 AND workspace_id=$2`, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMailAccountNotFound
		}
		return err
	}
	account := row.toDomain()
	// The transaction only owns the lock. Token refreshes and message writes
	// keep their existing transactions, including durable refresh rotation.
	return fn(&account)
}
