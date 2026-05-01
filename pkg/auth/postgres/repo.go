// Package postgres provides the Postgres implementation of auth.Repository.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/auth"
)

//go:embed migrations/*.sql
var Migrations embed.FS

// Repo is the Postgres-backed auth.Repository.
type Repo struct {
	pool *pgxpool.Pool
}

// New wraps an existing pgxpool.Pool. The pool must already have the auth
// migrations applied (use pkg/persist/postgres.Open with
// WithExtraMigrations(Migrations, "migrations", "auth")).
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var _ auth.Repository = (*Repo)(nil)

// ---------------------------------------------------------------------------
// User methods
// ---------------------------------------------------------------------------

func (r *Repo) CreateUser(ctx context.Context, u auth.User, password string) (auth.User, error) {
	var out auth.User
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	err = tx.QueryRow(ctx, `
		INSERT INTO auth_users (username, email)
		VALUES ($1, NULLIF($2, ''))
		RETURNING user_id, username, COALESCE(email,''), email_verified, mfa_enabled,
		          status, failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz),
		          COALESCE(last_login_at, 'epoch'::timestamptz), created_at, updated_at
	`, u.Username, u.Email).Scan(
		&out.UserID, &out.Username, &out.Email, &out.EmailVerified, &out.MFAEnabled,
		&out.Status, &out.FailedAttempts, &out.LockedUntil, &out.LastLoginAt,
		&out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return out, auth.ErrUsernameTaken
		}
		return out, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_passwords (user_id, password_hash) VALUES ($1, $2)
	`, out.UserID, password); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (r *Repo) GetUserByUsername(ctx context.Context, username string) (auth.User, error) {
	return r.fetchUser(ctx, `WHERE username = $1`, username)
}

func (r *Repo) GetUserByID(ctx context.Context, id uuid.UUID) (auth.User, error) {
	return r.fetchUser(ctx, `WHERE user_id = $1`, id)
}

func (r *Repo) fetchUser(ctx context.Context, where string, arg any) (auth.User, error) {
	var u auth.User
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, username, COALESCE(email,''), email_verified, mfa_enabled,
		       status, failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz),
		       COALESCE(last_login_at, 'epoch'::timestamptz), created_at, updated_at
		FROM auth_users `+where, arg,
	).Scan(&u.UserID, &u.Username, &u.Email, &u.EmailVerified, &u.MFAEnabled,
		&u.Status, &u.FailedAttempts, &u.LockedUntil, &u.LastLoginAt,
		&u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, auth.ErrUserNotFound
	}
	return u, err
}

func (r *Repo) UpdateUserLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_users
		SET last_login_at = $2, failed_attempts = 0, locked_until = NULL, updated_at = NOW()
		WHERE user_id = $1
	`, id, at)
	return err
}

func (r *Repo) IncrementFailedAttempts(ctx context.Context, id uuid.UUID, threshold int, dur time.Duration) (int, time.Time, error) {
	var count int
	var locked time.Time
	err := r.pool.QueryRow(ctx, `
		UPDATE auth_users
		SET failed_attempts = failed_attempts + 1,
		    locked_until = CASE
		        WHEN failed_attempts + 1 >= $2 THEN NOW() + make_interval(secs => $3)
		        ELSE locked_until
		    END,
		    updated_at = NOW()
		WHERE user_id = $1
		RETURNING failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz)
	`, id, threshold, dur.Seconds()).Scan(&count, &locked)
	return count, locked, err
}

func (r *Repo) ResetFailedAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_users
		SET failed_attempts = 0, locked_until = NULL, updated_at = NOW()
		WHERE user_id = $1
	`, id)
	return err
}

func (r *Repo) SetUserStatus(ctx context.Context, id uuid.UUID, status string, locked time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_users
		SET status = $2, locked_until = NULLIF($3, 'epoch'::timestamptz), updated_at = NOW()
		WHERE user_id = $1
	`, id, status, locked)
	return err
}

// ---------------------------------------------------------------------------
// Password methods
// ---------------------------------------------------------------------------

func (r *Repo) GetPassword(ctx context.Context, id uuid.UUID) (auth.PasswordCredential, error) {
	var p auth.PasswordCredential
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, password_hash, hash_algorithm, changed_at
		FROM auth_passwords WHERE user_id = $1
	`, id).Scan(&p.UserID, &p.PasswordHash, &p.HashAlgorithm, &p.ChangedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, auth.ErrUserNotFound
	}
	return p, err
}

func (r *Repo) UpdatePassword(ctx context.Context, id uuid.UUID, newHash string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_passwords SET password_hash = $2, changed_at = NOW() WHERE user_id = $1
	`, id, newHash)
	return err
}

// ---------------------------------------------------------------------------
// Session methods
// ---------------------------------------------------------------------------

func (r *Repo) CreateSession(ctx context.Context, s auth.Session) error {
	metaJSON, _ := json.Marshal(s.ClientMeta)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO auth_sessions (token_hash, user_id, expires_at, client_meta)
		VALUES ($1, $2, $3, $4)
	`, s.TokenHash, s.UserID, s.ExpiresAt, metaJSON)
	return err
}

func (r *Repo) GetSession(ctx context.Context, tokenHash []byte) (auth.Session, error) {
	var s auth.Session
	var meta []byte
	var revokedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT token_hash, user_id, issued_at, expires_at, last_used_at,
		       revoked_at, COALESCE(client_meta::text, '{}')
		FROM auth_sessions WHERE token_hash = $1
	`, tokenHash).Scan(&s.TokenHash, &s.UserID, &s.IssuedAt, &s.ExpiresAt,
		&s.LastUsedAt, &revokedAt, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, auth.ErrSessionNotFound
	}
	if err != nil {
		return s, err
	}
	if revokedAt != nil {
		s.RevokedAt = *revokedAt
	}
	_ = json.Unmarshal(meta, &s.ClientMeta)
	return s, nil
}

func (r *Repo) SlideSession(ctx context.Context, tokenHash []byte, newExpiry time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_sessions SET expires_at = $2, last_used_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash, newExpiry)
	return err
}

func (r *Repo) RevokeSession(ctx context.Context, tokenHash []byte) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, tokenHash)
	return err
}

func (r *Repo) RevokeAllSessionsExcept(ctx context.Context, id uuid.UUID, keep []byte) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE user_id = $1 AND token_hash != $2 AND revoked_at IS NULL
	`, id, keep)
	return int(tag.RowsAffected()), err
}

func (r *Repo) RevokeAllSessionsForUser(ctx context.Context, id uuid.UUID) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, id)
	return int(tag.RowsAffected()), err
}

func (r *Repo) ListActiveSessions(ctx context.Context, id uuid.UUID) ([]auth.Session, error) {
	// Filter on revoked_at IS NULL — surviving rows are guaranteed
	// non-revoked, so RevokedAt stays the zero value and IsZero() works.
	rows, err := r.pool.Query(ctx, `
		SELECT token_hash, user_id, issued_at, expires_at, last_used_at,
		       COALESCE(client_meta::text, '{}')
		FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL
		ORDER BY issued_at DESC
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Session
	for rows.Next() {
		var s auth.Session
		var meta []byte
		if err := rows.Scan(&s.TokenHash, &s.UserID, &s.IssuedAt, &s.ExpiresAt,
			&s.LastUsedAt, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(meta, &s.ClientMeta)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Reaper methods
// ---------------------------------------------------------------------------

func (r *Repo) DeleteExpiredSessions(ctx context.Context, retention time.Duration) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM auth_sessions
		WHERE revoked_at IS NOT NULL OR expires_at < NOW() - make_interval(secs => $1)
	`, retention.Seconds())
	return int(tag.RowsAffected()), err
}

func (r *Repo) DeleteOldAuditRows(ctx context.Context, olderThan time.Duration) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM auth_audit_log
		WHERE occurred_at < NOW() - make_interval(secs => $1)
	`, olderThan.Seconds())
	return int(tag.RowsAffected()), err
}

// ---------------------------------------------------------------------------
// Audit methods
// ---------------------------------------------------------------------------

func (r *Repo) Audit(ctx context.Context, ev auth.AuditEvent) error {
	metaJSON, _ := json.Marshal(ev.Metadata)
	var ipStr *string
	if ev.IPAddr.IsValid() {
		s := ev.IPAddr.String()
		ipStr = &s
	}
	var uid *uuid.UUID
	if ev.UserID != uuid.Nil {
		u := ev.UserID
		uid = &u
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO auth_audit_log
		    (event, user_id, username_attempted, ip_addr, user_agent, gateway_id, metadata)
		VALUES ($1, $2, NULLIF($3,''), $4::INET, NULLIF($5,''), NULLIF($6,''), $7)
	`, ev.Event, uid, ev.UsernameAttempted, ipStr, ev.UserAgent, ev.GatewayID, metaJSON)
	return err
}

func (r *Repo) RecentAudit(ctx context.Context, id uuid.UUID, limit int) ([]auth.AuditEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT event,
		       COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       COALESCE(username_attempted,''),
		       COALESCE(host(ip_addr),''),
		       COALESCE(user_agent,''),
		       COALESCE(gateway_id,''),
		       COALESCE(metadata::text, '{}')
		FROM auth_audit_log WHERE user_id = $1
		ORDER BY occurred_at DESC LIMIT $2
	`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.AuditEvent
	for rows.Next() {
		var ev auth.AuditEvent
		var ipStr, metaStr string
		if err := rows.Scan(&ev.Event, &ev.UserID, &ev.UsernameAttempted, &ipStr,
			&ev.UserAgent, &ev.GatewayID, &metaStr); err != nil {
			return nil, err
		}
		if ipStr != "" {
			ev.IPAddr, _ = netip.ParseAddr(ipStr)
		}
		_ = json.Unmarshal([]byte(metaStr), &ev.Metadata)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isUniqueViolation returns true for Postgres error code 23505
// (unique_violation).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
