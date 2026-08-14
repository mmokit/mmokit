package metrics

// Ingress rejection accounting for CE-002 criterion 7.
//
// The whole point of the criterion is that the counters are BOUNDED: a
// rejection is driven by traffic an attacker chooses to send, so anything that
// allocates a new series per rejection would convert the denial-of-service fix
// into a new denial-of-service vector. That is why the reason set and the
// surface set below are closed enums indexing a fixed-size array rather than
// map keys, and why there is deliberately no connID, remote address, username
// or typeID dimension. Twenty-four counters exist from process start and
// twenty-four exist after any amount of hostile traffic.
//
// cell_metrics.go states the same rule for the client-prediction counters
// ("Plain counters, no per-player labels — the cardinality has to stay
// bounded"); this is that rule applied to the ingress path.

// IngressReason is the closed set of reasons an inbound payload was refused.
//
// Closed on purpose: adding an arm is a deliberate edit here, not something a
// call site can do by passing a new string. The String values are the label
// values in the text exposition, so they are a scrape-facing contract — renaming
// one breaks an operator's dashboards and alerts.
type IngressReason uint8

const (
	// ReasonTruncated is a read that ran past the end of the body: the
	// payload-derived half of the decoder's bounds checks. The single most
	// common shape of a malformed frame.
	ReasonTruncated IngressReason = iota
	// ReasonStringLimit is a wire-declared string length above MaxStringBytes.
	ReasonStringLimit
	// ReasonSliceLimit is a wire-declared slice element count above
	// MaxSliceElems.
	ReasonSliceLimit
	// ReasonBytesLimit is a []byte field refused: over MaxBytesFieldLen, or
	// present at all on a surface with RejectByteFields set.
	ReasonBytesLimit
	// ReasonDepth is struct nesting past MaxDepth.
	ReasonDepth
	// ReasonAllocBudget is an allocation that would exceed what remains of
	// MaxTotalAllocBytes for this decode. This is the arm that bounds
	// amplification; the others mostly bound one field.
	ReasonAllocBudget
	// ReasonTrailing is a body longer than the type it claims to be, on a
	// surface where the length is part of the contract (ReflectUnmarshalStrict).
	ReasonTrailing
	// ReasonFrameTooLarge is a frame or body over MaxFrameBytes, refused
	// before a single field is decoded.
	ReasonFrameTooLarge
	// ReasonUnknownTypeID is a well-formed frame naming a typeID with no
	// registration. Counted because a sustained rate means either an SDK/server
	// version skew or someone probing the registry.
	ReasonUnknownTypeID
	// ReasonPanicRecovered is a frame dropped by one of the ingress panic
	// barriers. Distinct from every other reason: it means something got past
	// the checked decoder, so a non-zero value is a bug report, not a peer
	// misbehaving.
	ReasonPanicRecovered
	// ReasonQueueFull is a frame refused at a per-connection queue depth cap.
	// The pre-authentication memory-exhaustion primitive CE-002 criterion 5
	// closed; a sustained rate means a peer is outrunning its drain.
	ReasonQueueFull
	// ReasonTickBudgetExhausted is work deferred to a later tick or poll pass
	// because the per-drain budget ran out. NOT a dropped frame — the frames
	// stay queued — but it is the signal that ingress is saturating the loop.
	ReasonTickBudgetExhausted
	// ReasonIdentityMismatch is a mesh frame whose payload-carried process ID
	// disagreed with the identity its stream presented at open, or a stream
	// that presented no identity at all. Non-zero means either a misrouted
	// producer or a peer claiming to be someone else; neither is normal.
	ReasonIdentityMismatch

	// numIngressReasons must stay last. It sizes the counter table.
	numIngressReasons
)

var ingressReasonNames = [numIngressReasons]string{
	ReasonTruncated:           "truncated",
	ReasonStringLimit:         "string_limit",
	ReasonSliceLimit:          "slice_limit",
	ReasonBytesLimit:          "bytes_limit",
	ReasonDepth:               "depth",
	ReasonAllocBudget:         "alloc_budget",
	ReasonTrailing:            "trailing",
	ReasonFrameTooLarge:       "frame_too_large",
	ReasonUnknownTypeID:       "unknown_type_id",
	ReasonPanicRecovered:      "panic_recovered",
	ReasonQueueFull:           "queue_full",
	ReasonTickBudgetExhausted: "tick_budget_exhausted",
	ReasonIdentityMismatch:    "identity_mismatch",
}

// String returns the scrape label value for r, or "invalid" when r is outside
// the enum. It never panics: a bad value must not be able to take down the
// scrape handler.
func (r IngressReason) String() string {
	if r >= numIngressReasons {
		return "invalid"
	}
	return ingressReasonNames[r]
}

// IngressSurface is the closed two-value split between payloads an untrusted
// remote peer supplied and payloads this cluster's own encoder produced.
//
// It matches the two limit profiles the decoder runs under (see
// pkg/universe/wire_limits.go). The split is worth its two series because the
// operator response differs: a client-surface rejection is an ordinary fact of
// exposing a port, while a mesh-surface rejection means a trusted peer, or a
// disk or link between two of them, produced something this process refused.
type IngressSurface uint8

const (
	// SurfaceClient is the untrusted ingress path: WebSocket and UDP frames,
	// typed ops, typed client input, and the host-side virtual connections
	// carrying gateway-forwarded client bytes.
	SurfaceClient IngressSurface = iota
	// SurfaceMesh is the intra-cluster path plus the client-SDK decode in
	// the reference game's bot client: border component blobs, transfer frames, service-event
	// payloads, op responses.
	SurfaceMesh

	// numIngressSurfaces must stay last. It sizes the counter table.
	numIngressSurfaces
)

var ingressSurfaceNames = [numIngressSurfaces]string{
	SurfaceClient: "client",
	SurfaceMesh:   "mesh",
}

// String returns the scrape label value for s, or "invalid" when s is outside
// the enum.
func (s IngressSurface) String() string {
	if s >= numIngressSurfaces {
		return "invalid"
	}
	return ingressSurfaceNames[s]
}

// IngressMetrics tallies refused inbound payloads across every ingress surface
// in the process.
//
// The table is a fixed [surface][reason] array of Counter, so its size is a
// compile-time constant and no rejection can ever allocate. Every method is
// safe from any goroutine (lock-free atomics) and safe on a nil receiver, which
// matters because rejection sites run on transport goroutines, on the cell loop,
// and inside panic barriers — none of them a good place to discover a nil check
// was missing.
type IngressMetrics struct {
	rejected [numIngressSurfaces][numIngressReasons]Counter
}

// RecordRejected counts one refused payload. Out-of-range arguments are
// discarded rather than panicking: this is called from inside recover barriers
// and from the decoder's error paths, and an observability call must never be
// the thing that takes a frame down.
func (m *IngressMetrics) RecordRejected(surface IngressSurface, reason IngressReason) {
	if m == nil || surface >= numIngressSurfaces || reason >= numIngressReasons {
		return
	}
	m.rejected[surface][reason].Add(1)
}

// AddRejected counts n refused payloads at once, for call sites that batch —
// a per-drain budget deferring a known number of frames, for instance.
func (m *IngressMetrics) AddRejected(surface IngressSurface, reason IngressReason, n uint64) {
	if m == nil || n == 0 || surface >= numIngressSurfaces || reason >= numIngressReasons {
		return
	}
	m.rejected[surface][reason].Add(n)
}

// Rejected reports the current count for one (surface, reason) pair.
func (m *IngressMetrics) Rejected(surface IngressSurface, reason IngressReason) uint64 {
	if m == nil || surface >= numIngressSurfaces || reason >= numIngressReasons {
		return 0
	}
	return m.rejected[surface][reason].Load()
}

// IngressRejection is one series of the rejection table.
type IngressRejection struct {
	Surface IngressSurface
	Reason  IngressReason
	Count   uint64
}

// IngressSnapshot is a read-consistent-enough view of the whole table.
//
// Every series is present, including the zero ones. That is deliberate: a
// counter that exists but has never fired must still be scrapeable, or an alert
// on it cannot distinguish "no rejections" from "this build does not have that
// counter". The slice length is a compile-time constant
// (numIngressSurfaces * numIngressReasons), so a scrape allocates a fixed amount
// no matter what traffic the process has seen.
type IngressSnapshot struct {
	Rejections []IngressRejection
}

// Snapshot returns every series in a stable (surface, reason) order.
// Allocates, and is meant for scrape intervals, not the hot path.
func (m *IngressMetrics) Snapshot() IngressSnapshot {
	out := IngressSnapshot{
		Rejections: make([]IngressRejection, 0, int(numIngressSurfaces)*int(numIngressReasons)),
	}
	for s := IngressSurface(0); s < numIngressSurfaces; s++ {
		for r := IngressReason(0); r < numIngressReasons; r++ {
			out.Rejections = append(out.Rejections, IngressRejection{
				Surface: s,
				Reason:  r,
				Count:   m.Rejected(s, r),
			})
		}
	}
	return out
}

// Total reports every rejection the process has counted, across all surfaces
// and reasons. Used by tests that only need to prove the table moved.
func (m *IngressMetrics) Total() uint64 {
	var total uint64
	for s := IngressSurface(0); s < numIngressSurfaces; s++ {
		for r := IngressReason(0); r < numIngressReasons; r++ {
			total += m.Rejected(s, r)
		}
	}
	return total
}

// processIngress is the one ingress tally per process.
//
// A package-level singleton rather than a field on universe.Process, because
// the rejection sites genuinely cannot all reach a Process: the WebSocket read
// pump and the UDP transport queues live in pkg/net, the op poll loop lives in
// pkg/ops, and the reflection decoder is reached through free functions
// (ReflectUnmarshal) whose Stage argument is nil on every mesh path. Threading a
// handle to all of them would put a pointer on every Conn and every decode
// frame to observe something that is process-wide by nature.
//
// The precedent is deliberate and local: pkg/universe/reflect_marshal.go already
// keeps marshalDrops and decodeDrops as package-level dropNoters for exactly
// this reason. The cost is that a test binary hosting several Processes shares
// one tally — which is why the tests here and in pkg/universe assert on deltas
// rather than absolute values.
var processIngress IngressMetrics

// Ingress returns the process-wide ingress tally. Every rejection site in the
// engine records through it, and Handler scrapes it.
func Ingress() *IngressMetrics { return &processIngress }

// UDPDropSnapshot carries the client UDP listener's packet-level refusal
// counters. Separate from IngressMetrics because these are refused before any
// frame exists to have a surface or a decode reason: they are the handshake and
// source-address checks in pkg/net/udp_server.go, not the decoder.
//
// Bounded by construction for the same reason as everything above — aggregate
// counters with no per-peer or per-token breakdown.
type UDPDropSnapshot struct {
	Bound               bool   // false when no UDP listener is running
	SourceMismatchDrops uint64 // packet source address did not match the session's
	CapacityDrops       uint64 // session table full
	PendingFullDrops    uint64 // pending-handshake table full
	PendingCount        uint64 // pending handshakes right now (a gauge)
}

// ProcessSnapshot is the process-scoped half of a scrape: counters that belong
// to the process rather than to any one cell.
//
// It exists because Handler's original input — map[cellID]LoadSnapshot — has
// nowhere to put a process-wide number, so a gateway-role-only process (which
// owns no cells) served a /metrics body with header comments and not one sample.
type ProcessSnapshot struct {
	Ingress IngressSnapshot
	UDP     UDPDropSnapshot
}
