package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrMailAccountNotFound = errors.New("mail account not found")
var ErrMailAccountConflict = errors.New("Esta conta foi alterada durante a operação. Reabra a conta e tente novamente.")

type MailAccountRepository struct {
	db             *sqlx.DB
	operationSlots chan struct{}
}

func NewMailAccountRepository(db *sqlx.DB) *MailAccountRepository {
	r := &MailAccountRepository{db: db}
	// A lock holds a connection while provider access uses the repositories.
	// Leave pool capacity for those queries even under concurrent sync requests.
	if limit := db.Stats().MaxOpenConnections; limit > 1 {
		r.operationSlots = make(chan struct{}, limit/2)
	}
	return r
}

var _ MailAccountRepo = (*MailAccountRepository)(nil)

func (r *MailAccountRepository) Create(ctx context.Context, account *domain.MailAccount) error {
	return createMailAccount(ctx, r.db, account)
}

func createMailAccount(ctx context.Context, db sqlx.ExtContext, account *domain.MailAccount) error {
	method := account.AuthMethod
	if method == "" {
		method = "password"
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO mail_accounts (
			id, workspace_id, created_by, email_address, display_name,
			imap_host, imap_port, imap_security,
			smtp_host, smtp_port, smtp_security,
			username, password_encrypted, signature, smtp_username, auth_method, oauth_provider
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		account.ID, account.WorkspaceID, account.CreatedBy, account.EmailAddress, account.DisplayName,
		account.IMAPHost, account.IMAPPort, account.IMAPSecurity,
		account.SMTPHost, account.SMTPPort, account.SMTPSecurity,
		account.Username, account.PasswordEncrypted, account.Signature, account.SMTPUsername, method, account.OAuthProvider,
	)
	return err
}

func (r *MailAccountRepository) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*domain.MailAccount, error) {
	var row mailAccountRow
	if err := r.db.GetContext(ctx, &row, `
		SELECT * FROM mail_accounts WHERE id = $1 AND workspace_id = $2`, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailAccountNotFound
		}
		return nil, err
	}
	acct := row.toDomain()
	return &acct, nil
}

func (r *MailAccountRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]domain.MailAccount, error) {
	rows := []mailAccountRow{}
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT * FROM mail_accounts WHERE workspace_id = $1 ORDER BY email_address`, workspaceID); err != nil {
		return nil, err
	}
	out := make([]domain.MailAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

func (r *MailAccountRepository) ListAll(ctx context.Context) ([]domain.MailAccount, error) {
	rows := []mailAccountRow{}
	if err := r.db.SelectContext(ctx, &rows, `SELECT * FROM mail_accounts`); err != nil {
		return nil, err
	}
	out := make([]domain.MailAccount, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

// Update rewrites the connection and display fields. A nil PasswordEncrypted
// keeps the stored password untouched (the API only sends a new one when the
// user actually changed it).
func (r *MailAccountRepository) Update(ctx context.Context, account *domain.MailAccount) error {
	query := `
		UPDATE mail_accounts SET
			email_address = $3, display_name = $4,
			imap_host = $5, imap_port = $6, imap_security = $7,
			smtp_host = $8, smtp_port = $9, smtp_security = $10,
			username = $11, signature = $12, smtp_username = $13, updated_at = NOW(), credentials_revision=credentials_revision+1`
	args := []any{
		account.ID, account.WorkspaceID,
		account.EmailAddress, account.DisplayName,
		account.IMAPHost, account.IMAPPort, account.IMAPSecurity,
		account.SMTPHost, account.SMTPPort, account.SMTPSecurity,
		account.Username, account.Signature, account.SMTPUsername,
	}
	if account.PasswordEncrypted != "" {
		query += `, password_encrypted = $14`
		args = append(args, account.PasswordEncrypted)
	}
	// A password edit begun before an OAuth upgrade must not replace the
	// protocol servers underneath the new bearer token, or overwrite metadata.
	n := len(args)
	args = append(args, account.AuthMethod, account.OAuthProvider, account.CredentialsRevision)
	query += fmt.Sprintf(` WHERE id = $1 AND workspace_id = $2 AND auth_method=$%d AND oauth_provider=$%d AND credentials_revision=$%d`, n+1, n+2, n+3)
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailAccountConflict
	}
	return nil
}

func (r *MailAccountRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM mail_accounts WHERE id = $1 AND workspace_id = $2 AND auth_method='password'`, id, workspaceID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailAccountNotFound
	}
	return nil
}

// mailAccountRow mirrors a mail_accounts row; sqlx maps `SELECT *` columns.
type mailAccountRow struct {
	CredentialsRevision int64      `db:"credentials_revision"`
	AuthMethod          string     `db:"auth_method"`
	OAuthProvider       string     `db:"oauth_provider"`
	SMTPUsername        string     `db:"smtp_username"`
	ID                  uuid.UUID  `db:"id"`
	WorkspaceID         uuid.UUID  `db:"workspace_id"`
	CreatedBy           uuid.UUID  `db:"created_by"`
	EmailAddress        string     `db:"email_address"`
	DisplayName         string     `db:"display_name"`
	IMAPHost            string     `db:"imap_host"`
	IMAPPort            int        `db:"imap_port"`
	IMAPSecurity        string     `db:"imap_security"`
	SMTPHost            string     `db:"smtp_host"`
	SMTPPort            int        `db:"smtp_port"`
	SMTPSecurity        string     `db:"smtp_security"`
	Username            string     `db:"username"`
	PasswordEncrypted   string     `db:"password_encrypted"`
	Signature           string     `db:"signature"`
	UIDValidity         int64      `db:"uid_validity"`
	LastUID             int64      `db:"last_uid"`
	LastSyncedAt        *time.Time `db:"last_synced_at"`
	LastSyncError       string     `db:"last_sync_error"`
	CreatedAt           time.Time  `db:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at"`
}

func (row mailAccountRow) toDomain() domain.MailAccount {
	return domain.MailAccount{
		CredentialsRevision: row.CredentialsRevision,
		AuthMethod:          row.AuthMethod,
		OAuthProvider:       row.OAuthProvider,
		ID:                  row.ID,
		WorkspaceID:         row.WorkspaceID,
		CreatedBy:           row.CreatedBy,
		EmailAddress:        row.EmailAddress,
		DisplayName:         row.DisplayName,
		IMAPHost:            row.IMAPHost,
		IMAPPort:            row.IMAPPort,
		IMAPSecurity:        row.IMAPSecurity,
		SMTPHost:            row.SMTPHost,
		SMTPPort:            row.SMTPPort,
		SMTPSecurity:        row.SMTPSecurity,
		Username:            row.Username,
		SMTPUsername:        row.SMTPUsername,
		PasswordEncrypted:   row.PasswordEncrypted,
		Signature:           row.Signature,
		UIDValidity:         uint32(row.UIDValidity),
		LastUID:             row.LastUID,
		LastSyncedAt:        row.LastSyncedAt,
		LastSyncError:       row.LastSyncError,
		CreatedAt:           row.CreatedAt,
		UpdatedAt:           row.UpdatedAt,
	}
}
