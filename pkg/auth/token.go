package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// TokenBytes is the entropy length of a session token. Not configurable
// (256 bits is the right answer; a knob invites someone to drop it).
const TokenBytes = 32

// NewToken returns a base64url-encoded random token plus its SHA-256 hash.
// The raw token is what the client sees; the hash is what the DB stores.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return s, HashToken(s), nil
}

// HashToken returns the SHA-256 hash of a base64url-encoded token.
// Deterministic; suitable as a primary key.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
