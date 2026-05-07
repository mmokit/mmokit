// Package postgres is the chat Repository's Postgres implementation.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/services/chat"
)

type pgRepo struct{ pool *pgxpool.Pool }

// New returns a Postgres-backed chat.Repository.
func New(pool *pgxpool.Pool) chat.Repository { return &pgRepo{pool: pool} }

var _ chat.Repository = (*pgRepo)(nil)

// --- Channels ---

func (r *pgRepo) UpsertChannel(ctx context.Context, c chat.Channel) (chat.Channel, error) {
	const q = `
		INSERT INTO chat_channels (channel_id, slug, kind, topic, slow_mode_seconds, password_hash, owner_user_id)
		VALUES (COALESCE($1::uuid, gen_random_uuid()), $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7::uuid, $8::uuid))
		ON CONFLICT (slug) DO UPDATE
		  SET kind = EXCLUDED.kind,
		      topic = EXCLUDED.topic,
		      slow_mode_seconds = EXCLUDED.slow_mode_seconds,
		      password_hash = EXCLUDED.password_hash,
		      owner_user_id = EXCLUDED.owner_user_id,
		      updated_at = NOW()
		RETURNING channel_id, slug, kind, topic, slow_mode_seconds,
		          COALESCE(password_hash, ''),
		          COALESCE(owner_user_id, $8::uuid),
		          created_at, updated_at`
	var idArg any
	if c.ChannelID != uuid.Nil {
		idArg = c.ChannelID
	} else {
		idArg = nil
	}
	row := r.pool.QueryRow(ctx, q,
		idArg, c.Slug, c.Kind, c.Topic, c.SlowModeSeconds,
		c.PasswordHash, c.OwnerUserID, uuid.Nil,
	)
	var out chat.Channel
	if err := row.Scan(&out.ChannelID, &out.Slug, &out.Kind, &out.Topic, &out.SlowModeSeconds,
		&out.PasswordHash, &out.OwnerUserID, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return chat.Channel{}, err
	}
	return out, nil
}

func (r *pgRepo) GetChannelByID(ctx context.Context, id uuid.UUID) (chat.Channel, error) {
	return r.scanOneChannel(ctx, `WHERE channel_id = $1`, id)
}

func (r *pgRepo) GetChannelBySlug(ctx context.Context, slug string) (chat.Channel, error) {
	return r.scanOneChannel(ctx, `WHERE slug = $1`, slug)
}

func (r *pgRepo) scanOneChannel(ctx context.Context, where string, args ...any) (chat.Channel, error) {
	q := `SELECT channel_id, slug, kind, topic, slow_mode_seconds,
		COALESCE(password_hash, ''), COALESCE(owner_user_id, $$00000000-0000-0000-0000-000000000000$$::uuid),
		created_at, updated_at FROM chat_channels ` + where
	row := r.pool.QueryRow(ctx, q, args...)
	var c chat.Channel
	if err := row.Scan(&c.ChannelID, &c.Slug, &c.Kind, &c.Topic, &c.SlowModeSeconds,
		&c.PasswordHash, &c.OwnerUserID, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return chat.Channel{}, chat.ErrChannelNotFound
		}
		return chat.Channel{}, err
	}
	return c, nil
}

func (r *pgRepo) ListAllChannels(ctx context.Context) ([]chat.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT channel_id, slug, kind, topic, slow_mode_seconds,
		       COALESCE(password_hash, ''),
		       COALESCE(owner_user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       created_at, updated_at
		  FROM chat_channels
		 ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.Channel
	for rows.Next() {
		var c chat.Channel
		if err := rows.Scan(&c.ChannelID, &c.Slug, &c.Kind, &c.Topic, &c.SlowModeSeconds,
			&c.PasswordHash, &c.OwnerUserID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *pgRepo) UpdateChannel(ctx context.Context, c chat.Channel) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channels
		   SET slug=$2, kind=$3, topic=$4, slow_mode_seconds=$5,
		       password_hash=NULLIF($6, ''),
		       owner_user_id=NULLIF($7, '00000000-0000-0000-0000-000000000000'::uuid),
		       updated_at=NOW()
		 WHERE channel_id=$1`,
		c.ChannelID, c.Slug, c.Kind, c.Topic, c.SlowModeSeconds,
		c.PasswordHash, c.OwnerUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrChannelNotFound
	}
	return nil
}

func (r *pgRepo) DeleteChannel(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_channels WHERE channel_id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrChannelNotFound
	}
	return nil
}

// --- Members ---

func (r *pgRepo) AddOrUpdateMember(ctx context.Context, m chat.ChannelMember) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_channel_members (channel_id, user_id, role)
		VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'member'))
		ON CONFLICT (channel_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		m.ChannelID, m.UserID, m.Role)
	return err
}

func (r *pgRepo) RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_channel_members WHERE channel_id=$1 AND user_id=$2`, channelID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrMemberNotFound
	}
	return nil
}

func (r *pgRepo) BulkSetMembers(ctx context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM chat_channel_members WHERE channel_id = $1`, channelID); err != nil {
		return err
	}
	if role == "" {
		role = "member"
	}
	for _, uid := range userIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO chat_channel_members (channel_id, user_id, role) VALUES ($1, $2, $3)`,
			channelID, uid, role); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *pgRepo) ListMembers(ctx context.Context, channelID uuid.UUID) ([]chat.ChannelMember, error) {
	return r.scanMembers(ctx, `WHERE channel_id = $1`, channelID)
}

func (r *pgRepo) ListAllMembers(ctx context.Context) ([]chat.ChannelMember, error) {
	return r.scanMembers(ctx, ``)
}

func (r *pgRepo) scanMembers(ctx context.Context, where string, args ...any) ([]chat.ChannelMember, error) {
	q := `SELECT channel_id, user_id, role, joined_at,
		COALESCE(banned_until, 'epoch'::timestamptz),
		COALESCE(banned_by, '00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(banned_reason, '')
		FROM chat_channel_members ` + where
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.ChannelMember
	for rows.Next() {
		var m chat.ChannelMember
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Role, &m.JoinedAt,
			&m.BannedUntil, &m.BannedBy, &m.BannedReason); err != nil {
			return nil, err
		}
		// Treat 'epoch' sentinel as zero time
		if m.BannedUntil.Year() <= 1970 {
			m.BannedUntil = time.Time{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *pgRepo) SetMemberRole(ctx context.Context, channelID, userID uuid.UUID, role string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE chat_channel_members SET role=$3 WHERE channel_id=$1 AND user_id=$2`,
		channelID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrMemberNotFound
	}
	return nil
}

func (r *pgRepo) SetMemberBan(ctx context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_channel_members (channel_id, user_id, role, banned_until, banned_by, banned_reason)
		VALUES ($1, $2, 'member', $3, $4, $5)
		ON CONFLICT (channel_id, user_id) DO UPDATE
		  SET banned_until=EXCLUDED.banned_until, banned_by=EXCLUDED.banned_by, banned_reason=EXCLUDED.banned_reason`,
		channelID, userID, until, bannedBy, reason)
	return err
}

func (r *pgRepo) ClearMemberBan(ctx context.Context, channelID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channel_members
		   SET banned_until=NULL, banned_by=NULL, banned_reason=NULL
		 WHERE channel_id=$1 AND user_id=$2`, channelID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrMemberNotFound
	}
	return nil
}

// --- Mutes ---

func (r *pgRepo) UpsertMute(ctx context.Context, m chat.Mute) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_mutes (user_id, channel_id, expires_at, reason, muted_by)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		ON CONFLICT (user_id, channel_id) DO UPDATE
		  SET expires_at=EXCLUDED.expires_at, reason=EXCLUDED.reason, muted_by=EXCLUDED.muted_by`,
		m.UserID, m.ChannelID, m.ExpiresAt, m.Reason, m.MutedBy)
	return err
}

func (r *pgRepo) DeleteMute(ctx context.Context, userID, channelID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_mutes WHERE user_id=$1 AND channel_id=$2`, userID, channelID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chat.ErrMuteNotFound
	}
	return nil
}

func (r *pgRepo) ListActiveMutes(ctx context.Context) ([]chat.Mute, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, channel_id, expires_at, COALESCE(reason, ''), muted_by, created_at
		  FROM chat_mutes
		 WHERE expires_at > NOW()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chat.Mute
	for rows.Next() {
		var mu chat.Mute
		if err := rows.Scan(&mu.UserID, &mu.ChannelID, &mu.ExpiresAt, &mu.Reason, &mu.MutedBy, &mu.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, mu)
	}
	return out, rows.Err()
}

// --- Reaper ---

func (r *pgRepo) DeleteExpiredMutes(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_mutes WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *pgRepo) ClearExpiredBans(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channel_members
		   SET banned_until=NULL, banned_by=NULL, banned_reason=NULL
		 WHERE banned_until IS NOT NULL AND banned_until <= NOW()`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
