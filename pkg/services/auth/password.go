package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ArgonParams configures argon2id hashing. Defaults track OWASP 2024.
type ArgonParams struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

func DefaultArgonParams() ArgonParams {
	return ArgonParams{Memory: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLen: 16, KeyLen: 32}
}

// HashPassword returns the encoded argon2id hash of password.
func HashPassword(password string, p ArgonParams) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword returns true if the password matches the encoded hash.
func VerifyPassword(password, encoded string) (bool, error) {
	p, salt, hash, err := decodeArgon2(encoded)
	if err != nil {
		return false, err
	}
	cmp := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
	return subtle.ConstantTimeCompare(hash, cmp) == 1, nil
}

// HashUsesParams reports whether encoded was produced with the given params.
// Use to decide whether to re-hash on verify.
func HashUsesParams(encoded string, p ArgonParams) bool {
	got, _, _, err := decodeArgon2(encoded)
	if err != nil {
		return false
	}
	return got == p
}

func decodeArgon2(encoded string) (ArgonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ArgonParams{}, nil, nil, errors.New("auth: bad argon2 encoded form")
	}
	var ver int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil {
		return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad version: %w", err)
	}
	if ver != argon2.Version {
		return ArgonParams{}, nil, nil, fmt.Errorf("auth: argon2 version mismatch: %d", ver)
	}
	var p ArgonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad salt: %w", err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad hash: %w", err)
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(hash))
	return p, salt, hash, nil
}
