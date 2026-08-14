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
	ErrUserNotFound       = errors.New("auth: user not found")
	ErrUsernameTaken      = errors.New("auth: username taken")
	ErrSessionNotFound    = errors.New("auth: session not found")
	ErrCapabilityNotFound = errors.New("auth: capability grant not found")
)

// User is the canonical identity record. Mirrors auth.users.
type User struct {
	UserID         uuid.UUID
	Username       string // always lowercase
	Email          string // empty when not provided
	EmailVerified  bool
	MFAEnabled     bool
	Status         string // "active" | "locked" | "disabled"
	FailedAttempts int
	LockedUntil    time.Time // zero value = not locked
	LastLoginAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PasswordCredential mirrors auth.passwords (one per user in v1).
type PasswordCredential struct {
	UserID        uuid.UUID
	PasswordHash  string // argon2id encoded
	HashAlgorithm string // 'argon2id'
	ChangedAt     time.Time
}

// Session mirrors auth.sessions.
type Session struct {
	TokenHash  []byte // sha256(raw_token)
	UserID     uuid.UUID
	IssuedAt   time.Time
	ExpiresAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time         // zero value = not revoked
	ClientMeta map[string]string // ip, ua, gateway_id
}

// Capability is a single granted capability row. Mirrors auth.capabilities.
type Capability struct {
	UserID     uuid.UUID
	Capability string
	// GrantedAt is set by the repository at the moment of grant or
	// re-grant. Caller-supplied values are ignored — set this read-only.
	GrantedAt time.Time
	// GrantedBy is the user_id that performed the grant. uuid.Nil is the
	// accepted sentinel for system-originated grants (e.g., bootstrap-admin).
	GrantedBy uuid.UUID
	ExpiresAt time.Time // zero value = no expiry
}

// AuditEvent mirrors auth.audit_log row inputs.
type AuditEvent struct {
	Event             string
	UserID            uuid.UUID // zero value when unknown
	UsernameAttempted string
	IPAddr            netip.Addr // zero value when unavailable
	UserAgent         string
	GatewayID         string
	Metadata          map[string]any
}

// Repository abstracts persistence. Postgres impl: pkg/services/auth/postgres.
// In-memory mock for tests: pkg/services/auth/authtest.
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

	// Capabilities
	HasCapability(ctx context.Context, userID uuid.UUID, capability string) (bool, error)
	GrantCapability(ctx context.Context, c Capability) error
	RevokeCapability(ctx context.Context, userID uuid.UUID, capability string) error
	ListCapabilities(ctx context.Context, userID uuid.UUID) ([]Capability, error)
}
