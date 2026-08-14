package universe

import (
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/logger"
)

func newTestGatewayRegistry(t *testing.T) *GatewayRegistry {
	t.Helper()
	return NewGatewayRegistry(logger.New())
}

func TestGatewayRegistry_Register(t *testing.T) {
	r := newTestGatewayRegistry(t)
	r.Register("gw-a", "ws://10.0.0.1:8080", "10.0.0.1:9000")
	gw := r.Get("gw-a")
	if gw == nil {
		t.Fatal("expected gateway entry, got nil")
	}
	if gw.ID != "gw-a" {
		t.Errorf("ID: got %q, want %q", gw.ID, "gw-a")
	}
	if gw.WSAddr != "ws://10.0.0.1:8080" {
		t.Errorf("WSAddr: got %q, want %q", gw.WSAddr, "ws://10.0.0.1:8080")
	}
	if gw.GRPCAddr != "10.0.0.1:9000" {
		t.Errorf("GRPCAddr: got %q, want %q", gw.GRPCAddr, "10.0.0.1:9000")
	}
	if gw.State != RemoteGatewayRegistered {
		t.Errorf("State: got %v, want Registered", gw.State)
	}
}

func TestGatewayRegistry_TouchTransitions(t *testing.T) {
	r := newTestGatewayRegistry(t)
	r.Register("gw-b", "ws://10.0.0.2:8080", "10.0.0.2:9000")

	before := time.Now()
	time.Sleep(time.Millisecond) // ensure LastHeartbeat advances
	r.Touch("gw-b")

	gw := r.Get("gw-b")
	if gw.State != RemoteGatewayLive {
		t.Errorf("State after Touch: got %v, want Live", gw.State)
	}
	if !gw.LastHeartbeat.After(before) {
		t.Error("LastHeartbeat should have advanced after Touch")
	}

	// Second Touch stays Live.
	r.Touch("gw-b")
	gw = r.Get("gw-b")
	if gw.State != RemoteGatewayLive {
		t.Errorf("State after second Touch: got %v, want Live", gw.State)
	}
}

func TestGatewayRegistry_TouchUnknown(t *testing.T) {
	r := newTestGatewayRegistry(t)
	// Should not panic.
	r.Touch("nonexistent")
}

func TestGatewayRegistry_MarkDeadKeepsEntry(t *testing.T) {
	r := newTestGatewayRegistry(t)
	r.Register("gw-c", "ws://10.0.0.3:8080", "10.0.0.3:9000")

	r.MarkDead("gw-c")

	gw := r.Get("gw-c")
	if gw == nil {
		t.Fatal("entry should survive MarkDead")
	}
	if gw.State != RemoteGatewayDead {
		t.Errorf("State: got %v, want Dead", gw.State)
	}
}

func TestGatewayRegistry_RemoveIdempotent(t *testing.T) {
	r := newTestGatewayRegistry(t)

	// Remove an unknown gateway is a no-op.
	r.Remove("ghost")

	r.Register("gw-d", "ws://10.0.0.4:8080", "10.0.0.4:9000")
	r.Remove("gw-d")
	if r.Get("gw-d") != nil {
		t.Error("Get should return nil after Remove")
	}

	// Second Remove is a no-op.
	r.Remove("gw-d")
}

func TestGatewayRegistry_GetReturnsCopy(t *testing.T) {
	r := newTestGatewayRegistry(t)
	r.Register("gw-e", "ws://10.0.0.5:8080", "10.0.0.5:9000")

	// Inject a session directly so Sessions is non-nil in the stored entry.
	r.mu.Lock()
	r.gateways["gw-e"].Sessions[SessionKey{ConnID: 42}] = true
	r.mu.Unlock()

	gw1 := r.Get("gw-e")
	// Mutate the returned copy's Sessions map.
	gw1.Sessions[SessionKey{ConnID: 99}] = true

	gw2 := r.Get("gw-e")
	if gw2.Sessions[SessionKey{ConnID: 99}] {
		t.Error("mutation of returned copy leaked into registry (Get did not deep-copy)")
	}
}

func TestGatewayRegistry_LiveGatewaysSnapshot(t *testing.T) {
	r := newTestGatewayRegistry(t)
	r.Register("gw-f", "ws://10.0.0.6:8080", "10.0.0.6:9000")
	r.Register("gw-g", "ws://10.0.0.7:8080", "10.0.0.7:9000")
	r.Register("gw-h", "ws://10.0.0.8:8080", "10.0.0.8:9000")
	// Mark one dead so we confirm all states are returned.
	r.MarkDead("gw-h")

	gateways := r.LiveGateways()
	if len(gateways) != 3 {
		t.Fatalf("LiveGateways: got %d entries, want 3", len(gateways))
	}

	// Inject a session into one returned entry and verify it doesn't propagate.
	for _, gw := range gateways {
		if gw.Sessions == nil {
			gw.Sessions = make(map[SessionKey]bool)
		}
		gw.Sessions[SessionKey{ConnID: 999}] = true
	}

	gateways2 := r.LiveGateways()
	for _, gw := range gateways2 {
		if gw.Sessions[SessionKey{ConnID: 999}] {
			t.Errorf("mutation of LiveGateways entry leaked into registry for gateway %q", gw.ID)
		}
	}
}
