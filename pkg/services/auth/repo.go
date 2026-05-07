package auth

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Errors returned by Repository implementations.
var (
	ErrUserNotFound    = errors.New("auth: user not found")
	ErrUsernameTaken   = errors.New("auth: username taken")
	ErrSessionNotFound = errors.New("auth: session not found")
)

// User is the canonical identity record. Mirrors auth_users.
type User struct {
	UserID         uuid.UUID
	Username       string  // always lowercase
	Email          string  // empty when not provided
	EmailVerified  bool
	MFAEnabled     bool
	Status         string  // "active" | "locked" | "disabled"
	FailedAttempts int
	LockedUntil    time.Time // zero value = not locked
	LastLoginAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PasswordCredential mirrors auth_passwords (one per user in v1).
type PasswordCredential struct {
	UserID        uuid.UUID
	PasswordHash  string  // argon2id encoded
	HashAlgorithm string  // 'argon2id'
	ChangedAt     time.Time
}

// Session mirrors auth_sessions.
type Session struct {
	TokenHash  []byte  // sha256(raw_token)
	UserID     uuid.UUID
	IssuedAt   time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time  // zero value = not revoked
	ClientMeta map[string]string  // ip, ua, gateway_id
}

// AuditEvent mirrors auth_audit_log row inputs.
type AuditEvent struct {
	Event             string
	UserID            uuid.UUID  // zero value when unknown
	UsernameAttempted string
	IPAddr            netip.Addr  // zero value when unavailable
	UserAgent         string
	GatewayID         string
	Metadata          map[string]any
}

// Repository abstracts persistence. Postgres impl: pkg/auth/postgres.
// In-memory mock for tests: pkg/auth/authtest.
type Repository interface {
	// Users
	CreateUser(ctx context.Context, u User, password string) (User, error)
	GetUserByUsername(ctx context.Context, username string) (User, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (User, error)
	UpdateUserLogin(ctx context.Context, userID uuid.UUID, at time.Time) error
	IncrementFailedAttempts(ctx context.Context, userID uuid.UUID, lockoutThreshold int, lockoutDuration time.Duration) (newCount int, lockedUntil time.Time, err error)
	ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error
	SetUserStatus(ctx context.Context, userID uuid.UUID, status string, lockedUntil time.Time) error

	// Passwords
	GetPassword(ctx context.Context, userID uuid.UUID) (PasswordCredential, error)
	UpdatePassword(ctx context.Context, userID uuid.UUID, newHash string) error

	// Sessions
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, tokenHash []byte) (Session, error)
	SlideSession(ctx context.Context, tokenHash []byte, newExpiry time.Time) error
	RevokeSession(ctx context.Context, tokenHash []byte) error
	RevokeAllSessionsExcept(ctx context.Context, userID uuid.UUID, keepTokenHash []byte) (int, error)
	RevokeAllSessionsForUser(ctx context.Context, userID uuid.UUID) (int, error)
	ListActiveSessions(ctx context.Context, userID uuid.UUID) ([]Session, error)

	// Reaper
	DeleteExpiredSessions(ctx context.Context, retentionAfterExpiry time.Duration) (int, error)
	DeleteOldAuditRows(ctx context.Context, olderThan time.Duration) (int, error)

	// Audit
	Audit(ctx context.Context, ev AuditEvent) error
	RecentAudit(ctx context.Context, userID uuid.UUID, limit int) ([]AuditEvent, error)
}
