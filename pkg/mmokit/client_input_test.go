package mmokit_test

import (
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/net"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// clientInputPing is a tiny HandleClient-eligible message used for
// registry + dispatch tests.
type clientInputPing struct{ N uint32 }

// TestHandleClient_RegistersInClientInputRegistry verifies that calling
// HandleClient[T] populates the mmokit-side client-input registry and
// the universe-side ClientInputHooks indirection sees the type.
func TestHandleClient_RegistersInClientInputRegistry(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})

	mmokit.HandleClient(p, func(player mmokit.Entity, msg *clientInputPing) {})

	pingType := reflect.TypeFor[clientInputPing]()
	if pkguniverse.ClientInputHooks.IsRegistered == nil {
		t.Fatal("ClientInputHooks.IsRegistered not wired")
	}
	if !pkguniverse.ClientInputHooks.IsRegistered(pingType) {
		t.Errorf("clientInputPing not registered after HandleClient")
	}

	if pkguniverse.ClientInputHooks.TypeIDOf == nil {
		t.Fatal("ClientInputHooks.TypeIDOf not wired")
	}
	id := pkguniverse.ClientInputHooks.TypeIDOf(pingType)
	if id == 0 {
		t.Errorf("expected non-zero typeID for clientInputPing")
	}

	if pkguniverse.ClientInputHooks.TypeOfTypeID == nil {
		t.Fatal("ClientInputHooks.TypeOfTypeID not wired")
	}
	gotType := pkguniverse.ClientInputHooks.TypeOfTypeID(id)
	if gotType != pingType {
		t.Errorf("TypeOfTypeID(%d) = %v, want %v", id, gotType, pingType)
	}

	// Unknown typeID resolves to nil — drives the "untrusted typeID"
	// log + drop path in DispatchClientInput.
	if pkguniverse.ClientInputHooks.TypeOfTypeID(0xDEADBEEF) != nil {
		t.Errorf("TypeOfTypeID for unregistered ID should return nil")
	}
}

// TestClientInputTypes_DeterministicOrder verifies that ClientInputTypes
// returns registered types in stable lexical order. Sdkgen depends on
// this for reproducible output.
func TestClientInputTypes_DeterministicOrder(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})

	// Register two types in reverse-lexical order; ClientInputTypes
	// should still emit them sorted.
	type bbInput struct{ V uint32 }
	type aaInput struct{ V uint32 }

	mmokit.HandleClient(p, func(player mmokit.Entity, msg *bbInput) {})
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *aaInput) {})

	types := mmokit.ClientInputTypes()

	// Find both in the slice; they may share the slice with other
	// types from earlier tests (registry is package-global).
	var aaIdx, bbIdx int = -1, -1
	for i, tt := range types {
		if tt == reflect.TypeFor[aaInput]() {
			aaIdx = i
		}
		if tt == reflect.TypeFor[bbInput]() {
			bbIdx = i
		}
	}
	if aaIdx < 0 || bbIdx < 0 {
		t.Fatalf("registered types missing from ClientInputTypes: aa=%d bb=%d types=%v", aaIdx, bbIdx, types)
	}
	if aaIdx > bbIdx {
		t.Errorf("ClientInputTypes returned types out of sort order: aaIdx=%d > bbIdx=%d", aaIdx, bbIdx)
	}
}

// fakeInputTransport is a minimal Transport that lets a test inject a
// pre-built 0x02 frame directly onto its DrainClientInput queue. All
// other Transport methods are no-ops — only the dispatch path is
// exercised.
type fakeInputTransport struct {
	clientInput [][]byte
}

func (t *fakeInputTransport) SendReliable(data []byte)    {}
func (t *fakeInputTransport) SendUnreliable(data []byte)  {}
func (t *fakeInputTransport) DrainInput() [][]byte        { return nil }
func (t *fakeInputTransport) DrainOpInput() [][]byte      { return nil }
func (t *fakeInputTransport) DrainClientInput() [][]byte  { r := t.clientInput; t.clientInput = nil; return r }
func (t *fakeInputTransport) InjectInput(data []byte)     {}
func (t *fakeInputTransport) Close()                      {}

// TestDispatchClientInput_FiresHandler exercises the end-to-end gateway
// dispatch path: build a wire frame on channel 0x02, push it into a
// connection's transport, run DispatchClientInput, assert the
// registered handler fired with (player Entity, *msg) populated.
func TestDispatchClientInput_FiresHandler(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})

	registerTestKindOnProcess(t, p)

	type clientPing struct{ N uint32 }
	var fired struct {
		count   int
		netID   uint32
		payload uint32
	}
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *clientPing) {
		fired.count++
		fired.netID = player.NetID()
		fired.payload = msg.N
	})

	p.Build()

	if len(p.Cells) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(p.Cells))
	}
	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	stage := cell.Stage
	eng := stage.Engine()

	// Spawn a player entity so a PlayerSession can point at it.
	playerEnt := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

	// Register a transport on the engine's ConnMgr so DrainClientInput
	// has somewhere to read from.
	tr := &fakeInputTransport{}
	connMgr, ok := eng.ConnMgr.(*net.ConnManager)
	if !ok {
		t.Fatalf("engine ConnMgr is %T, expected *net.ConnManager", eng.ConnMgr)
	}
	connID := connMgr.AddTransport(tr)

	// Register the player session. RegisterSessionTransfer puts it
	// directly into the named state (Active), bypassing the OnEnter
	// callback chain that would otherwise try to spawn an entity for
	// us — we already have one.
	eng.Players.RegisterSessionTransfer(connID, "p1", "active", nil)
	sess := eng.Players.ByConnID(connID)
	if sess == nil {
		t.Fatalf("RegisterSessionTransfer did not register session for conn=%d", connID)
	}
	sess.Entity = playerEnt.Handle()

	// Build a 0x02 wire frame: [u32 typeID][u32 bodyLen][body].
	// (The channel byte is stripped by the readPump before it lands
	// on the clientInput queue, so the frame here matches what
	// DrainClientInput actually returns.)
	pingType := reflect.TypeFor[clientPing]()
	typeID := mmokit.TypeIDOf(pingType)

	msg := &clientPing{N: 42}
	body := pkguniverse.ReflectMarshal(msg)

	frame := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(frame[0:4], typeID)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(len(body)))
	copy(frame[8:], body)

	tr.clientInput = append(tr.clientInput, frame)

	stage.DispatchClientInput()

	if fired.count != 1 {
		t.Fatalf("handler fired %d times, want 1", fired.count)
	}
	if fired.payload != 42 {
		t.Errorf("handler msg.N = %d, want 42", fired.payload)
	}
	if fired.netID == 0 {
		t.Errorf("handler player.NetID = 0, expected non-zero")
	}
}

// TestDispatchClientInput_DropsUntrustedTypeID verifies that an inbound
// frame carrying a typeID that isn't in the HandleClient registry is
// dropped without panicking and without firing any handler.
func TestDispatchClientInput_DropsUntrustedTypeID(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})

	registerTestKindOnProcess(t, p)

	// Register a handler so the dispatcher exists, but build a frame
	// with a typeID it doesn't know about.
	type registeredMsg struct{ N uint32 }
	fired := false
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *registeredMsg) {
		fired = true
	})

	p.Build()

	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	stage := cell.Stage
	eng := stage.Engine()

	playerEnt := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

	tr := &fakeInputTransport{}
	connMgr := eng.ConnMgr.(*net.ConnManager)
	connID := connMgr.AddTransport(tr)

	eng.Players.RegisterSessionTransfer(connID, "p2", "active", nil)
	sess := eng.Players.ByConnID(connID)
	sess.Entity = playerEnt.Handle()

	// Frame with bogus typeID 0xDEADBEEF.
	frame := make([]byte, 8)
	binary.LittleEndian.PutUint32(frame[0:4], 0xDEADBEEF)
	binary.LittleEndian.PutUint32(frame[4:8], 0)
	tr.clientInput = append(tr.clientInput, frame)

	stage.DispatchClientInput()

	if fired {
		t.Errorf("handler fired for an untrusted typeID")
	}
}

// TestDispatchClientInput_DropsMalformedFrame verifies that a frame
// shorter than the 8-byte header is dropped without panicking.
func TestDispatchClientInput_DropsMalformedFrame(t *testing.T) {
	p := mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})

	registerTestKindOnProcess(t, p)

	type someMsg struct{ N uint32 }
	mmokit.HandleClient(p, func(player mmokit.Entity, msg *someMsg) {})

	p.Build()
	var cell *pkguniverse.Cell
	for _, c := range p.Cells {
		cell = c
	}
	stage := cell.Stage
	eng := stage.Engine()

	playerEnt := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

	tr := &fakeInputTransport{}
	connMgr := eng.ConnMgr.(*net.ConnManager)
	connID := connMgr.AddTransport(tr)

	eng.Players.RegisterSessionTransfer(connID, "p3", "active", nil)
	sess := eng.Players.ByConnID(connID)
	sess.Entity = playerEnt.Handle()

	// 4-byte frame is shorter than the 8-byte header — should be
	// logged + dropped, never panic.
	tr.clientInput = append(tr.clientInput, []byte{0x01, 0x02, 0x03, 0x04})

	// Also a frame whose declared bodyLen exceeds remaining bytes.
	truncated := make([]byte, 8)
	binary.LittleEndian.PutUint32(truncated[0:4], 0x12345678)
	binary.LittleEndian.PutUint32(truncated[4:8], 100) // claims 100 bytes
	tr.clientInput = append(tr.clientInput, truncated)

	// Should not panic.
	stage.DispatchClientInput()
}
