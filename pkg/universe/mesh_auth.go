package universe

import (
	"crypto/sha256"
	"encoding/hex"
)

// clusterSecretEnvVar is the environment fallback for --cluster-secret. It is
// read in two places on purpose — BindFlags (so it becomes the flag default,
// giving flag > env > preset field) and New (because BindFlags is skipped
// under go test and for any game that calls flag.Parse itself).
const clusterSecretEnvVar = "MMO_CLUSTER_SECRET"

// clusterSecretFingerprint is the first 4 bytes of SHA-256, hex-encoded. Log
// this, never the secret itself: remote hosts install a MeshControl log
// forwarder, so a logged secret would ship over the very channel it protects.
//
// It is a fingerprint, not a credential — two processes showing the same value
// agree on the secret, which is the only operational question a log line needs
// to answer.
func clusterSecretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:4])
}
