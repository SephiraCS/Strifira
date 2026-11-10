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

var ErrMailDraftNotFound = errors.New("mail draft not found")

type MailDraftRepository struct {
	db *sqlx.DB
}

func NewMailDraftRepository(db *sqlx.DB) *MailDraftRepository {
	return &MailDraftRepository{db: db}
}

var _ MailDraftRepo = (*MailDraftRepository)(nil)

type mailDraftRow struct {
	ID                   uuid.UUID  `db:"id"`
	WorkspaceID          uuid.UUID  `db:"workspace_id"`
	AccountID            *uuid.UUID `db:"account_id"`
	UserID               uuid.UUID  `db:"user_id"`
	ThreadKey            string     `db:"thread_key"`
	InReplyToMessageID   *uuid.UUID `db:"in_reply_to_message_id"`
	ForwardFromMessageID *uuid.UUID `db:"forward_from_message_id"`
	ToJSON               []byte     `db:"to_addresses"`
	CCJSON               []byte     `db:"cc_addresses"`
	BCCJSON              []byte     `db:"bcc_addresses"`
	Subject              string     `db:"subject"`
	Body                 string     `db:"body"`
	CreatedAt            time.Time  `db:"created_at"`
	UpdatedAt            time.Time  `db:"updated_at"`
}

func (row mailDraftRow) toDomain() domain.MailDraft {
	return domain.MailDraft{
		ID:                   row.ID,
		WorkspaceID:          row.WorkspaceID,
		AccountID:            row.AccountID,
		UserID:               row.UserID,
		ThreadKey:            row.ThreadKey,
		InReplyToMessageID:   row.InReplyToMessageID,
		ForwardFromMessageID: row.ForwardFromMessageID,
		To:                   decodeAddresses(row.ToJSON),
		CC:                   decodeAddresses(row.CCJSON),
		BCC:                  decodeAddresses(row.BCCJSON),
		Subject:              row.Subject,
		Body:                 row.Body,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
	}
}

func (r *MailDraftRepository) Create(ctx context.Context, draft *domain.MailDraft) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mail_drafts (
			id, workspace_id, account_id, user_id, thread_key,
			in_reply_to_message_id, forward_from_message_id,
			to_addresses, cc_addresses, bcc_addresses, subject, body
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		draft.ID, draft.WorkspaceID, draft.AccountID, draft.UserID, draft.ThreadKey,
		draft.InReplyToMessageID, draft.ForwardFromMessageID,
		encodeAddresses(draft.To), encodeAddresses(draft.CC), encodeAddresses(draft.BCC),
		draft.Subject, draft.Body,
	)
	return err
}

func (r *MailDraftRepository) GetByID(ctx context.Context, workspaceID, userID, id uuid.UUID) (*domain.MailDraft, error) {
	var row mailDraftRow
	if err := r.db.GetContext(ctx, &row, `
		SELECT * FROM mail_drafts WHERE id = $1 AND workspace_id = $2 AND user_id = $3`,
		id, workspaceID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailDraftNotFound
		}
		return nil, err
	}
	draft := row.toDomain()
	return &draft, nil
}

func (r *MailDraftRepository) ListByUser(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.MailDraft, error) {
	rows := []mailDraftRow{}
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT * FROM mail_drafts WHERE workspace_id = $1 AND user_id = $2 ORDER BY updated_at DESC`,
		workspaceID, userID); err != nil {
		return nil, err
	}
	out := make([]domain.MailDraft, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

func (r *MailDraftRepository) Update(ctx context.Context, draft *domain.MailDraft) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE mail_drafts SET
			account_id = $4, thread_key = $5,
			in_reply_to_message_id = $6, forward_from_message_id = $7,
			to_addresses = $8, cc_addresses = $9, bcc_addresses = $10,
			subject = $11, body = $12, updated_at = NOW()
		WHERE id = $1 AND workspace_id = $2 AND user_id = $3`,
		draft.ID, draft.WorkspaceID, draft.UserID,
		draft.AccountID, draft.ThreadKey,
		draft.InReplyToMessageID, draft.ForwardFromMessageID,
		encodeAddresses(draft.To), encodeAddresses(draft.CC), encodeAddresses(draft.BCC),
		draft.Subject, draft.Body,
	)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailDraftNotFound
	}
	return nil
}

func (r *MailDraftRepository) Delete(ctx context.Context, workspaceID, userID, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM mail_drafts WHERE id = $1 AND workspace_id = $2 AND user_id = $3`,
		id, workspaceID, userID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailDraftNotFound
	}
	return nil
}

func (r *MailDraftRepository) AddAttachment(ctx context.Context, att *domain.MailAttachment) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO mail_attachments (id, message_id, draft_id, filename, content_type, size_bytes, content)
		VALUES ($1, NULL, $2, $3, $4, $5, $6)`,
		att.ID, att.DraftID, att.Filename, att.ContentType, att.SizeBytes, att.Content,
	)
	return err
}

func (r *MailDraftRepository) DeleteAttachment(ctx context.Context, draftID, attachmentID uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM mail_attachments WHERE id = $1 AND draft_id = $2`, attachmentID, draftID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailMessageNotFound
	}
	return nil
}

func (r *MailDraftRepository) ListAttachments(ctx context.Context, draftID uuid.UUID) ([]domain.MailAttachment, error) {
	return listAttachments(ctx, r.db, `draft_id = $1`, draftID)
}

func (r *MailDraftRepository) GetAttachment(ctx context.Context, draftID, attachmentID uuid.UUID) (*domain.MailAttachment, error) {
	var row mailAttachmentRow
	if err := r.db.GetContext(ctx, &row, `
		SELECT * FROM mail_attachments WHERE id = $1 AND draft_id = $2`,
		attachmentID, draftID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailMessageNotFound
		}
		return nil, err
	}
	return row.toDomain(), nil
}
