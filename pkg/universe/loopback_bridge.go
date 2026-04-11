package universe

import (
	"math/rand"
	"sync"
	"time"
)

// LoopbackOpts configures a LoopbackBridge.
type LoopbackOpts struct {
	// LatencyMs is the artificial delivery delay in milliseconds.
	// Zero means synchronous in-call delivery (useful for deterministic
	// unit tests that don't need to simulate real network timing).
	LatencyMs int

	// LossRate is the probability that any given message is dropped,
	// in the range [0.0, 1.0]. 0 delivers everything; 1.0 drops
	// everything; values in between drop messages independently at
	// random, seeded by Seed.
	LossRate float32

	// Seed controls the PRNG used for loss sampling. Zero means
	// time-based (non-deterministic). Non-zero values produce
	// reproducible loss patterns, which is useful for regression tests.
	Seed int64
}

// LoopbackBridge is a test-only in-process router for NodeMessage
// envelopes. Phase 7+ correctness and performance tests use it to
// stand up 2+ node meshes without a real transport. It is NOT a
// production NodeBridge — it does not satisfy that interface. It is
// a standalone helper that routes messages by destination node ID to
// registered receiver callbacks.
//
// The bridge deep-copies any mutable byte slices in the NodeMessage
// envelope (currently BorderFrame) so senders may reuse their buffers
// after Send returns. Other NodeMessage fields are struct values or
// pointer fields; callers must not mutate those after sending.
//
// Not safe for concurrent Send from multiple goroutines on the same
// bridge (tests run on the game loop goroutine and drive all traffic
// synchronously). SetReceiver is guarded by a mutex so tests can
// register receivers from setup while deliveries are in flight.
type LoopbackBridge struct {
	opts LoopbackOpts

	mu        sync.Mutex
	rand      *rand.Rand
	receivers map[string]func(NodeMessage)
}

// NewLoopbackBridge creates a loopback bridge with the given options.
// Receiver registration is separate: call SetReceiver for each node
// that should accept messages.
func NewLoopbackBridge(opts LoopbackOpts) *LoopbackBridge {
	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &LoopbackBridge{
		opts:      opts,
		rand:      rand.New(rand.NewSource(seed)),
		receivers: make(map[string]func(NodeMessage)),
	}
}

// SetReceiver registers (or replaces) the delivery callback for a
// given destination node ID. Safe to call during setup and teardown;
// not safe to call concurrently with Send on the same bridge.
func (lb *LoopbackBridge) SetReceiver(nodeID string, fn func(NodeMessage)) {
	lb.mu.Lock()
	lb.receivers[nodeID] = fn
	lb.mu.Unlock()
}

// Send routes a message from sourceNode to destNode. Applies configured
// loss (drop silently) and latency (synchronous if zero, otherwise a
// deferred goroutine wakes up after LatencyMs milliseconds).
//
// Messages with mutable byte payloads (BorderFrame) are deep-copied
// before delivery so the caller can reuse its buffers after Send
// returns. If destNode has no registered receiver, the message is
// silently dropped.
func (lb *LoopbackBridge) Send(sourceNode, destNode string, msg NodeMessage) {
	lb.mu.Lock()
	if lb.opts.LossRate > 0 && lb.rand.Float32() < lb.opts.LossRate {
		lb.mu.Unlock()
		return
	}
	recv, ok := lb.receivers[destNode]
	delay := time.Duration(lb.opts.LatencyMs) * time.Millisecond
	lb.mu.Unlock()

	if !ok {
		return
	}

	// Deep-copy mutable byte fields so the sender can reuse buffers.
	msgCopy := msg
	if msg.BorderFrame != nil {
		buf := make([]byte, len(msg.BorderFrame))
		copy(buf, msg.BorderFrame)
		msgCopy.BorderFrame = buf
	}

	if delay == 0 {
		recv(msgCopy)
		return
	}
	go func() {
		time.Sleep(delay)
		recv(msgCopy)
	}()
}
