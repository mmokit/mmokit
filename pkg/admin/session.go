package admin

import "time"

// SessionRecord describes a single admin session. Stored opaquely keyed by
// HashToken(token) — the raw token only ever exists in transit (cookie body).
type SessionRecord struct {
	Username   string
	Grants     []string // cmdsys grant patterns
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// SessionStore persists admin sessions. Implementations key by HashToken(rawToken).
type SessionStore interface {
	// Create generates a fresh opaque token via auth.NewToken, stores the
	// SessionRecord under HashToken(token), and returns both the raw token
	// (for the cookie body) and the stored hash (for caller bookkeeping).
	Create(rec SessionRecord) (token string, hash []byte, err error)
	// Lookup returns the record if the hash is valid and not expired.
	Lookup(hash []byte) (SessionRecord, bool)
	// Touch updates LastSeenAt and optionally extends ExpiresAt by slidingTTL.
	Touch(hash []byte, slidingTTL time.Duration) error
	// Delete removes a single record.
	Delete(hash []byte) error
	// DeleteExpired removes all records past their ExpiresAt.
	DeleteExpired() error
}
