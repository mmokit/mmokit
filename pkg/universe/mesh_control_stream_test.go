package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/logger"
)

func newStreamFenceServer(t *testing.T) *meshControlServer {
	t.Helper()
	return &meshControlServer{
		log:            logger.New(),
		streams:        make(map[string]*controlStream),
		gatewayStreams: make(map[string]*controlStream),
	}
}

// TestReleaseHostStream_GatedOnPointerIdentity is the reconnect-race
// regression. A stale handler's teardown used to delete unconditionally by
// the payload-supplied hostID, which removed a freshly reconnected host's
// registration and then ran MarkDead / UnregisterByHost /
// reassignOrphanedCells against it. The release must report false — and
// therefore skip the entire teardown — unless it still owns the entry.
func TestReleaseHostStream_GatedOnPointerIdentity(t *testing.T) {
	s := newStreamFenceServer(t)

	stale := &controlStream{kill: make(chan struct{}), gen: 1}
	fresh := &controlStream{kill: make(chan struct{}), gen: 2}
	s.streams["host-a"] = fresh

	if s.releaseHostStream("host-a", stale) {
		t.Fatal("stale stream released the reconnected registration")
	}
	if got := s.streams["host-a"]; got != fresh {
		t.Fatalf("registration = %v, want the fresh stream", got)
	}

	if !s.releaseHostStream("host-a", fresh) {
		t.Fatal("current stream failed to release its own registration")
	}
	if _, ok := s.streams["host-a"]; ok {
		t.Fatal("registration survived its owner's release")
	}
	if s.releaseHostStream("host-a", fresh) {
		t.Fatal("double release reported success")
	}
}

// TestReleaseGatewayStream_GatedOnPointerIdentity mirrors the host case. The
// gateway teardown additionally runs sessionRoutes.RemoveByGateway, which
// against a reconnected gateway wipes routes for live sessions.
func TestReleaseGatewayStream_GatedOnPointerIdentity(t *testing.T) {
	s := newStreamFenceServer(t)

	stale := &controlStream{kill: make(chan struct{}), gen: 1}
	fresh := &controlStream{kill: make(chan struct{}), gen: 2}
	s.gatewayStreams["gw-a"] = fresh

	if s.releaseGatewayStream("gw-a", stale) {
		t.Fatal("stale gateway stream released the reconnected registration")
	}
	if got := s.gatewayStreams["gw-a"]; got != fresh {
		t.Fatalf("registration = %v, want the fresh stream", got)
	}
	if !s.releaseGatewayStream("gw-a", fresh) {
		t.Fatal("current gateway stream failed to release its own registration")
	}
	if s.releaseGatewayStream("gw-a", fresh) {
		t.Fatal("double release reported success")
	}
}

// TestRegisterHostStream_EvictsPredecessor asserts registration replaces the
// old record and closes its kill channel, so the stale handler actually
// returns instead of lingering with a live gRPC stream.
func TestRegisterHostStream_EvictsPredecessor(t *testing.T) {
	s := newStreamFenceServer(t)

	first := s.registerHostStream("host-a", nil)
	second := s.registerHostStream("host-a", nil)

	if first == second {
		t.Fatal("re-registration returned the same record")
	}
	if second.gen <= first.gen {
		t.Fatalf("generation did not advance: %d -> %d", first.gen, second.gen)
	}
	select {
	case <-first.kill:
	default:
		t.Fatal("predecessor kill channel was not closed on eviction")
	}
	select {
	case <-second.kill:
		t.Fatal("current stream's kill channel was closed")
	default:
	}
	if got := s.streams["host-a"]; got != second {
		t.Fatalf("registration = %v, want the second record", got)
	}
}

// TestRegisterGatewayStream_EvictsPredecessor mirrors the host case.
func TestRegisterGatewayStream_EvictsPredecessor(t *testing.T) {
	s := newStreamFenceServer(t)

	first := s.registerGatewayStream("gw-a", nil)
	second := s.registerGatewayStream("gw-a", nil)

	select {
	case <-first.kill:
	default:
		t.Fatal("predecessor kill channel was not closed on eviction")
	}
	if got := s.gatewayStreams["gw-a"]; got != second {
		t.Fatalf("registration = %v, want the second record", got)
	}
}

// TestCancelStream_ClosesKillIdempotently covers the admin `host kill` path
// against the collapsed record, including the double-cancel case that used to
// be handled by an explicit non-blocking drain.
func TestCancelStream_ClosesKillIdempotently(t *testing.T) {
	s := newStreamFenceServer(t)

	if s.cancelStream("host-missing") {
		t.Fatal("cancelStream reported success for an unknown host")
	}

	cs := s.registerHostStream("host-a", nil)
	if !s.cancelStream("host-a") {
		t.Fatal("cancelStream reported failure for a registered host")
	}
	select {
	case <-cs.kill:
	default:
		t.Fatal("cancelStream did not close the kill channel")
	}
	if !s.cancelStream("host-a") {
		t.Fatal("second cancelStream reported failure")
	}
}

// TestCellReadyArbitration_RejectsStaleAnnouncement covers the ownership
// arbitration: AssignCell only ADDS to host.OwnedCells and HostForCell is a
// map-order linear scan, so a stale re-announce (reannounceOwnedCells after a
// stream blip during which the cell was reassigned) would otherwise produce a
// nondeterministic two-owner registry.
func TestCellReadyArbitration_RejectsStaleAnnouncement(t *testing.T) {
	log := logger.New()
	reg := NewHostRegistry(log)
	reg.Register("host-a", "addr-a", false, false)
	reg.Register("host-b", "addr-b", false, false)

	cell := MeshCellID("cell_0_0")
	if err := reg.AssignCell("host-b", cell); err != nil {
		t.Fatalf("AssignCell: %v", err)
	}
	if got := reg.HostForCell(cell); got != "host-b" {
		t.Fatalf("HostForCell = %q, want host-b", got)
	}

	// Simulate the arbitration branch in handleHostControl's CellReady case
	// for a stale announcement from host-a.
	announcer := "host-a"
	cur := reg.HostForCell(cell)
	rejected := false
	if cur != "" && cur != announcer {
		if h := reg.Get(cur); h != nil && h.State != RemoteHostDead {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("stale CellReady from host-a was not rejected while host-b is live")
	}
	if got := reg.HostForCell(cell); got != "host-b" {
		t.Fatalf("ownership changed to %q on a rejected announcement", got)
	}
	if h := reg.Get("host-a"); h != nil && len(h.OwnedCells) != 0 {
		t.Fatalf("host-a OwnedCells = %v, want empty", h.OwnedCells)
	}

	// Once the current owner is Dead the announcement must be accepted:
	// crash recovery depends on it.
	reg.MarkDead("host-b")
	cur = reg.HostForCell(cell)
	accepted := true
	if cur != "" && cur != announcer {
		if h := reg.Get(cur); h != nil && h.State != RemoteHostDead {
			accepted = false
		}
	}
	if !accepted {
		t.Fatal("CellReady was rejected even though the current owner is Dead")
	}
}
