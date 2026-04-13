package universe

import (
	"sync"
	"testing"
)

func TestSessionKey_String(t *testing.T) {
	k := SessionKey{GatewayID: "inproc", ConnID: 42}
	if got := k.String(); got != "inproc:42" {
		t.Errorf("String() = %q, want %q", got, "inproc:42")
	}

	k2 := SessionKey{GatewayID: "gw-1", ConnID: 0}
	if got := k2.String(); got != "gw-1:0" {
		t.Errorf("String() = %q, want %q", got, "gw-1:0")
	}
}

func TestSessionRoutes_SetGet_RoundTrip(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: 7}

	r.Set(&SessionRoute{Key: key, CellID: "cell_0_0", Epoch: 1})

	got, ok := r.Get(key)
	if !ok {
		t.Fatal("Get: expected ok=true, got false")
	}
	if got.CellID != "cell_0_0" {
		t.Errorf("CellID = %q, want %q", got.CellID, "cell_0_0")
	}
	if got.Epoch != 1 {
		t.Errorf("Epoch = %d, want 1", got.Epoch)
	}
}

func TestSessionRoutes_Get_DeepCopy(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: 1}
	r.Set(&SessionRoute{Key: key, CellID: "cell_0_0", Epoch: 2})

	got, _ := r.Get(key)
	got.CellID = "mutated"
	got.Epoch = 99

	// Stored value must be unchanged
	stored, ok := r.Get(key)
	if !ok {
		t.Fatal("Get after mutation: expected ok=true")
	}
	if stored.CellID != "cell_0_0" {
		t.Errorf("stored CellID = %q after mutation, want %q", stored.CellID, "cell_0_0")
	}
	if stored.Epoch != 2 {
		t.Errorf("stored Epoch = %d after mutation, want 2", stored.Epoch)
	}
}

func TestSessionRoutes_Get_Missing(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: "nope", ConnID: 999}
	got, ok := r.Get(key)
	if ok {
		t.Error("Get: expected ok=false for missing key")
	}
	if got != nil {
		t.Error("Get: expected nil for missing key")
	}
}

func TestSessionRoutes_Remove(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: 5}
	r.Set(&SessionRoute{Key: key, CellID: "cell_1_0", Epoch: 1})

	r.Remove(key)

	_, ok := r.Get(key)
	if ok {
		t.Error("Get after Remove: expected ok=false")
	}

	// Remove on absent key should be a no-op (no panic)
	r.Remove(key)
}

func TestSessionRoutes_Migrate_BumpsEpoch(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: 3}
	r.Set(&SessionRoute{Key: key, CellID: "cell_0_0", HostID: "host-a", Epoch: 1})

	epoch, ok := r.Migrate(key, "host-b", "cell_1_0")
	if !ok {
		t.Fatal("Migrate: expected ok=true on existing key")
	}
	if epoch != 2 {
		t.Errorf("Migrate epoch = %d, want 2", epoch)
	}

	got, _ := r.Get(key)
	if got.Epoch != 2 {
		t.Errorf("stored Epoch = %d, want 2", got.Epoch)
	}
	if got.HostID != "host-b" {
		t.Errorf("HostID = %q, want %q", got.HostID, "host-b")
	}
	if got.CellID != "cell_1_0" {
		t.Errorf("CellID = %q, want %q", got.CellID, "cell_1_0")
	}

	// Second Migrate bumps again
	epoch2, ok2 := r.Migrate(key, "host-c", "cell_2_0")
	if !ok2 {
		t.Fatal("second Migrate: expected ok=true")
	}
	if epoch2 != 3 {
		t.Errorf("second Migrate epoch = %d, want 3", epoch2)
	}
}

func TestSessionRoutes_Migrate_Missing(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: "ghost", ConnID: 1}
	epoch, ok := r.Migrate(key, "host-x", "cell_0_0")
	if ok {
		t.Error("Migrate on missing key: expected ok=false")
	}
	if epoch != 0 {
		t.Errorf("Migrate on missing key: epoch = %d, want 0", epoch)
	}
}

func TestSessionRoutes_RemoveByGateway(t *testing.T) {
	r := newSessionRoutes()
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: "gw-1", ConnID: 1}, CellID: "cell_0_0", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: "gw-1", ConnID: 2}, CellID: "cell_0_1", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: "gw-2", ConnID: 3}, CellID: "cell_1_0", Epoch: 1})

	n := r.RemoveByGateway("gw-1")
	if n != 2 {
		t.Errorf("RemoveByGateway returned %d, want 2", n)
	}

	// gw-1 entries gone
	if _, ok := r.Get(SessionKey{GatewayID: "gw-1", ConnID: 1}); ok {
		t.Error("gw-1:1 still present after RemoveByGateway")
	}
	if _, ok := r.Get(SessionKey{GatewayID: "gw-1", ConnID: 2}); ok {
		t.Error("gw-1:2 still present after RemoveByGateway")
	}

	// gw-2 entry untouched
	if _, ok := r.Get(SessionKey{GatewayID: "gw-2", ConnID: 3}); !ok {
		t.Error("gw-2:3 was incorrectly removed")
	}
}

func TestSessionRoutes_RemoveByHost(t *testing.T) {
	r := newSessionRoutes()
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 10}, HostID: "host-a", CellID: "cell_0_0", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 11}, HostID: "host-a", CellID: "cell_0_1", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 12}, HostID: "host-b", CellID: "cell_1_0", Epoch: 1})

	n := r.RemoveByHost("host-a")
	if n != 2 {
		t.Errorf("RemoveByHost returned %d, want 2", n)
	}

	if _, ok := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 10}); ok {
		t.Error("conn 10 still present after RemoveByHost(host-a)")
	}
	if _, ok := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 11}); ok {
		t.Error("conn 11 still present after RemoveByHost(host-a)")
	}
	if _, ok := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 12}); !ok {
		t.Error("conn 12 was incorrectly removed")
	}
}

func TestSessionRoutes_RemapCell(t *testing.T) {
	r := newSessionRoutes()
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 20}, CellID: "cell_old_0", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 21}, CellID: "cell_old_1", Epoch: 1})
	r.Set(&SessionRoute{Key: SessionKey{GatewayID: InprocGatewayID, ConnID: 22}, CellID: "cell_keep", Epoch: 1})

	n := r.remapCell(func(cellID string) bool {
		return cellID == "cell_old_0" || cellID == "cell_old_1"
	}, "cell_new")
	if n != 2 {
		t.Errorf("remapCell returned %d, want 2", n)
	}

	got20, _ := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 20})
	if got20.CellID != "cell_new" {
		t.Errorf("conn 20 CellID = %q, want %q", got20.CellID, "cell_new")
	}
	got21, _ := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 21})
	if got21.CellID != "cell_new" {
		t.Errorf("conn 21 CellID = %q, want %q", got21.CellID, "cell_new")
	}
	got22, _ := r.Get(SessionKey{GatewayID: InprocGatewayID, ConnID: 22})
	if got22.CellID != "cell_keep" {
		t.Errorf("conn 22 CellID = %q, want %q", got22.CellID, "cell_keep")
	}
}

func TestSessionRoutes_Concurrency(t *testing.T) {
	r := newSessionRoutes()
	key := SessionKey{GatewayID: InprocGatewayID, ConnID: 100}
	r.Set(&SessionRoute{Key: key, CellID: "cell_0_0", Epoch: 1})

	const iters = 100
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			r.Set(&SessionRoute{Key: key, CellID: "cell_0_0", Epoch: 1})
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			r.Get(key)
		}
	}()

	wg.Wait()
}
