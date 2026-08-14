package universe

import (
	"encoding/binary"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/metrics"
	pkgnet "github.com/zenion/mmokit/pkg/net"
)

// ── Scrape reachability ──────────────────────────────────────────────────────

// sampleLines returns the exposition lines that are actual samples — comments
// and blanks removed. The distinction is the whole point of this file: the
// handler has always emitted "# HELP"/"# TYPE" headers unconditionally, so a
// body-is-non-empty check would have passed on HEAD while the scrape carried
// no data at all.
func sampleLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// A gateway-role-only process owns no cells, so metrics.Handler's original
// input — map[cellID]LoadSnapshot — was empty on every scrape and the endpoint
// served header comments and nothing else. That is the process that terminates
// every client connection in a distributed deployment: the one whose ingress
// numbers an operator most needs.
//
// This is CE-002 criterion 7's actual acceptance test. Counters that exist but
// are unreachable are what §6.8.4 warns about (RecordInputAckFrame's reader has
// no callers to this day), so the assertion is on the scrape, not on the field.
func TestHandler_GatewayOnlyProcessEmitsIngressSeries(t *testing.T) {
	p := &Process{
		ConnMgr: pkgnet.NewConnManager(),
		Log:     logger.New(),
		roles:   Roles{RoleGateway: {}},
	}

	rec := httptest.NewRecorder()
	p.MetricsHandler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	samples := sampleLines(body)
	if len(samples) == 0 {
		t.Fatalf("gateway-only /metrics carries no samples at all, only headers:\n%s", body)
	}
	if !strings.Contains(body, "mmokit_ingress_rejected_total{") {
		t.Fatalf("gateway-only /metrics has no ingress series:\n%s", body)
	}

	// Every sample this process can serve must be an ingress one — it owns no
	// cells, so a cell-scoped sample here would mean the fixture is lying about
	// its roles rather than that the endpoint works.
	for _, line := range samples {
		if !strings.HasPrefix(line, "mmokit_ingress_rejected_total") {
			t.Fatalf("unexpected non-ingress sample on a cell-less process: %q", line)
		}
	}

	// No UDP listener bound, so that family must be absent rather than a
	// fabricated row of zeros claiming a clean UDP surface.
	if strings.Contains(body, "mmokit_udp_packets_dropped_total") {
		t.Errorf("UDP family emitted on a process with no UDP listener:\n%s", body)
	}
}

// A rejection recorded anywhere in the process must reach the scrape. Without
// this, the previous test would pass on a handler that emits a constant table
// of zeros.
func TestHandler_RecordedRejectionReachesTheScrape(t *testing.T) {
	p := &Process{
		ConnMgr: pkgnet.NewConnManager(),
		Log:     logger.New(),
		roles:   Roles{RoleGateway: {}},
	}
	read := func() uint64 {
		rec := httptest.NewRecorder()
		p.MetricsHandler()(rec, httptest.NewRequest("GET", "/metrics", nil))
		for _, line := range sampleLines(rec.Body.String()) {
			const prefix = `mmokit_ingress_rejected_total{reason="depth",surface="mesh"} `
			if strings.HasPrefix(line, prefix) {
				var v uint64
				for _, c := range strings.TrimPrefix(line, prefix) {
					v = v*10 + uint64(c-'0')
				}
				return v
			}
		}
		t.Fatal("the mesh/depth series is not in the exposition")
		return 0
	}

	before := read()
	metrics.Ingress().RecordRejected(metrics.SurfaceMesh, metrics.ReasonDepth)
	if after := read(); after != before+1 {
		t.Fatalf("scraped mesh/depth went %d -> %d, want +1", before, after)
	}
}

// ── Reason classification ────────────────────────────────────────────────────

type clsString struct{ S string }
type clsSlice struct{ V []uint32 }
type clsBytes struct{ B []byte }
type clsFixed struct{ N uint32 }

// Each malformed body must land on the reason that names why it was refused,
// not on a catch-all. A metric whose reasons are all "truncated" tells an
// operator nothing an error log did not already.
//
// Asserted as deltas: the tally is process-wide by design (see
// metrics.Ingress), so a test binary running many Processes shares it.
func TestDecoder_RejectionsAreClassifiedByReason(t *testing.T) {
	client := clientProfile(pkgnet.WireLimits{})

	cases := []struct {
		name   string
		reason metrics.IngressReason
		run    func()
	}{
		{
			name:   "truncated",
			reason: metrics.ReasonTruncated,
			// Declares a 4-byte string and supplies none of it.
			run: func() {
				var out clsString
				_ = ReflectUnmarshalStrict(nil, []byte{0x04, 0x00}, &out, client)
			},
		},
		{
			name:   "string_limit",
			reason: metrics.ReasonStringLimit,
			// Under the configured ceiling rather than under the body length:
			// the declared length is legal for the body, only too long to accept.
			run: func() {
				lim := client
				lim.MaxStringBytes = 4
				body := append([]byte{0x08, 0x00}, make([]byte, 8)...)
				var out clsString
				_ = ReflectUnmarshalStrict(nil, body, &out, lim)
			},
		},
		{
			name:   "slice_limit",
			reason: metrics.ReasonSliceLimit,
			run: func() {
				lim := client
				lim.MaxSliceElems = 1
				body := append([]byte{0x04, 0x00}, make([]byte, 16)...)
				var out clsSlice
				_ = ReflectUnmarshalStrict(nil, body, &out, lim)
			},
		},
		{
			name:   "bytes_limit",
			reason: metrics.ReasonBytesLimit,
			// RejectByteFields is set on every client profile, so the arm is
			// refused on the field's existence.
			run: func() {
				var out clsBytes
				_ = ReflectUnmarshalStrict(nil, []byte{0, 0, 0, 0}, &out, client)
			},
		},
		{
			name:   "alloc_budget",
			reason: metrics.ReasonAllocBudget,
			run: func() {
				lim := client
				lim.MaxTotalAllocBytes = 2
				body := append([]byte{0x08, 0x00}, make([]byte, 8)...)
				var out clsString
				_ = ReflectUnmarshalStrict(nil, body, &out, lim)
			},
		},
		{
			name:   "trailing",
			reason: metrics.ReasonTrailing,
			// A perfectly well-formed clsFixed plus four surplus bytes: only
			// the strict wrapper's consumed-bytes check can refuse this.
			run: func() {
				var out clsFixed
				_ = ReflectUnmarshalStrict(nil, make([]byte, 8), &out, client)
			},
		},
		{
			name:   "frame_too_large",
			reason: metrics.ReasonFrameTooLarge,
			run: func() {
				lim := client
				lim.MaxFrameBytes = 4
				var out clsFixed
				_ = ReflectUnmarshalStrict(nil, make([]byte, 64), &out, lim)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshotClientReasons()
			tc.run()
			after := snapshotClientReasons()

			if got := after[tc.reason] - before[tc.reason]; got != 1 {
				t.Fatalf("client/%s moved by %d, want exactly 1", tc.reason, got)
			}
			// Nothing else on the same surface may move, or the classification
			// is a coincidence rather than a decision.
			for r := range after {
				if r == tc.reason {
					continue
				}
				if d := after[r] - before[r]; d != 0 {
					t.Errorf("client/%s also moved by %d; the refusal was classified twice", r, d)
				}
			}
		})
	}
}

// snapshotClientReasons reads every client-surface counter as a map keyed by
// reason. Only the client surface: a background cell loop from an earlier test
// could still be decoding mesh traffic, and this test has no business asserting
// on that.
func snapshotClientReasons() map[metrics.IngressReason]uint64 {
	out := make(map[metrics.IngressReason]uint64)
	for _, rej := range metrics.Ingress().Snapshot().Rejections {
		if rej.Surface == metrics.SurfaceClient {
			out[rej.Reason] = rej.Count
		}
	}
	return out
}

// The surface split has to be real, not decorative: the same malformed bytes
// through the tolerant wrapper must land on mesh, not client.
func TestDecoder_ToleratantWrapperRecordsMeshSurface(t *testing.T) {
	beforeClient := snapshotClientReasons()
	before := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonTruncated)

	var out clsString
	if err := ReflectUnmarshal([]byte{0x04, 0x00}, &out); err == nil {
		t.Fatal("tolerant wrapper accepted a truncated string body")
	}

	if got := metrics.Ingress().Rejected(metrics.SurfaceMesh, metrics.ReasonTruncated); got != before+1 {
		t.Fatalf("mesh/truncated went %d -> %d, want +1", before, got)
	}
	afterClient := snapshotClientReasons()
	for r := range afterClient {
		if d := afterClient[r] - beforeClient[r]; d != 0 {
			t.Errorf("client/%s moved by %d on a mesh-surface decode", r, d)
		}
	}
}

// ── Non-decode rejection sites ───────────────────────────────────────────────

// The queue cap is CE-002 criterion 5's control, and unit 5 shipped it with a
// counter that only the tests read. It has to reach the same table.
func TestVCMQueueDrop_RecordsQueueFull(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	limits := pkgnet.DefaultWireLimits()
	limits.MaxInputQueueDepth = 2
	limits.MaxFrameBytes = 16
	vcm.SetWireLimits(limits)
	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-metrics", ConnID: 1}, "m", 0, "cell_0_0")

	beforeQueue := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonQueueFull)
	beforeSize := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonFrameTooLarge)

	for range 5 {
		vcm.appendChannel(localID, []byte{1}, pkgnet.ChannelEvent)
	}
	vcm.appendChannel(localID, make([]byte, limits.MaxFrameBytes+1), pkgnet.ChannelEvent)

	if got := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonQueueFull) - beforeQueue; got != 3 {
		t.Errorf("queue_full moved by %d, want 3 (5 appended into a depth-2 queue)", got)
	}
	if got := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonFrameTooLarge) - beforeSize; got != 1 {
		t.Errorf("frame_too_large moved by %d, want 1", got)
	}
}

// An unregistered typeID is a well-formed frame naming something this build
// does not have. Sustained, it is a version skew or a registry probe, and it is
// invisible on a scrape unless it is counted here.
func TestClientInput_UnknownTypeIDIsCounted(t *testing.T) {
	prev := ClientInputHooks.TypeOfTypeID
	ClientInputHooks.TypeOfTypeID = func(uint32) reflect.Type { return nil }
	t.Cleanup(func() { ClientInputHooks.TypeOfTypeID = prev })

	cell := newTestCell("unknown-typeid", CellID{X: 0, Y: 0})
	connMgr := cell.Engine.ConnMgr.(*pkgnet.ConnManager)
	tr := &countingTransport{}
	frame := make([]byte, clientInputHeaderBytes)
	binary.LittleEndian.PutUint32(frame[0:4], 0xFEED0001)
	tr.input = append(tr.input, frame)
	connID := connMgr.AddTransport(tr, "")
	cell.Engine.Players.RegisterSessionTransfer(connID, "u", "active", nil)

	before := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonUnknownTypeID)
	cell.Stage.DispatchClientInput()
	if got := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonUnknownTypeID) - before; got != 1 {
		t.Fatalf("unknown_type_id moved by %d, want 1", got)
	}
}
