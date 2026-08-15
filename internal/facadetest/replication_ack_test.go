package facadetest

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/net"
	"github.com/mmokit/mmokit/pkg/replication"
	"github.com/mmokit/mmokit/pkg/spatial"
	pkguniverse "github.com/mmokit/mmokit/pkg/universe"
)

// bestEffortInputTransport is fakeInputTransport that additionally reports a
// best-effort delivery class, standing in for a UDP client.
type bestEffortInputTransport struct {
	fakeInputTransport
}

func (t *bestEffortInputTransport) DeliveryClass() net.DeliveryClass {
	return net.DeliveryBestEffort
}

// TestReplicationAck_RoutesToReplicationSystem exercises the whole inbound
// path the CE-003 loop depends on: a typed ReplicationAck client-input frame
// arrives on channel 0x00, DispatchClientInput resolves it against the player
// entity owned by the sending connection on that stage, and the engine-default
// handler forwards it to the cell's ReplicationSystem.AckFrame with the right
// connID, stream epoch, and sequence.
func TestReplicationAck_RoutesToReplicationSystem(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
	registerTestKindOnProcess(t, p)
	p.Build()

	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	if cell == nil {
		t.Fatal("expected one cell")
	}
	stage := cell.Stage
	eng := stage.Engine()

	// Spy on the sink. NewReplicationSystem installs the real AckFrame via
	// ReplicationConfig.RegisterAck; overwriting it here is the cheapest way
	// to observe what the client-input handler forwards.
	var got struct {
		calls       int
		connID      uint32
		streamEpoch uint32
		seq         uint32
	}
	eng.SetReplicationAck(func(connID, streamEpoch, seq uint32) {
		got.calls++
		got.connID = connID
		got.streamEpoch = streamEpoch
		got.seq = seq
	})

	connMgr, ok := eng.ConnMgr.(*net.ConnManager)
	if !ok {
		t.Fatalf("engine ConnMgr is %T, expected *net.ConnManager", eng.ConnMgr)
	}
	tr := &bestEffortInputTransport{}
	connID := connMgr.AddTransport(tr, "")

	// PlayerConn is what the engine-default handler reads to learn which
	// connection is acking — a real player entity always carries it.
	playerEnt := stage.Spawn(
		mmokit.Position{},
		mmokit.EntityKind{Type: testKindID},
		mmokit.PlayerConn{ConnID: connID},
	)

	eng.Players.RegisterSessionTransfer(connID, "udp-player", "active", nil)
	sess := eng.Players.ByConnID(connID)
	if sess == nil {
		t.Fatalf("RegisterSessionTransfer did not register session for conn=%d", connID)
	}
	sess.Entity = playerEnt.Handle()

	tr.input = append(tr.input, buildReplicationAckFrame(t, 7, 42))
	stage.DispatchClientInput()

	if got.calls != 1 {
		t.Fatalf("AckReplicationFrame calls = %d, want 1", got.calls)
	}
	if got.connID != connID {
		t.Fatalf("acked connID = %d, want %d", got.connID, connID)
	}
	if got.streamEpoch != 7 || got.seq != 42 {
		t.Fatalf("acked (streamEpoch, seq) = (%d, %d), want (7, 42)", got.streamEpoch, got.seq)
	}
}

// TestReplicationAck_NoLiveEntityIsDroppedSilently asserts an ack from a
// connection whose session has no entity — the window right after a handoff,
// or a client that acks after its entity was destroyed — is dropped rather
// than panicking.
func TestReplicationAck_NoLiveEntityIsDroppedSilently(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
	registerTestKindOnProcess(t, p)
	p.Build()

	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	stage := cell.Stage
	eng := stage.Engine()

	calls := 0
	eng.SetReplicationAck(func(uint32, uint32, uint32) { calls++ })

	connMgr := eng.ConnMgr.(*net.ConnManager)
	tr := &bestEffortInputTransport{}
	connID := connMgr.AddTransport(tr, "")
	eng.Players.RegisterSessionTransfer(connID, "entity-less", "active", nil)

	tr.input = append(tr.input, buildReplicationAckFrame(t, 1, 1))
	stage.DispatchClientInput() // must not panic

	if calls != 0 {
		t.Fatalf("ack from an entity-less session reached the sink %d time(s)", calls)
	}
}

// TestDefaultReplicationConfig_LatchesAckModeFromTransportClass asserts the
// selector wired by DefaultReplicationConfig picks AckExplicit for a datagram
// connection and AckReliable for a reliable-ordered one, and that an unknown
// connection is treated conservatively.
func TestDefaultReplicationConfig_LatchesAckModeFromTransportClass(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
	registerTestKindOnProcess(t, p)
	p.Build()

	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	eng := cell.Stage.Engine()
	connMgr := eng.ConnMgr.(*net.ConnManager)

	udpConn := connMgr.AddTransport(&bestEffortInputTransport{}, "")
	wsConn := connMgr.AddTransport(&fakeInputTransport{}, "") // no class -> conservative

	cfg := mmokit.DefaultReplicationConfig(eng, spatial.NewHashGrid(100), nil)
	if cfg.AckModeFor == nil {
		t.Fatal("DefaultReplicationConfig did not wire AckModeFor")
	}
	if cfg.RegisterAck == nil {
		t.Fatal("DefaultReplicationConfig did not wire RegisterAck")
	}

	if got := cfg.AckModeFor(udpConn); got != replication.AckExplicit {
		t.Fatalf("datagram connection mode = %v, want AckExplicit", got)
	}
	if got := cfg.AckModeFor(wsConn); got != replication.AckReliable {
		t.Fatalf("reliable-ordered connection mode = %v, want AckReliable", got)
	}
	if got := cfg.AckModeFor(9999); got != replication.AckReliable {
		t.Fatalf("unknown connection mode = %v, want AckReliable", got)
	}
}

// buildReplicationAckFrame assembles the typed-input wire frame the readPump
// would hand to DrainInput: [u32 typeID][u32 bodyLen][body], channel byte
// already stripped.
func buildReplicationAckFrame(t *testing.T, streamEpoch, seq uint32) []byte {
	t.Helper()
	typeID := mmokit.TypeIDOf(reflect.TypeFor[mmokit.ReplicationAck]())
	if typeID == 0 {
		t.Fatal("mmokit.ReplicationAck is not registered as a client-input type")
	}
	body := mustMarshal(t, &mmokit.ReplicationAck{StreamEpoch: streamEpoch, Seq: seq})
	frame := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(frame[0:4], typeID)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(body)))
	copy(frame[8:], body)
	return frame
}
