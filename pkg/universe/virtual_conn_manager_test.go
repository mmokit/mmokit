package universe

import (
	"sync"
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
)

func testVCMLogger() *logger.Logger {
	return logger.New(CatMeshMsg)
}

func TestVCM_RegisterLookupDrop(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 42}

	localID := vcm.RegisterSession(key, "alice", 1)
	if localID == 0 {
		t.Fatal("RegisterSession returned 0 (invalid)")
	}

	got, ok := vcm.LookupByKey(key)
	if !ok {
		t.Fatal("LookupByKey: expected ok=true after register")
	}
	if got != localID {
		t.Errorf("LookupByKey = %d, want %d", got, localID)
	}

	droppedID, ok := vcm.DropSession(key)
	if !ok {
		t.Fatal("DropSession: expected ok=true")
	}
	if droppedID != localID {
		t.Errorf("DropSession localID = %d, want %d", droppedID, localID)
	}

	_, ok = vcm.LookupByKey(key)
	if ok {
		t.Error("LookupByKey after Drop: expected ok=false")
	}
}

func TestVCM_DoubleRegister(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 7}

	id1 := vcm.RegisterSession(key, "bob", 1)
	id2 := vcm.RegisterSession(key, "bob", 2)

	if id1 != id2 {
		t.Errorf("double-register returned different IDs: %d vs %d", id1, id2)
	}

	// Epoch must be updated to 2 on the stored session.
	vcm.mu.RLock()
	sess := vcm.byKey[key]
	vcm.mu.RUnlock()
	if sess == nil {
		t.Fatal("session missing after double-register")
	}
	if sess.epoch != 2 {
		t.Errorf("epoch = %d after second register, want 2", sess.epoch)
	}
}

func TestVCM_InjectDrainInput(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 1}
	localID := vcm.RegisterSession(key, "carol", 1)

	// Channel 0x00 (event) data.
	eventData := []byte{0x00, 0xAB, 0xCD}
	// Channel 0x01 (ops) data.
	opData := []byte{0x01, 0x12, 0x34}

	vcm.InjectInput(localID, eventData)
	vcm.InjectInput(localID, opData)

	events := vcm.DrainInput(localID)
	if len(events) != 1 {
		t.Fatalf("DrainInput: got %d items, want 1", len(events))
	}
	if string(events[0]) != string(eventData) {
		t.Errorf("DrainInput[0] = %v, want %v", events[0], eventData)
	}

	ops := vcm.DrainOpInput(localID)
	if len(ops) != 1 {
		t.Fatalf("DrainOpInput: got %d items, want 1", len(ops))
	}
	if string(ops[0]) != string(opData) {
		t.Errorf("DrainOpInput[0] = %v, want %v", ops[0], opData)
	}

	// Second drain should return nil (queue cleared).
	if vcm.DrainInput(localID) != nil {
		t.Error("DrainInput second call: expected nil")
	}
	if vcm.DrainOpInput(localID) != nil {
		t.Error("DrainOpInput second call: expected nil")
	}
}

func TestVCM_DropUnknownKey(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "ghost", ConnID: 999}

	localID, ok := vcm.DropSession(key)
	if ok {
		t.Error("DropSession on missing key: expected ok=false")
	}
	if localID != 0 {
		t.Errorf("DropSession on missing key: localID = %d, want 0", localID)
	}
}

func TestVCM_LookupByLocal(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-2", ConnID: 55}
	localID := vcm.RegisterSession(key, "dave", 1)

	got, ok := vcm.LookupByLocal(localID)
	if !ok {
		t.Fatal("LookupByLocal: expected ok=true after register")
	}
	if got != key {
		t.Errorf("LookupByLocal = %v, want %v", got, key)
	}
}

func TestVCM_SendWithNilHN(t *testing.T) {
	// nil hn is the test affordance; Send/SendReliable must not panic.
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-1", ConnID: 1}
	localID := vcm.RegisterSession(key, "eve", 1)

	// Should not panic.
	vcm.Send(localID, []byte{0x00, 0x01})
	vcm.SendReliable(localID, []byte{0x00, 0x02})
}

func TestVCM_ConcurrentRegisterDrop(t *testing.T) {
	vcm := NewVirtualConnManager(nil, testVCMLogger())
	key := SessionKey{GatewayID: "gw-race", ConnID: 1}

	const iters = 100
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			vcm.RegisterSession(key, "racer", uint64(i+1))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			vcm.DropSession(key)
		}
	}()

	wg.Wait()
}
