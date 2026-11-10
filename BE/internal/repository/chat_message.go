package repository

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type ChatMessageRepository struct {
	db *sqlx.DB
}

func NewChatMessageRepository(db *sqlx.DB) *ChatMessageRepository {
	return &ChatMessageRepository{db: db}
}

var _ ChatMessageRepo = (*ChatMessageRepository)(nil)

// chatMessageRow mirrors a message joined with its author in flat columns —
// sqlx scans aliased joins reliably without nested-struct mapping.
type chatMessageRow struct {
	ID                uuid.UUID  `db:"id"`
	ChannelID         uuid.UUID  `db:"channel_id"`
	UserID            uuid.UUID  `db:"user_id"`
	Body              string     `db:"body"`
	CreatedAt         time.Time  `db:"created_at"`
	EditedAt          *time.Time `db:"edited_at"`
	DeletedAt         *time.Time `db:"deleted_at"`
	AuthorID          uuid.UUID  `db:"author_id"`
	AuthorName        string     `db:"author_name"`
	AuthorDisplayName string     `db:"author_display_name"`
	AuthorEmail       string     `db:"author_email"`
	AuthorAvatarURL   *string    `db:"author_avatar_url"`
}

func (row chatMessageRow) toDomain() domain.ChatMessage {
	return domain.ChatMessage{
		ID:        row.ID,
		ChannelID: row.ChannelID,
		UserID:    row.UserID,
		Author: &domain.ChatUser{
			ID:          row.AuthorID,
			Name:        row.AuthorName,
			DisplayName: row.AuthorDisplayName,
			Email:       row.AuthorEmail,
			AvatarURL:   row.AuthorAvatarURL,
		},
		Body:      row.Body,
		CreatedAt: row.CreatedAt,
		EditedAt:  row.EditedAt,
		DeletedAt: row.DeletedAt,
	}
}

const chatMessageSelect = `
	SELECT m.id, m.channel_id, m.user_id, m.body, m.created_at, m.edited_at, m.deleted_at,
	       u.id AS author_id, u.name AS author_name, u.display_name AS author_display_name,
	       u.email AS author_email, u.avatar_url AS author_avatar_url
	FROM chat_messages m
	JOIN users u ON u.id = m.user_id`

func (r *ChatMessageRepository) CreateMessage(ctx context.Context, message *domain.ChatMessage) error {
	query := `INSERT INTO chat_messages (id, channel_id, user_id, body) VALUES ($1, $2, $3, $4) RETURNING created_at`
	if err := r.db.QueryRowContext(ctx, query, message.ID, message.ChannelID, message.UserID, message.Body).
		Scan(&message.CreatedAt); err != nil {
		return err
	}
	// Read back with the author join so broadcasts carry the full payload.
	created, err := r.GetMessageByID(ctx, message.ID)
	if err != nil {
		return err
	}
	*message = *created
	return nil
}

func (r *ChatMessageRepository) GetMessageByID(ctx context.Context, id uuid.UUID) (*domain.ChatMessage, error) {
	var row chatMessageRow
	err := r.db.GetContext(ctx, &row, chatMessageSelect+` WHERE m.id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	message := row.toDomain()
	return &message, nil
}

// ListMessages returns a page of a channel's timeline (excluding
// tombstones), oldest-last for direct rendering. before paginates backwards;
// one extra row is fetched so the caller can tell there are older messages.
func (r *ChatMessageRepository) ListMessages(ctx context.Context, channelID uuid.UUID, before *time.Time, limit int) ([]domain.ChatMessage, error) {
	query := chatMessageSelect + `
		WHERE m.channel_id = $1 AND m.deleted_at IS NULL`
	args := []any{channelID}
	if before != nil {
		query += ` AND m.created_at < $2`
		args = append(args, *before)
	}
	query += ` ORDER BY m.created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)

	var rows []chatMessageRow
	if err := r.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	messages := make([]domain.ChatMessage, len(rows))
	for i, row := range rows {
		messages[i] = row.toDomain()
	}
	// Oldest first for rendering.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

func (r *ChatMessageRepository) UpdateMessageBody(ctx context.Context, id uuid.UUID, body string) (*domain.ChatMessage, error) {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE chat_messages SET body = $1, edited_at = NOW() WHERE id = $2 AND deleted_at IS NULL`, body, id); err != nil {
		return nil, err
	}
	return r.GetMessageByID(ctx, id)
}

func (r *ChatMessageRepository) SoftDeleteMessage(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chat_messages SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id)
	return err
}
