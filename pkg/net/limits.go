package net

import (
	"sync/atomic"
	"time"
)

// WireLimits bounds every attacker-controlled quantity on the client ingress
// path: how large one frame may be, how many frames a single connection may
// have queued, how many the server will process in one drain, and (for the
// decoder units that follow) how large a single wire-declared string, slice,
// byte field, nesting depth, or aggregate allocation may be.
//
// It lives in pkg/net rather than pkg/universe because the dependency runs one
// way: pkg/universe imports pkg/net, and `go list -deps ./pkg/net` returns only
// pkg/net and pkg/net/udpproto. The three enforcement sites that need these
// numbers first — the WebSocket read pump, the UDP transport's inbound router,
// and the WebSocket accept path — are all in this package and could not reach a
// type declared in pkg/universe.
//
// A zero value is not a valid limit set; call Normalized (or construct through
// DefaultWireLimits) so an unset field falls back to its default rather than
// rejecting everything.
type WireLimits struct {
	// MaxFrameBytes is the largest single inbound frame accepted on any
	// client surface, measured after the channel byte is stripped. The
	// default matches coder/websocket's own defaultReadLimit so the two
	// agree by construction instead of by coincidence.
	MaxFrameBytes int

	// MaxInputQueueDepth bounds the per-connection channel-0x00 queue.
	// inputBufferSize is only an initial capacity; this is the actual
	// bound. Reached only when nothing is draining the connection, which
	// is exactly the pre-authentication case.
	MaxInputQueueDepth int

	// MaxOpQueueDepth bounds the per-connection channel-0x01 queue.
	MaxOpQueueDepth int

	// MaxFramesPerDrain bounds how many queued frames one drain call
	// returns. The remainder stays queued (behind MaxInputQueueDepth /
	// MaxOpQueueDepth) so a burst costs a bounded amount of work per tick
	// instead of an unbounded amount in a single one.
	MaxFramesPerDrain int

	// MaxStringBytes bounds a wire-declared string length.
	MaxStringBytes int

	// MaxSliceElems bounds a wire-declared slice element count.
	MaxSliceElems int

	// MaxBytesFieldLen bounds a wire-declared []byte field length.
	MaxBytesFieldLen int

	// MaxDepth bounds struct nesting during a decode.
	MaxDepth int

	// MaxTotalAllocBytes bounds the aggregate allocation charged to one
	// decode, so many individually-legal fields cannot add up to an
	// illegal total.
	MaxTotalAllocBytes int
}

// Default limit values. Kept as named constants so the enforcement sites and
// the tests that pin them refer to the same numbers.
const (
	// DefaultMaxFrameBytes matches coder/websocket's defaultReadLimit.
	DefaultMaxFrameBytes = 32768

	defaultMaxInputQueueDepth = 256
	defaultMaxOpQueueDepth    = 64
	defaultMaxFramesPerDrain  = 64

	defaultMaxStringBytes     = 8192
	defaultMaxSliceElems      = 4096
	defaultMaxBytesFieldLen   = 65536
	defaultMaxDepth           = 16
	defaultMaxTotalAllocBytes = 1 << 20
)

// DefaultWireLimits returns the limit set applied when nothing overrides it.
func DefaultWireLimits() WireLimits {
	return WireLimits{
		MaxFrameBytes:      DefaultMaxFrameBytes,
		MaxInputQueueDepth: defaultMaxInputQueueDepth,
		MaxOpQueueDepth:    defaultMaxOpQueueDepth,
		MaxFramesPerDrain:  defaultMaxFramesPerDrain,
		MaxStringBytes:     defaultMaxStringBytes,
		MaxSliceElems:      defaultMaxSliceElems,
		MaxBytesFieldLen:   defaultMaxBytesFieldLen,
		MaxDepth:           defaultMaxDepth,
		MaxTotalAllocBytes: defaultMaxTotalAllocBytes,
	}
}

// Normalized replaces every non-positive field with its default. Config
// structs reach the runtime with zero values far more often than the flag
// defaults suggest — universe.New's `if !flag.Parsed()` guard is always false
// under `go test`, so BindFlags never runs there — and a zero limit would
// otherwise reject every frame.
func (l WireLimits) Normalized() WireLimits {
	d := DefaultWireLimits()
	if l.MaxFrameBytes <= 0 {
		l.MaxFrameBytes = d.MaxFrameBytes
	}
	if l.MaxInputQueueDepth <= 0 {
		l.MaxInputQueueDepth = d.MaxInputQueueDepth
	}
	if l.MaxOpQueueDepth <= 0 {
		l.MaxOpQueueDepth = d.MaxOpQueueDepth
	}
	if l.MaxFramesPerDrain <= 0 {
		l.MaxFramesPerDrain = d.MaxFramesPerDrain
	}
	if l.MaxStringBytes <= 0 {
		l.MaxStringBytes = d.MaxStringBytes
	}
	if l.MaxSliceElems <= 0 {
		l.MaxSliceElems = d.MaxSliceElems
	}
	if l.MaxBytesFieldLen <= 0 {
		l.MaxBytesFieldLen = d.MaxBytesFieldLen
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxTotalAllocBytes <= 0 {
		l.MaxTotalAllocBytes = d.MaxTotalAllocBytes
	}
	return l
}

// dropThrottle admits at most one log line per dropLogInterval. Every ingress
// drop counter is driven by traffic an attacker controls, so logging each one
// would make the log an amplification target — the same reasoning that already
// throttles UDPServer.noteDrop. The counter is always incremented; only the
// line is rate limited.
type dropThrottle struct {
	last atomic.Int64
}

// allow reports whether a log line may be emitted now.
func (d *dropThrottle) allow() bool {
	now := udpClockStamp(time.Now())
	previous := d.last.Load()
	if now-previous < int64(dropLogInterval) {
		return false
	}
	return d.last.CompareAndSwap(previous, now)
}
