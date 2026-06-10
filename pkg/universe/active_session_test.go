package universe

import (
	"testing"

	"github.com/google/uuid"
)

// IsUserSessionActive is the decision the op-channel login guard turns on:
// it must report true ONLY while a user has a LIVE session (Active==true),
// and false for an unknown user or one in the disconnected grace period.
// A wrong answer here either lets a duplicate-active login through (the
// downstream gateway rejection then Remove()s the connection mid-op-dispatch
// and the client hangs) or falsely rejects a legitimate reconnect.
func TestIsUserSessionActive(t *testing.T) {
	p := New(Config{Mode: "all"})

	uid := uuid.New()

	// Unknown user → not active.
	if p.IsUserSessionActive(uid) {
		t.Fatal("unknown user reported active")
	}

	// After a session is registered → active.
	p.registerAuthenticatedSession(uid, "alice", "gw-1", 7, "host-1", MeshCellID("cell_0_0"))
	if !p.IsUserSessionActive(uid) {
		t.Fatal("registered session not reported active")
	}

	// After a clean disconnect (grace period, Active=false) → not active, so
	// a reconnect is NOT rejected as a duplicate.
	p.notifySessionDisconnected("alice", "host-1", MeshCellID("cell_0_0"))
	if p.IsUserSessionActive(uid) {
		t.Fatal("grace-period (disconnected) session still reported active — reconnect would be wrongly rejected")
	}

	// Nil UUID is never active.
	if p.IsUserSessionActive(uuid.Nil) {
		t.Fatal("nil UUID reported active")
	}
}
