package universe

import (
	"flag"
	"strings"
	"testing"
)

// TestNew_ClusterSecretFallsBackWithoutBindFlags pins the same §6.8.4 trap
// TestNew_WireLimitsFallBackWithoutBindFlags pins, for the cluster secret.
//
// New's `if !flag.Parsed()` guard is always false under `go test`, so BindFlags
// never runs and its MMO_CLUSTER_SECRET read never fires. If the env read lived
// only in BindFlags, every test fixture and every game that calls flag.Parse
// itself would silently come up with no secret.
func TestNew_ClusterSecretFallsBackWithoutBindFlags(t *testing.T) {
	if !flag.Parsed() {
		t.Fatal("flag.Parsed() is false under go test; the premise of this test " +
			"(and of New's guard) has changed")
	}

	t.Setenv(clusterSecretEnvVar, "from-env")

	c := New(Config{Mode: "all"})
	if got := c.cfg.ClusterSecret; got != "from-env" {
		t.Fatalf("ClusterSecret = %q, want %q from the env fallback in New", got, "from-env")
	}
}

// TestNew_ClusterSecretFieldBeatsEnv pins the precedence order's lower half.
// The flag > env half is exercised by BindFlags in a real process; here the
// preset field must win over the environment because a caller that set the
// field in Go was explicit about it.
func TestNew_ClusterSecretFieldBeatsEnv(t *testing.T) {
	t.Setenv(clusterSecretEnvVar, "from-env")

	c := New(Config{Mode: "all", ClusterSecret: "from-field"})
	if got := c.cfg.ClusterSecret; got != "from-field" {
		t.Fatalf("ClusterSecret = %q, want the preset field to beat the env", got)
	}
}

// TestClusterSecretFingerprint_NeverContainsTheSecret is the guard against
// someone "improving" the log line by including the value. A fingerprint that
// leaks its input is worse than no log line at all: remote hosts forward their
// logs over the very channel the secret protects.
func TestClusterSecretFingerprint_NeverContainsTheSecret(t *testing.T) {
	const secret = "super-secret-value"
	fp := clusterSecretFingerprint(secret)

	if strings.Contains(fp, secret) {
		t.Fatalf("fingerprint %q contains the secret", fp)
	}
	if len(fp) != 8 {
		t.Fatalf("fingerprint %q has length %d, want 8 hex chars (4 bytes)", fp, len(fp))
	}
	if fp == clusterSecretFingerprint(secret+"x") {
		t.Fatal("fingerprint does not distinguish different secrets")
	}
	if fp != clusterSecretFingerprint(secret) {
		t.Fatal("fingerprint is not stable across calls")
	}
}
