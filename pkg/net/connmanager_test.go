package net

import (
	"slices"
	"testing"
	"time"
)

// mockTransport implements Transport for testing.
type mockTransport struct {
	reliable   [][]byte
	unreliable [][]byte
	input      [][]byte
	opInput    [][]byte
	closed     bool
}

func (m *mockTransport) SendReliable(data []byte) SendResult {
	m.reliable = append(m.reliable, data)
	return SendResult{Disposition: SendQueued, Delivery: DeliveryReliableOrdered}
}
func (m *mockTransport) SendUnreliable(data []byte) SendResult {
	m.unreliable = append(m.unreliable, data)
	return SendResult{Disposition: SendQueued, Delivery: DeliveryBestEffort}
}
func (m *mockTransport) DrainInput() [][]byte    { r := m.input; m.input = nil; return r }
func (m *mockTransport) DrainOpInput() [][]byte  { r := m.opInput; m.opInput = nil; return r }
func (m *mockTransport) InjectInput(data []byte) { m.input = append(m.input, data) }
func (m *mockTransport) Close()                  { m.closed = true }

func drainEvent(t *testing.T, ch <-chan PlayerEvent) PlayerEvent {
	t.Helper()
	select {
	case evt := <-ch:
		return evt
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected event but channel was empty")
		return PlayerEvent{}
	}
}

func TestConnManager_AddTransport_IncrementingIDs(t *testing.T) {
	cm := NewConnManager()
	id1 := cm.AddTransport(&mockTransport{})
	id2 := cm.AddTransport(&mockTransport{})
	id3 := cm.AddTransport(&mockTransport{})

	if id1 != 1 {
		t.Fatalf("expected first connID=1, got %d", id1)
	}
	if id2 != 2 {
		t.Fatalf("expected second connID=2, got %d", id2)
	}
	if id3 != 3 {
		t.Fatalf("expected third connID=3, got %d", id3)
	}
}

func TestConnManager_AddTransport_SendsConnectedEvent(t *testing.T) {
	cm := NewConnManager()
	id := cm.AddTransport(&mockTransport{})

	evt := drainEvent(t, cm.Events())
	if !evt.Connected {
		t.Fatal("expected Connected=true")
	}
	if evt.ConnID != id {
		t.Fatalf("expected ConnID=%d, got %d", id, evt.ConnID)
	}
}

func TestConnManager_Get_RegisteredAndUnknown(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	got := cm.Get(id)
	if got != mt {
		t.Fatal("Get should return the registered transport")
	}

	if cm.Get(9999) != nil {
		t.Fatal("Get for unknown ID should return nil")
	}
}

func TestConnManager_Send(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	data := []byte("hello")
	result := cm.Send(id, data)
	if !result.Queued() || result.Delivery != DeliveryBestEffort {
		t.Fatalf("Send result = %+v, want queued best-effort", result)
	}

	if len(mt.unreliable) != 1 {
		t.Fatalf("expected 1 unreliable message, got %d", len(mt.unreliable))
	}
	if string(mt.unreliable[0]) != "hello" {
		t.Fatalf("expected 'hello', got %q", mt.unreliable[0])
	}
}

func TestConnManager_SendReliable(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	data := []byte("important")
	result := cm.SendReliable(id, data)
	if !result.Supports(DeliveryReliableOrdered) {
		t.Fatalf("SendReliable result = %+v, want reliable ordered enqueue", result)
	}

	if len(mt.reliable) != 1 {
		t.Fatalf("expected 1 reliable message, got %d", len(mt.reliable))
	}
	if string(mt.reliable[0]) != "important" {
		t.Fatalf("expected 'important', got %q", mt.reliable[0])
	}
}

func TestConnManager_Send_UnknownConnID_NoPanic(t *testing.T) {
	cm := NewConnManager()
	if result := cm.Send(9999, []byte("data")); result.Disposition != SendNoRoute {
		t.Fatalf("Send unknown disposition = %v, want no-route", result.Disposition)
	}
	if result := cm.SendReliable(9999, []byte("data")); result.Disposition != SendNoRoute {
		t.Fatalf("SendReliable unknown disposition = %v, want no-route", result.Disposition)
	}
}

func TestConnManager_DrainInput(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{
		input: [][]byte{[]byte("msg1"), []byte("msg2")},
	}
	id := cm.AddTransport(mt)

	msgs := cm.DrainInput(id)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if string(msgs[0]) != "msg1" || string(msgs[1]) != "msg2" {
		t.Fatalf("unexpected messages: %v", msgs)
	}

	// Second drain should be empty
	msgs = cm.DrainInput(id)
	if msgs != nil {
		t.Fatalf("expected nil after drain, got %v", msgs)
	}
}

func TestConnManager_DrainInput_UnknownConnID(t *testing.T) {
	cm := NewConnManager()
	msgs := cm.DrainInput(9999)
	if msgs != nil {
		t.Fatalf("expected nil for unknown connID, got %v", msgs)
	}
}

func TestConnManager_DrainOpInput(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{
		opInput: [][]byte{[]byte("op1")},
	}
	id := cm.AddTransport(mt)

	msgs := cm.DrainOpInput(id)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 op message, got %d", len(msgs))
	}
	if string(msgs[0]) != "op1" {
		t.Fatalf("expected 'op1', got %q", msgs[0])
	}
}

func TestConnManager_Remove(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	cm.Remove(id)

	if cm.Get(id) != nil {
		t.Fatal("Get should return nil after Remove")
	}
	if !mt.closed {
		t.Fatal("transport should be closed after Remove")
	}
}

func TestConnManager_Remove_SubsequentSendIsNoop(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	cm.Remove(id)

	// Should not panic and should not add messages
	if result := cm.Send(id, []byte("after-remove")); result.Disposition != SendNoRoute {
		t.Fatalf("Send after Remove disposition = %v, want no-route", result.Disposition)
	}
	if result := cm.SendReliable(id, []byte("after-remove")); result.Disposition != SendNoRoute {
		t.Fatalf("SendReliable after Remove disposition = %v, want no-route", result.Disposition)
	}

	if len(mt.unreliable) != 0 {
		t.Fatal("Send after Remove should be a no-op")
	}
	if len(mt.reliable) != 0 {
		t.Fatal("SendReliable after Remove should be a no-op")
	}
}

func TestConnManager_Unregister(t *testing.T) {
	cm := NewConnManager()
	mt := &mockTransport{}
	id := cm.AddTransport(mt)

	// Drain the Connected event first
	drainEvent(t, cm.Events())

	cm.Unregister(id)

	if cm.Get(id) != nil {
		t.Fatal("Get should return nil after Unregister")
	}
	if mt.closed {
		t.Fatal("Unregister should NOT call Close on the transport")
	}

	evt := drainEvent(t, cm.Events())
	if !evt.Disconnect {
		t.Fatal("expected Disconnect=true")
	}
	if evt.ConnID != id {
		t.Fatalf("expected ConnID=%d, got %d", id, evt.ConnID)
	}
}

func TestConnManager_ActiveConnIDs(t *testing.T) {
	cm := NewConnManager()
	id1 := cm.AddTransport(&mockTransport{})
	id2 := cm.AddTransport(&mockTransport{})
	id3 := cm.AddTransport(&mockTransport{})

	ids := cm.ActiveConnIDs()
	slices.Sort(ids)

	if len(ids) != 3 {
		t.Fatalf("expected 3 active IDs, got %d", len(ids))
	}
	if ids[0] != id1 || ids[1] != id2 || ids[2] != id3 {
		t.Fatalf("expected [%d %d %d], got %v", id1, id2, id3, ids)
	}

	cm.Remove(id2)
	ids = cm.ActiveConnIDs()
	slices.Sort(ids)

	if len(ids) != 2 {
		t.Fatalf("expected 2 active IDs after removal, got %d", len(ids))
	}
	if ids[0] != id1 || ids[1] != id3 {
		t.Fatalf("expected [%d %d], got %v", id1, id3, ids)
	}
}

// classedTransport is a mockTransport that also reports a static delivery
// class, standing in for WSTransport / UDPTransport without needing a socket.
type classedTransport struct {
	mockTransport
	class DeliveryClass
}

func (c *classedTransport) DeliveryClass() DeliveryClass { return c.class }

// TestConnManager_DeliveryClassFor is the backward-compat guarantee behind the
// per-connection ACK mode latch: a transport that does not report a class, and
// an unknown connection, must both answer DeliveryReliableOrdered so every
// existing stub transport stays on AckReliable.
func TestConnManager_DeliveryClassFor(t *testing.T) {
	cm := NewConnManager()

	wsLike := cm.AddTransport(&classedTransport{class: DeliveryReliableOrdered})
	udpLike := cm.AddTransport(&classedTransport{class: DeliveryBestEffort})
	stub := cm.AddTransport(&mockTransport{})

	cases := []struct {
		name   string
		connID uint32
		want   DeliveryClass
	}{
		{"reliable-ordered transport", wsLike, DeliveryReliableOrdered},
		{"best-effort transport", udpLike, DeliveryBestEffort},
		{"transport without the capability", stub, DeliveryReliableOrdered},
		{"unknown connID", 9999, DeliveryReliableOrdered},
	}
	for _, tc := range cases {
		if got := cm.DeliveryClassFor(tc.connID); got != tc.want {
			t.Errorf("%s: DeliveryClassFor(%d) = %v, want %v", tc.name, tc.connID, got, tc.want)
		}
	}
}

// TestTransportDeliveryClasses pins the concrete transports' static answers.
// These are what the replication ACK-mode latch is built on; changing either
// silently changes how every connection of that type replicates.
func TestTransportDeliveryClasses(t *testing.T) {
	var ws Transport = &WSTransport{}
	if p, ok := ws.(DeliveryClassProvider); !ok {
		t.Fatal("WSTransport does not implement DeliveryClassProvider")
	} else if got := p.DeliveryClass(); got != DeliveryReliableOrdered {
		t.Fatalf("WSTransport.DeliveryClass() = %v, want DeliveryReliableOrdered", got)
	}

	var udp Transport = &UDPTransport{}
	if p, ok := udp.(DeliveryClassProvider); !ok {
		t.Fatal("UDPTransport does not implement DeliveryClassProvider")
	} else if got := p.DeliveryClass(); got != DeliveryBestEffort {
		t.Fatalf("UDPTransport.DeliveryClass() = %v, want DeliveryBestEffort", got)
	}
}
