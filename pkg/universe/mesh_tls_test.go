package universe

import (
	"crypto/tls"
	"testing"
)

// testMeshTLS returns a fresh in-memory mesh server TLS config for tests that
// construct a HostNetwork directly rather than through a Process. It goes
// through meshServerTLSConfigFrom so the test posture cannot drift from
// production's.
func testMeshTLS(t *testing.T) *tls.Config {
	t.Helper()
	cert, err := generateDevCert()
	if err != nil {
		t.Fatalf("generateDevCert: %v", err)
	}
	return meshServerTLSConfigFrom(cert)
}
