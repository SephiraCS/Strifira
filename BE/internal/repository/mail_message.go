package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrMailMessageNotFound = errors.New("mail message not found")

type MailMessageRepository struct {
	db *sqlx.DB
}

func NewMailMessageRepository(db *sqlx.DB) *MailMessageRepository {
	return &MailMessageRepository{db: db}
}

var _ MailMessageRepo = (*MailMessageRepository)(nil)

// mailMessageRow mirrors a mail_messages row. Address lists arrive as JSONB
// and are decoded manually — sqlx scans aliased joins reliably without
// nested-struct mapping.
type mailMessageRow struct {
	ID             uuid.UUID `db:"id"`
	WorkspaceID    uuid.UUID `db:"workspace_id"`
	AccountID      uuid.UUID `db:"account_id"`
	Folder         string    `db:"folder"`
	MessageID      string    `db:"message_id"`
	InReplyTo      string    `db:"in_reply_to"`
	ThreadKey      string    `db:"thread_key"`
	Subject        string    `db:"subject"`
	FromName       string    `db:"from_name"`
	FromAddress    string    `db:"from_address"`
	ReplyTo        string    `db:"reply_to"`
	ToJSON         []byte    `db:"to_addresses"`
	CCJSON         []byte    `db:"cc_addresses"`
	BCCJSON        []byte    `db:"bcc_addresses"`
	BodyText       string    `db:"body_text"`
	BodyHTML       string    `db:"body_html"`
	Snippet        string    `db:"snippet"`
	Unread         bool      `db:"unread"`
	Starred        bool      `db:"starred"`
	Archived       bool      `db:"archived"`
	Spam           bool      `db:"spam"`
	UID            int64     `db:"uid"`
	SentAt         time.Time `db:"sent_at"`
	CreatedAt      time.Time `db:"created_at"`
	HasAttachments bool      `db:"has_attachments"`
}

func (row mailMessageRow) toDomain() domain.MailMessage {
	return domain.MailMessage{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		AccountID:      row.AccountID,
		Folder:         row.Folder,
		MessageID:      row.MessageID,
		InReplyTo:      row.InReplyTo,
		ThreadKey:      row.ThreadKey,
		Subject:        row.Subject,
		From:           domain.MailAddress{Name: row.FromName, Address: row.FromAddress},
		ReplyTo:        row.ReplyTo,
		To:             decodeAddresses(row.ToJSON),
		CC:             decodeAddresses(row.CCJSON),
		BCC:            decodeAddresses(row.BCCJSON),
		BodyText:       row.BodyText,
		BodyHTML:       row.BodyHTML,
		Snippet:        row.Snippet,
		Unread:         row.Unread,
		Starred:        row.Starred,
		Archived:       row.Archived,
		Spam:           row.Spam,
		UID:            row.UID,
		SentAt:         row.SentAt,
		CreatedAt:      row.CreatedAt,
		HasAttachments: row.HasAttachments,
	}
}

func decodeAddresses(raw []byte) []domain.MailAddress {
	if len(raw) == 0 {
		return nil
	}
	out := []domain.MailAddress{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func encodeAddresses(addrs []domain.MailAddress) []byte {
	if addrs == nil {
		addrs = []domain.MailAddress{}
	}
	raw, err := json.Marshal(addrs)
	if err != nil {
		return []byte("[]")
	}
	return raw
}

func snippetOf(body string) string {
	trimmed := strings.TrimSpace(body)
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	if len(trimmed) > 240 {
		return trimmed[:240]
	}
	return trimmed
}

func (r *MailMessageRepository) SyncState(ctx context.Context, accountID uuid.UUID) (uint32, int64, error) {
	var state struct {
		UIDValidity int64 `db:"uid_validity"`
		LastUID     int64 `db:"last_uid"`
	}
	if err := r.db.GetContext(ctx, &state,
		`SELECT uid_validity, last_uid FROM mail_accounts WHERE id = $1`, accountID); err != nil {
		return 0, 0, err
	}
	return uint32(state.UIDValidity), state.LastUID, nil
}

func (r *MailMessageRepository) UpdateSyncState(ctx context.Context, accountID uuid.UUID, uidValidity uint32, lastUID int64, lastError string, syncedAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE mail_accounts
		SET uid_validity = $2, last_uid = $3, last_sync_error = $4, last_synced_at = $5, updated_at = NOW()
		WHERE id = $1`, accountID, int64(uidValidity), lastUID, lastError, syncedAt)
	return err
}

// MailInboxMessage is one fetched message plus its decoded attachments.
type MailInboxMessage struct {
	Message     domain.MailMessage
	Attachments []domain.MailAttachment
}

// InsertInboxMessages stores fetched messages (with their attachments) in a
// transaction per message; rows whose (account_id, message_id) already exist
// are skipped. It returns the newly inserted messages, with IDs set.
func (r *MailMessageRepository) InsertInboxMessages(ctx context.Context, messages []MailInboxMessage) ([]domain.MailMessage, error) {
	inserted := make([]domain.MailMessage, 0, len(messages))
	for i := range messages {
		m := &messages[i].Message
		if m.ID == uuid.Nil {
			m.ID = uuid.New()
		}
		tx, err := r.db.BeginTxx(ctx, nil)
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO mail_messages (
				id, workspace_id, account_id, folder, message_id, in_reply_to, thread_key,
				subject, from_name, from_address, reply_to,
				to_addresses, cc_addresses, bcc_addresses,
				body_text, body_html, snippet, unread, starred, archived, spam, uid, sent_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, FALSE, FALSE, FALSE, $19, $20
			)
			ON CONFLICT (account_id, message_id) WHERE folder = 'inbox' AND message_id <> ''
			DO NOTHING`,
			m.ID, m.WorkspaceID, m.AccountID, domain.MailFolderInbox, m.MessageID, m.InReplyTo, m.ThreadKey,
			m.Subject, m.From.Name, m.From.Address, m.ReplyTo,
			encodeAddresses(m.To), encodeAddresses(m.CC), encodeAddresses(m.BCC),
			m.BodyText, m.BodyHTML, snippetOf(m.BodyText), m.Unread, m.UID, m.SentAt,
		)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if rows, _ := res.RowsAffected(); rows > 0 {
			for _, att := range messages[i].Attachments {
				if att.ID == uuid.Nil {
					att.ID = uuid.New()
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO mail_attachments (id, message_id, draft_id, filename, content_type, size_bytes, content)
					VALUES ($1, $2, NULL, $3, $4, $5, $6)`,
					att.ID, m.ID, att.Filename, att.ContentType, att.SizeBytes, att.Content); err != nil {
					_ = tx.Rollback()
					return nil, err
				}
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			inserted = append(inserted, *m)
		} else {
			// Already cached on a previous sync.
			_ = tx.Rollback()
		}
	}
	return inserted, nil
}

// CountUnreadInbox is independent of the visible folder, search and page size.
func (r *MailMessageRepository) CountUnreadInbox(ctx context.Context, workspaceID uuid.UUID, accountID *uuid.UUID) (int, error) {
	query := `SELECT COUNT(*) FROM mail_messages WHERE workspace_id = $1 AND folder = 'inbox' AND unread AND NOT archived AND NOT spam`
	args := []any{workspaceID}
	if accountID != nil {
		query += ` AND account_id = $2`
		args = append(args, *accountID)
	}
	var count int
	err := r.db.GetContext(ctx, &count, query, args...)
	return count, err
}

func (r *MailMessageRepository) ListByFolder(ctx context.Context, workspaceID uuid.UUID, accountID *uuid.UUID, folder string, q string, filters MailFilters, limit, offset int) ([]domain.MailMessage, error) {
	query := `
		SELECT m.*, EXISTS (SELECT 1 FROM mail_attachments a WHERE a.message_id = m.id) AS has_attachments
		FROM mail_messages m
		WHERE m.workspace_id = $1 AND m.folder = $2`
	args := []any{workspaceID, folder}
	argN := 3

	if accountID != nil {
		query += fmt.Sprintf(` AND m.account_id = $%d`, argN)
		args = append(args, *accountID)
		argN++
	}
	if q != "" {
		like := "%" + q + "%"
		query += fmt.Sprintf(` AND (m.subject ILIKE $%d OR m.from_address ILIKE $%d OR m.from_name ILIKE $%d OR m.body_text ILIKE $%d)`, argN, argN, argN, argN)
		args = append(args, like)
		argN++
	}

	if folder == domain.MailFolderInbox {
		if filters.Unread != nil {
			query += fmt.Sprintf(` AND m.unread = $%d`, argN)
			args = append(args, *filters.Unread)
			argN++
		}
		if filters.Starred != nil {
			query += fmt.Sprintf(` AND m.starred = $%d`, argN)
			args = append(args, *filters.Starred)
			argN++
		}
		if filters.Archived != nil {
			query += fmt.Sprintf(` AND m.archived = $%d`, argN)
			args = append(args, *filters.Archived)
			argN++
		}
		if filters.Spam != nil {
			query += fmt.Sprintf(` AND m.spam = $%d`, argN)
			args = append(args, *filters.Spam)
			argN++
		}
	}

	query += fmt.Sprintf(` ORDER BY m.sent_at DESC LIMIT $%d OFFSET $%d`, argN, argN+1)
	args = append(args, limit, offset)

	rows := []mailMessageRow{}
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	out := make([]domain.MailMessage, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.toDomain())
	}
	return out, nil
}

func (r *MailMessageRepository) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*domain.MailMessage, error) {
	var row mailMessageRow
	if err := r.db.GetContext(ctx, &row,
		`SELECT * FROM mail_messages WHERE id = $1 AND workspace_id = $2`, id, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailMessageNotFound
		}
		return nil, err
	}
	msg := row.toDomain()
	return &msg, nil
}

func (r *MailMessageRepository) GetByAccountAndMessageID(ctx context.Context, accountID uuid.UUID, messageID string) (*domain.MailMessage, error) {
	var row mailMessageRow
	if err := r.db.GetContext(ctx, &row,
		`SELECT * FROM mail_messages WHERE account_id = $1 AND message_id = $2`, accountID, messageID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailMessageNotFound
		}
		return nil, err
	}
	msg := row.toDomain()
	return &msg, nil
}

func (r *MailMessageRepository) SetFlags(ctx context.Context, workspaceID, id uuid.UUID, unread, starred, archived, spam *bool) (*domain.MailMessage, error) {
	_, err := r.db.ExecContext(ctx, `
		UPDATE mail_messages SET
			unread = COALESCE($3, unread),
			starred = COALESCE($4, starred),
			archived = COALESCE($5, archived),
			spam = COALESCE($6, spam)
		WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID, unread, starred, archived, spam)
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, workspaceID, id)
}

func (r *MailMessageRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM mail_messages WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return ErrMailMessageNotFound
	}
	return nil
}

// FinalizeSentMessage stores a composed message, its attachments and — when
// sent from a draft — re-points the draft's staged attachments and drops the
// draft, all in one transaction.
func (r *MailMessageRepository) FinalizeSentMessage(ctx context.Context, message *domain.MailMessage, stagedAttachments []domain.MailAttachment, draftID *uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO mail_messages (
			id, workspace_id, account_id, folder, message_id, in_reply_to, thread_key,
			subject, from_name, from_address, reply_to,
			to_addresses, cc_addresses, bcc_addresses,
			body_text, body_html, snippet, unread, starred, archived, spam, uid, sent_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, FALSE, FALSE, FALSE, FALSE, 0, $18
		)`,
		message.ID, message.WorkspaceID, message.AccountID, domain.MailFolderSent, message.MessageID, message.InReplyTo, message.ThreadKey,
		message.Subject, message.From.Name, message.From.Address, message.ReplyTo,
		encodeAddresses(message.To), encodeAddresses(message.CC), encodeAddresses(message.BCC),
		message.BodyText, message.BodyHTML, snippetOf(message.BodyText), message.SentAt,
	)
	if err != nil {
		return err
	}

	for _, att := range stagedAttachments {
		if att.ID == uuid.Nil {
			att.ID = uuid.New()
		}
		if att.DraftID != nil {
			// Already staged on the draft: re-point to the sent message.
			if _, err := tx.ExecContext(ctx, `
				UPDATE mail_attachments SET message_id = $2, draft_id = NULL WHERE id = $1`,
				att.ID, message.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mail_attachments (id, message_id, draft_id, filename, content_type, size_bytes, content)
			VALUES ($1, $2, NULL, $3, $4, $5, $6)`,
			att.ID, message.ID, att.Filename, att.ContentType, att.SizeBytes, att.Content); err != nil {
			return err
		}
	}

	if draftID != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mail_drafts WHERE id = $1`, *draftID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *MailMessageRepository) ListAttachmentsByMessage(ctx context.Context, messageID uuid.UUID) ([]domain.MailAttachment, error) {
	return listAttachments(ctx, r.db, `message_id = $1`, messageID)
}

func (r *MailMessageRepository) GetAttachment(ctx context.Context, workspaceID, attachmentID uuid.UUID) (*domain.MailAttachment, error) {
	var row mailAttachmentRow
	if err := r.db.GetContext(ctx, &row, `
		SELECT a.* FROM mail_attachments a
		JOIN mail_messages m ON m.id = a.message_id
		WHERE a.id = $1 AND m.workspace_id = $2`, attachmentID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrMailMessageNotFound
		}
		return nil, err
	}
	return row.toDomain(), nil
}

type mailAttachmentRow struct {
	ID          uuid.UUID  `db:"id"`
	MessageID   *uuid.UUID `db:"message_id"`
	DraftID     *uuid.UUID `db:"draft_id"`
	Filename    string     `db:"filename"`
	ContentType string     `db:"content_type"`
	SizeBytes   int64      `db:"size_bytes"`
	Content     []byte     `db:"content"`
	CreatedAt   time.Time  `db:"created_at"`
}

func (row mailAttachmentRow) toDomain() *domain.MailAttachment {
	return &domain.MailAttachment{
		ID:          row.ID,
		MessageID:   row.MessageID,
		DraftID:     row.DraftID,
		Filename:    row.Filename,
		ContentType: row.ContentType,
		SizeBytes:   row.SizeBytes,
		Content:     row.Content,
		CreatedAt:   row.CreatedAt,
	}
}

func listAttachments(ctx context.Context, db sqlx.QueryerContext, where string, arg any) ([]domain.MailAttachment, error) {
	rows := []mailAttachmentRow{}
	if err := sqlx.SelectContext(ctx, db, &rows, `
		SELECT id, message_id, draft_id, filename, content_type, size_bytes, created_at FROM mail_attachments
		WHERE `+where, arg); err != nil {
		return nil, err
	}
	out := make([]domain.MailAttachment, 0, len(rows))
	for _, row := range rows {
		att := row.toDomain()
		att.Content = nil
		out = append(out, *att)
	}
	return out, nil
}
