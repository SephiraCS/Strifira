package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SephiraCS/strifira-backend/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// ErrChatChannelExists reports a duplicate channel name in the workspace.
var ErrChatChannelExists = errors.New("channel name already in use")

type ChatChannelRepository struct {
	db *sqlx.DB
}

func NewChatChannelRepository(db *sqlx.DB) *ChatChannelRepository {
	return &ChatChannelRepository{db: db}
}

var _ ChatChannelRepo = (*ChatChannelRepository)(nil)

// CreateChannel inserts the channel with its membership rows atomically.
// memberIDs must include the creator for private channels and DMs; public
// channels pass only the creator and are open to every workspace member.
func (r *ChatChannelRepository) CreateChannel(ctx context.Context, channel *domain.ChatChannel, memberIDs []uuid.UUID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `INSERT INTO chat_channels (id, workspace_id, type, name, topic, dm_key, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at, updated_at`
	if err := tx.QueryRowContext(ctx, query, channel.ID, channel.WorkspaceID, channel.Type, channel.Name, channel.Topic, channel.DMKey, channel.CreatedBy).
		Scan(&channel.CreatedAt, &channel.UpdatedAt); err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_chat_channels_name" {
			return ErrChatChannelExists
		}
		return err
	}

	for _, memberID := range memberIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chat_channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			channel.ID, memberID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (r *ChatChannelRepository) GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.ChatChannel, error) {
	var channel domain.ChatChannel
	err := r.db.GetContext(ctx, &channel, `SELECT * FROM chat_channels WHERE id = $1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decorate(ctx, []*domain.ChatChannel{&channel}); err != nil {
		return nil, err
	}
	return &channel, nil
}

// GetDMByUsers returns the existing direct-message channel between two
// users in a workspace, or nil when none exists yet.
func (r *ChatChannelRepository) GetDMByUsers(ctx context.Context, workspaceID, userA, userB uuid.UUID) (*domain.ChatChannel, error) {
	key := DMKey(userA, userB)
	var channel domain.ChatChannel
	err := r.db.GetContext(ctx, &channel, `SELECT * FROM chat_channels WHERE workspace_id = $1 AND dm_key = $2`, workspaceID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.decorate(ctx, []*domain.ChatChannel{&channel}); err != nil {
		return nil, err
	}
	return &channel, nil
}

// ListVisibleByWorkspace returns the channels the user can see, ordered by
// most recent activity: public channels plus every private channel or DM
// they are a member of.
func (r *ChatChannelRepository) ListVisibleByWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.ChatChannel, error) {
	query := `
		SELECT c.*,
		       (SELECT MAX(msg.created_at) FROM chat_messages msg WHERE msg.channel_id = c.id) AS last_post_at
		FROM chat_channels c
		WHERE c.workspace_id = $1
		  AND (c.type = 'public' OR EXISTS (
		      SELECT 1 FROM chat_channel_members m WHERE m.channel_id = c.id AND m.user_id = $2))
		ORDER BY COALESCE(
			(SELECT MAX(msg.created_at) FROM chat_messages msg WHERE msg.channel_id = c.id),
			c.created_at) DESC,
		c.name ASC`
	var channels []domain.ChatChannel
	if err := r.db.SelectContext(ctx, &channels, query, workspaceID, userID); err != nil {
		return nil, err
	}
	ptrs := make([]*domain.ChatChannel, len(channels))
	for i := range channels {
		ptrs[i] = &channels[i]
	}
	if err := r.decorate(ctx, ptrs); err != nil {
		return nil, err
	}
	return channels, nil
}

// EnsureDefaultChannel provisions the workspace's #general channel once.
// Concurrent callers race on the partial unique index; the loser reads back
// the winner's row.
func (r *ChatChannelRepository) EnsureDefaultChannel(ctx context.Context, workspaceID, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_channels (id, workspace_id, type, name, created_by)
		SELECT gen_random_uuid(), $1, 'public', 'general', $2
		WHERE NOT EXISTS (SELECT 1 FROM chat_channels WHERE workspace_id = $1 AND name = 'general')
		ON CONFLICT DO NOTHING`, workspaceID, userID)
	return err
}

func (r *ChatChannelRepository) AddChannelMember(ctx context.Context, channelID, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO chat_channel_members (channel_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		channelID, userID)
	return err
}

func (r *ChatChannelRepository) IsChannelMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.GetContext(ctx, &exists,
		`SELECT EXISTS(SELECT 1 FROM chat_channel_members WHERE channel_id = $1 AND user_id = $2)`,
		channelID, userID)
	return exists, err
}

func (r *ChatChannelRepository) ChannelMemberIDs(ctx context.Context, channelID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.SelectContext(ctx, &ids, `SELECT user_id FROM chat_channel_members WHERE channel_id = $1 ORDER BY created_at`, channelID)
	return ids, err
}

// UpdateChannelTopic rewrites the topic and bumps updated_at.
func (r *ChatChannelRepository) UpdateChannelTopic(ctx context.Context, channelID uuid.UUID, topic string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chat_channels SET topic = $1, updated_at = NOW() WHERE id = $2`, topic, channelID)
	return err
}

// decorate resolves the DM peer (the other member) for DM channels and the
// private flag, so the API can hand the UI complete channel identities.
func (r *ChatChannelRepository) decorate(ctx context.Context, channels []*domain.ChatChannel) error {
	for _, channel := range channels {
		channel.Private = channel.Type != domain.ChatChannelPublic
		if channel.Type != domain.ChatChannelDM {
			continue
		}
		members, err := r.ChannelMemberIDs(ctx, channel.ID)
		if err != nil {
			return err
		}
		for _, memberID := range members {
			if memberID == channel.CreatedBy {
				continue
			}
			peer, err := r.UserByID(ctx, memberID)
			if err != nil {
				return err
			}
			if peer != nil {
				channel.DMPeer = peer
			}
			break
		}
	}
	return nil
}

// UserByID resolves the chat-renderable subset of a user (DM peers).
func (r *ChatChannelRepository) UserByID(ctx context.Context, userID uuid.UUID) (*domain.ChatUser, error) {
	var user domain.ChatUser
	err := r.db.GetContext(ctx, &user, `SELECT id, name, display_name, email, avatar_url FROM users WHERE id = $1`, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// DMKey builds the sorted pair identity used for DM deduplication.
func DMKey(a, b uuid.UUID) string {
	as, bs := a.String(), b.String()
	if as > bs {
		as, bs = bs, as
	}
	return as + ":" + bs
}
