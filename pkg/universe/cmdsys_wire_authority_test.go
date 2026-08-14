package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmokit/pkg/cmdsys"
)

// TestCallerProto_DropsGrants pins criterion 10's first clause at the
// conversion boundary: grants are not merely ignored on receipt, they cannot
// be expressed on the wire at all.
//
// The old shape let Grant{Pattern:"*.*"} execute any registered verb, because
// InvokeLocal's sole authority gate was a Check against these very bytes.
func TestCallerProto_DropsGrants(t *testing.T) {
	in := cmdsys.Caller{
		ID:     "operator-1",
		Source: cmdsys.SourceAdminHTTP,
		Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}},
	}

	pb := callerToProto(in)
	out := callerFromProto(pb)

	if len(out.Grants) != 0 {
		t.Fatalf("grants survived the wire round trip: %v", out.Grants)
	}
	if out.ID != "operator-1" {
		t.Fatalf("ID = %q, want it preserved for audit", out.ID)
	}
	if out.Source != cmdsys.SourceAdminHTTP {
		t.Fatalf("Source = %v, want it preserved for audit", out.Source)
	}
}

// TestWireCallerCannotSatisfyCheck is the property that makes the design safe
// rather than merely tidy: a caller reconstructed from the wire carries no
// authority, so any receive path that forgets to use InvokeAsPeer fails
// CLOSED rather than open.
func TestWireCallerCannotSatisfyCheck(t *testing.T) {
	hostile := callerFromProto(callerToProto(cmdsys.Caller{
		ID:     "attacker",
		Grants: []cmdsys.Grant{{Pattern: "*.*", Allow: true}},
	}))

	for _, capability := range []cmdsys.Capability{"admin.operator", "perf", "tune", "chat.admin"} {
		if cmdsys.Check(hostile, capability) {
			t.Fatalf("a wire-sourced caller satisfied Check(%q)", capability)
		}
	}
}

// TestClampRemoteDeadline_CapsFarFuture pins the deadline clamp. Without it a
// peer could pin a goroutine for as long as it liked: timeFromUnixNanos only
// substitutes a default when the value is <= 0.
func TestClampRemoteDeadline_CapsFarFuture(t *testing.T) {
	farFuture := time.Now().Add(48 * time.Hour).UnixNano()
	got := clampRemoteDeadline(farFuture)

	if limit := time.Now().Add(maxRemoteCommandDeadline + time.Second); got.After(limit) {
		t.Fatalf("deadline %v exceeds the %v cap", got, maxRemoteCommandDeadline)
	}

	// A sane deadline must pass through untouched.
	soon := time.Now().Add(2 * time.Second)
	if got := clampRemoteDeadline(soon.UnixNano()); !got.Equal(soon) {
		t.Fatalf("clamp altered an in-range deadline: %v != %v", got, soon)
	}
}
