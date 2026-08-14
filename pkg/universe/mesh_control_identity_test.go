package universe

import (
	"testing"

	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/metrics"
)

// TestBindIdentity_ExactMatchOnly pins the rule every one of the twelve
// authoritative control-plane sites now goes through.
//
// The empty-claim case is the one worth stating out loud: treating "" as
// "unset, therefore fine" would let any caller bypass the whole check by
// simply not setting the field.
func TestBindIdentity_ExactMatchOnly(t *testing.T) {
	s := &meshControlServer{log: logger.New()}

	cases := []struct {
		name          string
		stream, claim string
		want          bool
	}{
		{"identical", "host-a", "host-a", true},
		{"different host", "host-a", "host-b", false},
		{"empty claim", "host-a", "", false},
		{"case differs", "host-a", "HOST-A", false},
		{"claim is a prefix", "host-abc", "host-a", false},
		{"claim is a superstring", "host-a", "host-abc", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.bindIdentity(tc.stream, tc.claim, "TestMessage"); got != tc.want {
				t.Fatalf("bindIdentity(%q, %q) = %v, want %v", tc.stream, tc.claim, got, tc.want)
			}
		})
	}
}

// TestBindIdentity_RecordsRejection pins that a mismatch is observable rather
// than a silent drop. A binding that drops without a counter turns a hostile
// peer into a mystery outage.
//
// Asserts on a delta, not an absolute: the ingress tally is process-wide by
// design, so a test binary hosting several Processes shares one.
func TestBindIdentity_RecordsRejection(t *testing.T) {
	s := &meshControlServer{log: logger.New()}

	before := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch)
	s.bindIdentity("host-a", "host-b", "TestMessage")
	after := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch)

	if after != before+1 {
		t.Fatalf("identity mismatch counter went %d -> %d, want +1", before, after)
	}

	// A match must not touch the counter.
	s.bindIdentity("host-a", "host-a", "TestMessage")
	if got := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonIdentityMismatch); got != after {
		t.Fatalf("a matching identity incremented the rejection counter (%d -> %d)", after, got)
	}
}
