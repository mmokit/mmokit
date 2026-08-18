package universe

import (
	"encoding/binary"
	"math"
	"reflect"
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
	pkgnet "github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/replication"
)

// ── Virtual connection queues ────────────────────────────────────────────────

// The VCM queue is the most exposed of the three inbound surfaces: the gateway
// forwards at WebSocket read speed over a 16 MiB gRPC channel while the host
// drains once per 50 ms tick. Before the cap, appendChannel appended without
// any bound at all.
func TestAppendChannel_DropsAtQueueCap(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	limits := pkgnet.DefaultWireLimits()
	limits.MaxInputQueueDepth = 5
	limits.MaxOpQueueDepth = 3
	limits.MaxFramesPerDrain = 64
	vcm.SetWireLimits(limits)

	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-cap", ConnID: 1}, "flood", 0, "cell_0_0")

	const sent = 50
	for i := range sent {
		vcm.appendChannel(localID, []byte{byte(i)}, pkgnet.ChannelEvent)
		vcm.appendChannel(localID, []byte{byte(i)}, pkgnet.ChannelOperation)
	}

	events := vcm.DrainInput(localID)
	if len(events) != limits.MaxInputQueueDepth {
		t.Fatalf("event queue held %d frames, want %d", len(events), limits.MaxInputQueueDepth)
	}
	opsQ := vcm.DrainOpInput(localID)
	if len(opsQ) != limits.MaxOpQueueDepth {
		t.Fatalf("op queue held %d frames, want %d", len(opsQ), limits.MaxOpQueueDepth)
	}

	if got, want := vcm.InputDrops(), uint64(sent-limits.MaxInputQueueDepth); got != want {
		t.Errorf("InputDrops = %d, want %d", got, want)
	}
	if got, want := vcm.OpInputDrops(), uint64(sent-limits.MaxOpQueueDepth); got != want {
		t.Errorf("OpInputDrops = %d, want %d", got, want)
	}

	// Drop-newest: the retained frames are the oldest ones.
	for i, f := range events {
		if len(f) != 1 || f[0] != byte(i) {
			t.Fatalf("retained event %d = %v, want the oldest frames (drop-newest)", i, f)
		}
	}
}

// appendChannel had no length check at all; the frame size ceiling has to be
// enforced here as well as on the two direct client surfaces.
func TestAppendChannel_RejectsOversizeFrame(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	limits := pkgnet.DefaultWireLimits()
	limits.MaxFrameBytes = 32
	vcm.SetWireLimits(limits)

	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-big", ConnID: 2}, "big", 0, "cell_0_0")

	vcm.appendChannel(localID, make([]byte, limits.MaxFrameBytes), pkgnet.ChannelEvent)
	if got := len(vcm.DrainInput(localID)); got != 1 {
		t.Fatalf("at-limit frame: drained %d, want 1", got)
	}

	vcm.appendChannel(localID, make([]byte, limits.MaxFrameBytes+1), pkgnet.ChannelEvent)
	if got := vcm.DrainInput(localID); got != nil {
		t.Fatalf("oversize frame was queued: %d frames", len(got))
	}
	if got := vcm.OversizeFrameDrops(); got != 1 {
		t.Fatalf("OversizeFrameDrops = %d, want 1", got)
	}
}

// The per-drain budget must leave the remainder queued rather than dropping it:
// these frames are within the connection's depth budget, they are only in
// excess of one tick's worth of work.
func TestVCMDrain_StopsAtPerDrainBudget(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	limits := pkgnet.DefaultWireLimits()
	limits.MaxFramesPerDrain = 3
	limits.MaxInputQueueDepth = 32
	vcm.SetWireLimits(limits)

	localID := vcm.RegisterSession(SessionKey{GatewayID: "gw-drain", ConnID: 3}, "drain", 0, "cell_0_0")
	for i := range 8 {
		vcm.appendChannel(localID, []byte{byte(i)}, pkgnet.ChannelEvent)
	}

	var seen int
	for range 4 {
		seen += len(vcm.DrainInput(localID))
	}
	if seen != 8 {
		t.Fatalf("drained %d frames across four passes, want all 8 (remainder must stay queued)", seen)
	}
	if got := vcm.InputDrops(); got != 0 {
		t.Fatalf("InputDrops = %d, want 0 — the budget must defer, not drop", got)
	}
}

// ── Gateway op-frame barrier ─────────────────────────────────────────────────

// malformedOpRequest has a single string field, so its reflect-decoded body
// starts with a u16 length prefix — the exact shape reflect_marshal.go slices
// on without a bounds check.
type malformedOpRequest struct{ Name string }

type malformedOpResponse struct{ OK bool }

// registerGatewayLocalOp registers malformedOpRequest as a RouteGatewayLocal
// op and returns its wire typeID.
func registerGatewayLocalOp(t *testing.T) uint32 {
	t.Helper()
	globalWire.RegisterTypedOp(RouteGatewayLocal,
		reflect.TypeFor[malformedOpRequest](),
		reflect.TypeFor[malformedOpResponse](),
		func(_ *ops.OpContext, _ *malformedOpRequest) (*malformedOpResponse, error) {
			return &malformedOpResponse{OK: true}, nil
		})
	t.Cleanup(func() {
		globalWire.mu.Lock()
		delete(globalWire.typedOps, TypeIDOf(reflect.TypeFor[malformedOpRequest]()))
		globalWire.mu.Unlock()
	})
	return TypeIDOf(reflect.TypeFor[malformedOpRequest]())
}

// malformedOpFrame builds a 0x01 typed-op payload (channel byte already
// stripped) whose body declares a 0xFFFF-byte string and supplies only the two
// length-prefix bytes.
func malformedOpFrame(typeID uint32) []byte {
	body := []byte{0xFF, 0xFF}
	frame := EncodeTypedOpFrame(typeID, 7, body)
	return frame[1:] // strip the channel byte, as the drain path does
}

// A malformed 0x01 body reaches the reflection decoder PRE-AUTHENTICATION on a
// goroutine with no barrier of its own, so on HEAD this panics with "slice
// bounds out of range" and takes the process with it.
func TestProcessOpFrame_MalformedBody_DoesNotPanic(t *testing.T) {
	typeID := registerGatewayLocalOp(t)

	g := &Gateway{
		id:      "gw-test",
		connMgr: pkgnet.NewConnManager(),
		log:     logger.New(CatNetConn),
	}
	connID := g.connMgr.AddTransport(&countingTransport{}, "203.0.113.4:1000")

	// sess == nil: this is the pre-authentication window.
	g.processOpFrame(connID, nil, malformedOpFrame(typeID))

	// The pump must still be able to hand over the next frame.
	g.processOpFrame(connID, nil, malformedOpFrame(typeID))
}

// countingTransport records reliable sends and hands back a configurable
// backlog of inbound frames.
type countingTransport struct {
	input    [][]byte
	opInput  [][]byte
	reliable [][]byte
	drains   int
}

func (c *countingTransport) SendReliable(data []byte) pkgnet.SendResult {
	c.reliable = append(c.reliable, data)
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}

func (c *countingTransport) SendUnreliable(data []byte) pkgnet.SendResult {
	return c.SendReliable(data)
}

func (c *countingTransport) DrainInput() [][]byte {
	c.drains++
	r := c.input
	c.input = nil
	return r
}

func (c *countingTransport) DrainOpInput() [][]byte {
	r := c.opInput
	c.opInput = nil
	return r
}

func (c *countingTransport) InjectInput(data []byte) { c.input = append(c.input, data) }
func (c *countingTransport) Close()                  {}

// ── Per-tick client-input budget ─────────────────────────────────────────────

// DispatchClientInput runs inside gl.tick, which has no slack: before the
// budget it drained every queued frame for every connected player, so one
// tick's cost was set by how fast the clients chose to send.
func TestDispatchClientInput_StopsAtPerTickCap(t *testing.T) {
	// Every frame names an unregistered typeID, which is dropped after being
	// counted against the budget — the budget is what this test measures.
	cell := newTestCell("budget", CellID{X: 0, Y: 0})
	connMgr := cell.Engine.ConnMgr.(*pkgnet.ConnManager)

	// framesPerPlayer * players comfortably exceeds clientInputFrameBudget so
	// the budget check between players has to fire.
	const players = 12
	framesPerPlayer := clientInputFrameBudget / 4

	transports := make([]*countingTransport, 0, players)
	for i := range players {
		tr := &countingTransport{}
		for range framesPerPlayer {
			// 8-byte header declaring a zero-length body: a well-formed,
			// cheap frame. The cost being bounded here is the per-frame
			// dispatch, not the decode.
			frame := make([]byte, clientInputHeaderBytes)
			binary.LittleEndian.PutUint32(frame[0:4], 0xDEAD0000+uint32(i))
			tr.input = append(tr.input, frame)
		}
		transports = append(transports, tr)
		connID := connMgr.AddTransport(tr, "")
		cell.Engine.Players.RegisterSessionTransfer(connID, "p"+string(rune('a'+i)), "active", nil)
	}

	cell.Stage.DispatchClientInput()

	drained := 0
	undrained := 0
	for _, tr := range transports {
		if tr.drains == 0 {
			undrained++
		} else {
			drained++
		}
	}
	if undrained == 0 {
		t.Fatalf("every player was drained in one tick (%d players x %d frames); the budget never fired",
			players, framesPerPlayer)
	}
	if drained == 0 {
		t.Fatal("no player was drained; the budget is starving the drain entirely")
	}
	// The deferred players keep their frames — an undrained queue is serviced
	// next tick, whereas a drained-but-undispatched frame would be lost.
	for _, tr := range transports {
		if tr.drains == 0 && len(tr.input) != framesPerPlayer {
			t.Fatalf("deferred player lost frames: has %d, want %d", len(tr.input), framesPerPlayer)
		}
	}
}

// ── Cell loop survival ───────────────────────────────────────────────────────

// stringPayload stands in for any registered replicated component with a
// string field. Its decoder is the reflection codec.
type stringPayload struct{ Label string }

// A malformed peer frame must not kill the cell loop. DrainInbox is reached
// from cellBridge.PreTick inside gl.tick, and gl.tick runs bare with no
// recover, so before CE-002 unit 5 the panic below aborted the process.
//
// The frame here is decoded by a registered component's Apply — the real path
// for a border frame — not by a synthetic hook.
//
// CE-002 unit 8 flipped the assertion, in the same way it flipped the bounds
// table: the checked decoder now REFUSES the component body instead of
// panicking, so the outer barrier is never reached and the recovery counter
// must stay at zero. The barrier itself is still covered, by
// TestCellProcessMessage_RecoveryIsNotSilent. Both properties this test was
// written for — the drain continues, and the malformed body is visible rather
// than silent — are asserted below; only the mechanism changed.
//
// Unit 9 moved WHERE the drop is counted: the replicator's Apply now returns
// the error and applyEntityComponents counts it via noteComponentDecodeDrop, so
// DecodeDrops still moves but now proves the failure travelled from the decoder
// to the component-skip policy rather than being swallowed at the wrapper.
func TestCellProcessMessage_MalformedFrame_LoopSurvives(t *testing.T) {
	cell := newTestCell("victim", CellID{X: 0, Y: 0})

	// Register a replicated component whose Apply decodes a string field.
	cell.Stage.ReplicationRegistry().Register(ComponentReplicator{
		Scan: func(ecs.Entity) []byte { return nil },
		Apply: func(_ ecs.Entity, data []byte) error {
			var v stringPayload
			return ReflectUnmarshal(data, &v)
		},
	})
	compID := cell.Stage.ReplicationRegistry().All()[0].ID

	// Component tail: one entry whose 2-byte body declares a 0xFFFF-byte
	// string. The framing is valid; the component body is not.
	tail := []byte{0x01, 0x00} // componentCount = 1
	tail = append(tail, byte(compID), byte(compID>>8), 0x02, 0x00, 0xFF, 0xFF)

	delta := make([]byte, 26)
	binary.LittleEndian.PutUint32(delta[0:4], math.Float32bits(5))
	binary.LittleEndian.PutUint32(delta[4:8], math.Float32bits(6))
	binary.LittleEndian.PutUint32(delta[8:12], math.Float32bits(1))
	delta = append(delta, tail...)

	poison := replication.Frame{Entries: []replication.FrameEntry{{
		NetID:    replication.NetID{ID: 900, Epoch: 1},
		Kind:     1,
		DeltaBuf: delta,
	}}}

	cell.Inbox <- CellMessage{Type: MsgBorderFrame, FromCellID: "src", BorderFrame: poison.Encode()}
	// A well-formed message queued behind the poison one: the drain must keep
	// going, not just avoid crashing.
	cell.Inbox <- CellMessage{
		Type:       MsgSpawnTransfer,
		FromCellID: "src",
		Spawn:      &SpawnTransfer{ConnID: 4242, Username: "survivor"},
	}

	dropsBefore := DecodeDrops()
	cell.DrainInbox() // must not panic — this is what runs inside gl.tick

	if got := cell.RecoveredMessagePanics(); got != 0 {
		t.Fatalf("RecoveredMessagePanics = %d, want 0: the decoder must refuse the body, "+
			"not rely on the outer barrier to survive it", got)
	}
	if got := DecodeDrops(); got == dropsBefore {
		t.Fatal("malformed component body was accepted; the decoder counted no rejection")
	}
	if sess := cell.Engine.Players.ByConnID(4242); sess == nil {
		t.Fatal("message queued behind the poisoned frame was never processed")
	}
}

// A recovered panic in a MUTATING arm must not be silent: it forces a
// cluster-integrity re-assert so a half-applied handoff is visible.
func TestCellProcessMessage_RecoveryIsNotSilent(t *testing.T) {
	cell := newTestCell("silent", CellID{X: 0, Y: 0})
	// No Stage.coord in this fixture, so the re-assert is a no-op — the
	// assertion is that the counter still moves and the drain continues.
	cell.onMessage = func(CellMessage) { panic("simulated handler fault") }

	cell.Inbox <- CellMessage{Type: MsgSpawnTransfer, Spawn: &SpawnTransfer{ConnID: 1, Username: "x"}}
	cell.DrainInbox()

	if got := cell.RecoveredMessagePanics(); got != 1 {
		t.Fatalf("RecoveredMessagePanics = %d, want 1", got)
	}
}

var _ = engine.PlayerSession{}
