package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ResolvedSession is the result of a successful Resolve call.
type ResolvedSession struct {
	UserID    uuid.UUID
	Username  string
	ExpiresAt time.Time
}

// Resolver is the typed Go path for validating a session token outside
// the op-channel handler flow. The gateway uses it on WS upgrade.
type Resolver interface {
	Resolve(ctx context.Context, token string) (*ResolvedSession, error)
}

// ErrTokenInvalid is returned when the token is unknown / revoked /
// expired. Callers should clear the cookie and treat the connection as
// unauthenticated.
var ErrTokenInvalid = errors.New("auth: token invalid")
