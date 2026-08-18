package universe

import (
	"encoding/binary"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/metrics"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
)

// loadPing is the typed client-input message the sustained-ingress test floods
// with. The padding field is not decoration: with an 8-byte body the difference
// between a bounded and an unbounded queue is a few hundred kilobytes, which
// heap noise swallows, and the memory clause of the gate would pass whether or
// not the caps existed. At half a kilobyte per frame the two outcomes are
// megabytes apart.
type loadPing struct {
	Seq uint32
	Pad string
}

const (
	loadPingTypeID  uint32 = 0x10AD1234
	loadPingPadding        = 480
)

// loadPingFrame builds one channel-0x00 entry: [u32 typeID][u32 bodyLen][body],
// where body is loadPing encoded by the same codec the server decodes with.
// bodyLen is honest, so the frame walker stays aligned and the entry reaches
// ReflectUnmarshalStrict — the point is to drive the real dispatch path at
// volume, not to measure the rejection path.
func loadPingFrame(tb testing.TB, seq uint32) []byte {
	tb.Helper()
	body := mustMarshal(tb, &loadPing{Seq: seq, Pad: string(make([]byte, loadPingPadding))})
	frame := make([]byte, clientInputHeaderBytes+len(body))
	binary.LittleEndian.PutUint32(frame[0:4], loadPingTypeID)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(body)))
	copy(frame[clientInputHeaderBytes:], body)
	return frame
}

// heapAllocBytes forces a collection and reports live heap. Called only outside
// the timed sections — a GC pause inside one would be attributed to tick work.
func heapAllocBytes() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// TestIngress_SustainedLoadBoundedAndRecovers is the direct verification gate
// for CE-002 criterion 5 — roadmap §6.7's "load tests asserting bounded memory,
// queue depth, tick work, and recovery after backpressure", the one gate in that
// list not blocked on CI.
//
// Unit 5's tests are all single-shot: they prove a cap refuses the frame that
// crosses it. None of them says what happens when a peer sits on that cap for
// hundreds of frames, and none says the server comes back afterwards. This
// drives 16 connections at 4x their per-tick drain allowance for 40 ticks,
// stops, and then asserts one property per clause of the gate.
//
// The flood surface is the VirtualConnManager: the host-side queue that
// gateway-forwarded client bytes land in, which unit 5's commit message calls
// "the worst of the three" because the gateway forwards at WebSocket read speed
// over a 16 MiB gRPC channel while the host drains once per 50 ms tick.
func TestIngress_SustainedLoadBoundedAndRecovers(t *testing.T) {
	const (
		conns             = 16
		framesPerConnTick = 32
		floodTicks        = 40
		warmupTicks       = 8

		queueDepth    = 64
		framesPerTick = 8 // per connection, per drain

		// The 20 Hz cell loop's whole budget. DispatchClientInput runs inside
		// gl.tick and has no slack of its own, so this is the number that
		// matters: exceed it and ingress alone has blown the tick.
		loopBudget = 50 * time.Millisecond

		// Bounded queues retain conns*queueDepth frames forever; unbounded ones
		// would grow by conns*framesPerConnTick*floodTicks. At ~0.5 kB a frame
		// that is roughly 0.5 MiB against roughly 10 MiB.
		heapGrowthLimit = 2 << 20
	)

	// A real registered type, so each frame runs the full production path:
	// frame walk, strict decode under the client profile, dispatcher invoke.
	bindClientInputTypeID(t, loadPingTypeID, reflect.TypeFor[loadPing]())

	cell := newTestCell("ingress-load", CellID{X: 0, Y: 0})

	limits := pkgnet.DefaultWireLimits()
	limits.MaxInputQueueDepth = queueDepth
	limits.MaxFramesPerDrain = framesPerTick
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	vcm.SetWireLimits(limits)
	cell.Engine.ConnMgr = vcm

	// Count what actually reached a handler, so the accounting identity below
	// is measured end to end rather than inferred from the drain calls.
	var dispatched int
	dispatcher := cell.Stage.Dispatcher()
	dispatcher.SetEntityCtor(func(*Stage, uint32) any { return struct{}{} })
	dispatcher.Register(
		reflect.TypeFor[loadPing]().String(),
		reflect.TypeFor[loadPing](),
		reflect.ValueOf(func(struct{}, *loadPing) { dispatched++ }),
	)

	localIDs := make([]uint32, 0, conns)
	for i := range conns {
		username := fmt.Sprintf("flood-%d", i)
		localID := vcm.RegisterSession(
			SessionKey{GatewayID: "gw-load", ConnID: uint32(i + 1)}, username, 0, "cell_0_0")
		cell.Engine.Players.RegisterSessionTransfer(localID, username, "active", nil)
		sess := cell.Engine.Players.ByConnID(localID)
		if sess == nil {
			t.Fatalf("session %d did not register", localID)
		}
		cell.Stage.SpawnPlayer(sess)
		localIDs = append(localIDs, localID)
	}

	frame := loadPingFrame(t, 1)
	dropsAtStart := vcm.InputDrops()

	// ── Sustained flood ──────────────────────────────────────────────────
	var (
		maxTick     time.Duration
		heapBase    uint64
		appended    int
		budgetStart = metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonTickBudgetExhausted)
	)
	for tick := range floodTicks {
		for _, id := range localIDs {
			for range framesPerConnTick {
				// A fresh copy per frame: appendChannel retains the slice, so
				// sharing one would make the queue look free.
				vcm.appendChannel(id, append([]byte(nil), frame...), pkgnet.ChannelEvent)
				appended++
			}
		}

		start := time.Now()
		cell.Stage.DispatchClientInput()
		if d := time.Since(start); d > maxTick {
			maxTick = d
		}

		// Baseline taken once the queues have long since reached their cap, so
		// what follows measures STEADY STATE rather than the fill.
		if tick == warmupTicks {
			heapBase = heapAllocBytes()
		}
	}
	heapEnd := heapAllocBytes()
	droppedDuringFlood := vcm.InputDrops() - dropsAtStart

	// ── Clause 1: bounded memory ─────────────────────────────────────────
	if heapEnd > heapBase && heapEnd-heapBase > heapGrowthLimit {
		t.Errorf("steady-state heap grew %d bytes over %d flooded ticks (limit %d) — "+
			"the queues are not bounded",
			heapEnd-heapBase, floodTicks-warmupTicks, heapGrowthLimit)
	}

	// ── Clause 2: queue depth, with the drops accounting for the rest ────
	//
	// Every connection is fed 4x its drain allowance, so each queue sits at the
	// cap. The exact residual depends on how many connections the final tick
	// got to before its own budget, so it is bracketed rather than pinned:
	// anything in this band is "at the cap", and an uncapped queue would be an
	// order of magnitude above it.
	residual := drainAll(vcm, localIDs)
	minResidual := conns * (queueDepth - framesPerTick)
	if residual < minResidual || residual > conns*queueDepth {
		t.Errorf("queues held %d frames after the flood, want between %d and %d "+
			"(%d connections pinned at a depth-%d cap)",
			residual, minResidual, conns*queueDepth, conns, queueDepth)
	}
	if droppedDuringFlood == 0 {
		t.Error("no frames were dropped during the flood; the depth cap never engaged")
	}
	if got, want := dispatched+int(droppedDuringFlood)+residual, appended; got != want {
		t.Errorf("accounting: dispatched(%d) + dropped(%d) + queued(%d) = %d, want %d appended — "+
			"frames went missing without being counted",
			dispatched, droppedDuringFlood, residual, got, want)
	}

	// ── Clause 3: tick work ──────────────────────────────────────────────
	if maxTick > loopBudget {
		t.Errorf("worst client-input drain took %v, over the %v loop budget", maxTick, loopBudget)
	}
	// The per-drain cap is what holds clause 3 up, so assert it was engaged
	// rather than merely present: 32 frames arrived per connection per tick and
	// at most 8 may leave.
	if perTick := dispatched / floodTicks; perTick > conns*framesPerTick {
		t.Errorf("dispatched %d frames per tick, above the %d the per-drain cap allows — "+
			"MaxFramesPerDrain is not engaged",
			perTick, conns*framesPerTick)
	}
	if budgetHits := metrics.Ingress().Rejected(metrics.SurfaceClient, metrics.ReasonTickBudgetExhausted) - budgetStart; budgetHits > 0 {
		t.Logf("per-tick frame/time budget deferred players %d times during the flood", budgetHits)
	}

	// ── Clause 4: recovery after backpressure ────────────────────────────
	//
	// The flood has stopped. Nothing more is appended, so a server that has
	// merely survived is not enough: the backlog has to drain completely, at
	// the per-drain rate, and no further frame may be refused.
	dispatcher2 := dispatched
	dropsBeforeRecovery := vcm.InputDrops()
	refillOnce(t, vcm, localIDs, frame, queueDepth)
	appended += conns * queueDepth

	// queueDepth/framesPerTick ticks is the arithmetic minimum; the slack
	// absorbs a per-tick budget deferral without turning a timing wobble into
	// a failure.
	recoveryBudget := (queueDepth/framesPerTick)*3 + 4
	firstTickDispatched := 0
	ticksToDrain := 0
	for tick := range recoveryBudget {
		before := dispatched
		cell.Stage.DispatchClientInput()
		if tick == 0 {
			firstTickDispatched = dispatched - before
		}
		if queuedFrames(vcm, localIDs) == 0 {
			ticksToDrain = tick + 1
			break
		}
	}

	if ticksToDrain == 0 {
		t.Fatalf("queues still held %d frames after %d recovery ticks; the drain never caught up",
			queuedFrames(vcm, localIDs), recoveryBudget)
	}
	if firstTickDispatched == 0 {
		t.Error("the first tick after the flood dispatched nothing; the drain did not resume")
	}
	if firstTickDispatched > conns*framesPerTick {
		t.Errorf("the first recovery tick dispatched %d frames, above the %d the per-drain cap allows",
			firstTickDispatched, conns*framesPerTick)
	}
	if got := vcm.InputDrops() - dropsBeforeRecovery; got != 0 {
		t.Errorf("%d frames were still dropped after the flood stopped; backpressure did not clear", got)
	}
	if got, want := dispatched-dispatcher2, conns*queueDepth; got != want {
		t.Errorf("recovery dispatched %d frames, want the %d that were queued", got, want)
	}
}

// refillOnce tops every connection's queue back up to exactly n frames, so the
// recovery phase starts from a known backlog rather than from whatever the
// final flood tick happened to leave. Fails the test if any frame is refused —
// the queues were drained first, so there is room for all of them.
func refillOnce(tb testing.TB, vcm *VirtualConnManager, ids []uint32, frame []byte, n int) {
	tb.Helper()
	before := vcm.InputDrops()
	for _, id := range ids {
		for range n {
			vcm.appendChannel(id, append([]byte(nil), frame...), pkgnet.ChannelEvent)
		}
	}
	if got := vcm.InputDrops() - before; got != 0 {
		tb.Fatalf("refill dropped %d frames into empty depth-%d queues", got, n)
	}
}

// drainAll empties every queue and reports how many frames it took, calling
// DrainInput repeatedly because one call yields at most MaxFramesPerDrain.
func drainAll(vcm *VirtualConnManager, ids []uint32) int {
	total := 0
	for _, id := range ids {
		for {
			msgs := vcm.DrainInput(id)
			if len(msgs) == 0 {
				break
			}
			total += len(msgs)
		}
	}
	return total
}

// queuedFrames reports the current backlog without consuming it. The recovery
// loop needs a non-destructive reading: measuring by draining would do the
// drain's job for it and the test would pass on a server whose tick never
// caught up at all.
//
// Reads the session queues directly, which only an in-package test can do.
// Sessions are collected under v.mu and their queue locks taken after it is
// released, matching appendChannel's order so this cannot invert against it.
func queuedFrames(vcm *VirtualConnManager, ids []uint32) int {
	vcm.mu.RLock()
	sessions := make([]*virtualSession, 0, len(ids))
	for _, id := range ids {
		if sess := vcm.byLocal[id]; sess != nil {
			sessions = append(sessions, sess)
		}
	}
	vcm.mu.RUnlock()

	total := 0
	for _, sess := range sessions {
		sess.inputMu.Lock()
		total += len(sess.input)
		sess.inputMu.Unlock()
	}
	return total
}
