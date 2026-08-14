// Package chat — password.go wraps argon2id for custom-channel passwords.
// Uses lighter parameters than auth's account passwords (per spec §15.5):
// channel passwords are lower-stakes (channel membership, not account
// access), so m=8192 (8 MiB), t=2, p=2 is sufficient.
package chat

import (
	"github.com/zenion/mmokit/pkg/services/auth"
)

// channelArgonParams: the lighter argon2id parameters used for chat
// channel passwords. See spec §15.5.
var channelArgonParams = auth.ArgonParams{
	Memory:      8192,
	Iterations:  2,
	Parallelism: 2,
	SaltLen:     16,
	KeyLen:      32,
}

// HashChannelPassword hashes a channel password with chat's lighter
// argon2id params.
func HashChannelPassword(password string) (string, error) {
	return auth.HashPassword(password, channelArgonParams)
}

// VerifyChannelPassword verifies a candidate password against a stored
// argon2id-encoded hash. Accepts hashes produced with any params (the
// encoded format embeds them).
func VerifyChannelPassword(password, encoded string) (bool, error) {
	return auth.VerifyPassword(password, encoded)
}
