package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The property CE-002 criterion 7 actually turns on: the number of series is a
// compile-time constant, not a function of how much traffic arrived. If a
// future edit turns the table into a map keyed on anything peer-supplied, this
// is the test that should fail.
func TestIngressMetrics_CardinalityIsFixed(t *testing.T) {
	var m IngressMetrics

	before := len(m.Snapshot().Rejections)
	for range 10000 {
		m.RecordRejected(SurfaceClient, ReasonTruncated)
		m.RecordRejected(SurfaceMesh, ReasonQueueFull)
	}
	after := len(m.Snapshot().Rejections)

	if before != after {
		t.Fatalf("series count grew from %d to %d under load; the cardinality is not bounded", before, after)
	}
	if want := int(numIngressSurfaces) * int(numIngressReasons); after != want {
		t.Fatalf("snapshot carries %d series, want exactly %d (%d surfaces x %d reasons)",
			after, want, numIngressSurfaces, numIngressReasons)
	}
	if got := m.Rejected(SurfaceClient, ReasonTruncated); got != 10000 {
		t.Fatalf("client/truncated = %d, want 10000", got)
	}
	if got := m.Rejected(SurfaceMesh, ReasonTruncated); got != 0 {
		t.Fatalf("mesh/truncated = %d, want 0 — the surface split is not separating counts", got)
	}
}

// Every enum value must have a distinct, non-empty label. A duplicate or an
// empty string would collapse two series into one on a scrape, silently.
func TestIngressReason_LabelsAreDistinct(t *testing.T) {
	seen := make(map[string]IngressReason, numIngressReasons)
	for r := IngressReason(0); r < numIngressReasons; r++ {
		name := r.String()
		if name == "" || name == "invalid" {
			t.Fatalf("reason %d has label %q; every enum value needs a real label", r, name)
		}
		if prev, dup := seen[name]; dup {
			t.Fatalf("reasons %d and %d share the label %q", prev, r, name)
		}
		seen[name] = r
	}
	for s := IngressSurface(0); s < numIngressSurfaces; s++ {
		if name := s.String(); name == "" || name == "invalid" {
			t.Fatalf("surface %d has label %q", s, name)
		}
	}
	// Out-of-range values must not panic — the exposition writer calls these
	// on whatever it is handed.
	if got := IngressReason(200).String(); got != "invalid" {
		t.Fatalf("out-of-range reason label = %q, want %q", got, "invalid")
	}
	if got := IngressSurface(200).String(); got != "invalid" {
		t.Fatalf("out-of-range surface label = %q, want %q", got, "invalid")
	}
}

// Rejection sites run on transport goroutines, on the cell loop, and inside
// recover barriers. None of them is a place to discover a nil check was missing.
func TestIngressMetrics_NilReceiverIsSafe(t *testing.T) {
	var m *IngressMetrics
	m.RecordRejected(SurfaceClient, ReasonDepth)
	m.AddRejected(SurfaceMesh, ReasonTrailing, 7)
	if got := m.Rejected(SurfaceClient, ReasonDepth); got != 0 {
		t.Fatalf("nil receiver reported %d", got)
	}
	if got := m.Total(); got != 0 {
		t.Fatalf("nil receiver Total = %d", got)
	}
	if got := len(m.Snapshot().Rejections); got != int(numIngressSurfaces)*int(numIngressReasons) {
		t.Fatalf("nil receiver Snapshot returned %d series", got)
	}
}

// An out-of-range argument must be dropped, not indexed. RecordRejected is
// called from inside panic barriers, where panicking would defeat the barrier.
func TestIngressMetrics_OutOfRangeArgsAreDiscarded(t *testing.T) {
	var m IngressMetrics
	m.RecordRejected(IngressSurface(99), ReasonTruncated)
	m.RecordRejected(SurfaceClient, IngressReason(99))
	m.AddRejected(IngressSurface(99), IngressReason(99), 5)
	if got := m.Total(); got != 0 {
		t.Fatalf("Total = %d after only out-of-range records, want 0", got)
	}
}

// The exposition must emit every series including the zeros, so an alert can
// tell "no rejections" from "this build has no such counter".
func TestHandler_EmitsEveryIngressSeries(t *testing.T) {
	var m IngressMetrics
	m.RecordRejected(SurfaceClient, ReasonSliceLimit)

	h := Handler(
		func() map[string]LoadSnapshot { return nil },
		func() ProcessSnapshot { return ProcessSnapshot{Ingress: m.Snapshot()} },
	)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for r := IngressReason(0); r < numIngressReasons; r++ {
		for s := IngressSurface(0); s < numIngressSurfaces; s++ {
			want := "mmokit_ingress_rejected_total{reason=\"" + r.String() + "\",surface=\"" + s.String() + "\"}"
			if !strings.Contains(body, want) {
				t.Errorf("exposition is missing %s", want)
			}
		}
	}
	if !strings.Contains(body, `mmokit_ingress_rejected_total{reason="slice_limit",surface="client"} 1`) {
		t.Errorf("recorded rejection did not reach the exposition:\n%s", body)
	}
	// No listener bound => no UDP family at all, rather than a fabricated
	// row of zeros claiming a clean UDP surface.
	if strings.Contains(body, "mmokit_udp_packets_dropped_total") {
		t.Error("UDP family emitted with UDPDropSnapshot.Bound false")
	}
}

// A nil processFn must leave the cell-scoped output exactly as it was, so a
// game wiring metrics.Handler itself is not broken by the new half.
func TestHandler_NilProcessFnOmitsProcessFamilies(t *testing.T) {
	h := Handler(func() map[string]LoadSnapshot { return nil }, nil)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if strings.Contains(body, "mmokit_ingress_rejected_total") {
		t.Error("ingress family emitted with a nil processFn")
	}
	if !strings.Contains(body, "mmokit_tick_duration_seconds") {
		t.Error("cell-scoped families disappeared when processFn was nil")
	}
}

func TestHandler_EmitsUDPDropsWhenBound(t *testing.T) {
	h := Handler(
		func() map[string]LoadSnapshot { return nil },
		func() ProcessSnapshot {
			return ProcessSnapshot{UDP: UDPDropSnapshot{
				Bound:                true,
				SourceMismatchDrops:  3,
				CapacityDrops:        5,
				HandshakeRejectDrops: 7,
			}}
		},
	)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		`mmokit_udp_packets_dropped_total{reason="source_mismatch"} 3`,
		`mmokit_udp_packets_dropped_total{reason="capacity"} 5`,
		`mmokit_udp_packets_dropped_total{reason="handshake_reject"} 7`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition is missing %q", want)
		}
	}
}
