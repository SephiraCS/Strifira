package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrMailOAuthSession = errors.New("mail authorization expired or already used")

type MailOAuthRepository struct{ db *sqlx.DB }

func NewMailOAuthRepository(db *sqlx.DB) *MailOAuthRepository { return &MailOAuthRepository{db: db} }

func (r *MailOAuthRepository) SaveSession(ctx context.Context, hash string, workspaceID, userID uuid.UUID, encrypted string) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `DELETE FROM mail_oauth_sessions WHERE expires_at < NOW() OR (workspace_id=$1 AND user_id=$2)`, workspaceID, userID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mail_oauth_sessions (state_hash,workspace_id,user_id,payload_encrypted,expires_at) VALUES ($1,$2,$3,$4,$5)`, hash, workspaceID, userID, encrypted, time.Now().Add(10*time.Minute))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *MailOAuthRepository) ConsumeSession(ctx context.Context, hash string, workspaceID, userID uuid.UUID) (string, error) {
	var encrypted string
	err := r.db.GetContext(ctx, &encrypted, `DELETE FROM mail_oauth_sessions WHERE state_hash=$1 AND workspace_id=$2 AND user_id=$3 AND expires_at>NOW() RETURNING payload_encrypted`, hash, workspaceID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrMailOAuthSession
	}
	return encrypted, err
}

// Link atomically persists a validated account and its encrypted token. A
// reconnect must retain the same owner, provider and mailbox identity.
func (r *MailOAuthRepository) Link(ctx context.Context, account *domain.MailAccount, encrypted string, expectedRevision *int64) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expectedRevision != nil {
		var stored mailAccountRow
		if err := tx.GetContext(ctx, &stored, `SELECT * FROM mail_accounts WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, account.ID, account.WorkspaceID); err != nil {
			return ErrMailAccountNotFound
		}
		if stored.CreatedBy != account.CreatedBy || stored.EmailAddress != account.EmailAddress || stored.AuthMethod == "oauth2" && stored.OAuthProvider != account.OAuthProvider {
			return ErrMailAccountNotFound
		}
		if stored.CredentialsRevision != *expectedRevision {
			return ErrMailAccountConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE mail_accounts SET last_sync_error='',updated_at=NOW(),credentials_revision=credentials_revision+1,auth_method='oauth2',oauth_provider=$2,password_encrypted='',imap_host=$3,imap_port=$4,imap_security=$5,smtp_host=$6,smtp_port=$7,smtp_security=$8,username=$9,smtp_username=$10 WHERE id=$1`, account.ID, account.OAuthProvider, account.IMAPHost, account.IMAPPort, account.IMAPSecurity, account.SMTPHost, account.SMTPPort, account.SMTPSecurity, account.Username, account.SMTPUsername)
	} else {
		err = createMailAccount(ctx, tx, account)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mail_oauth_tokens (account_id,token_encrypted) VALUES ($1,$2) ON CONFLICT (account_id) DO UPDATE SET token_encrypted=EXCLUDED.token_encrypted`, account.ID, encrypted)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type MailOAuthToken struct {
	Encrypted     string `db:"token_encrypted"`
	DeleteAccount bool
	SyncError     string
}

// WithToken locks the account and token across processes. Refresh-token
// rotation, reconnect and disconnect cannot overwrite each other's tokens.
func (r *MailOAuthRepository) WithToken(ctx context.Context, workspaceID, accountID uuid.UUID, fn func(*MailOAuthToken) error) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var token MailOAuthToken
	err = tx.GetContext(ctx, &token, `SELECT t.token_encrypted FROM mail_accounts a JOIN mail_oauth_tokens t ON t.account_id=a.id WHERE a.id=$1 AND a.workspace_id=$2 FOR UPDATE OF a,t`, accountID, workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMailAccountNotFound
	}
	if err != nil {
		return err
	}
	if err := fn(&token); err != nil {
		return err
	}
	if token.DeleteAccount {
		_, err = tx.ExecContext(ctx, `DELETE FROM mail_accounts WHERE id=$1`, accountID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE mail_oauth_tokens SET token_encrypted=$2 WHERE account_id=$1`, accountID, token.Encrypted)
		if err == nil && token.SyncError != "" {
			_, err = tx.ExecContext(ctx, `UPDATE mail_accounts SET last_sync_error=$2, updated_at=NOW() WHERE id=$1`, accountID, token.SyncError)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
