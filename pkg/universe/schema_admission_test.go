package universe

import (
	"strings"
	"testing"
)

// stubProtocol presents a fingerprint without importing the mmokit facade,
// which pkg/universe cannot do — the facade imports this package. SetProtocol
// takes `any` and the seam is duck-typed, so this is enough.
type stubProtocol struct{ fp uint32 }

func (s stubProtocol) SchemaFingerprint() uint32 { return s.fp }

func newAdmissionServer(t *testing.T, self uint32, d Dimension) *meshControlServer {
	t.Helper()
	c := New(Config{CellsX: 1, CellsY: 1, Headless: true, Dimension: d})
	if self != 0 {
		c.SetProtocol(stubProtocol{fp: self})
	}
	return &meshControlServer{coord: c, log: c.Log}
}

// TestAdmitSchemaFingerprint covers the admission table. The two admit rows
// are the important guard: a table asserting only refusals would pass against
// a function that refused everything, which would take the whole distributed
// suite down with it.
func TestAdmitSchemaFingerprint(t *testing.T) {
	for _, c := range []struct {
		name       string
		self, peer uint32
		wantErr    bool
	}{
		// Every pkg/universe fixture builds both sides through universe.New
		// with no facade, so both are 0. This row is what keeps them working.
		{name: "both absent", self: 0, peer: 0, wantErr: false},
		{name: "equal", self: 7, peer: 7, wantErr: false},
		{name: "mismatch", self: 7, peer: 9, wantErr: true},
		// Asymmetric zero used to be ADMITTED by an escape reading
		// `self == 0 || peer == 0`. It is the shape a lost protocol-assembly
		// race produced, which is why it must refuse.
		{name: "peer absent", self: 7, peer: 0, wantErr: true},
		{name: "coordinator absent", self: 0, peer: 7, wantErr: true},
	} {
		s := newAdmissionServer(t, c.self, Dimension2D)
		err := s.admitSchemaFingerprint("host", "h1", c.peer)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: self=%d peer=%d err=%v, wantErr=%v", c.name, c.self, c.peer, err, c.wantErr)
		}
	}
}

// TestAdmitSchemaFingerprint_MessageNamesTheProfile pins the diagnostic. The
// dimension is inside the hash but unrecoverable from a 32-bit digest, so the
// coordinator can only name its OWN profile — and must, because the previous
// message sent an operator hunting a version skew that may not exist.
func TestAdmitSchemaFingerprint_MessageNamesTheProfile(t *testing.T) {
	s := newAdmissionServer(t, 7, Dimension3D)
	err := s.admitSchemaFingerprint("host", "h1", 9)
	if err == nil {
		t.Fatal("mismatched fingerprints were admitted")
	}
	msg := err.Error()
	for _, want := range []string{"3d profile", "Config.Dimension", "--dump-schema"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
	// Non-vacuity: a substring check passes trivially against a message that
	// appended the new advice while keeping the old misdiagnosis. Requiring
	// the removal is what makes this test about the fix.
	if strings.Contains(msg, "is running a different build; redeploy the cluster together") {
		t.Errorf("message still asserts a build skew as the only cause: %s", msg)
	}
}

// TestAdmitSchemaFingerprint_MissingPeerNamesTheCause — the refusal a lost
// race produces should say what to look at, not just that it failed.
func TestAdmitSchemaFingerprint_MissingPeerNamesTheCause(t *testing.T) {
	s := newAdmissionServer(t, 7, Dimension2D)
	err := s.admitSchemaFingerprint("gateway", "g1", 0)
	if err == nil {
		t.Fatal("a peer presenting no fingerprint was admitted")
	}
	if !strings.Contains(err.Error(), "presented none") {
		t.Errorf("message does not name the missing fingerprint: %v", err)
	}
}
