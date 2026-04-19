package universe

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// distributedFixture spins up one coord-role *Process (Mode=coordinator,
// ControlListen=127.0.0.1:0) plus N host-role *Coordinators (Mode=host,
// CoordinatorAddr=<coord addr>), connected via real gRPC MeshControl —
// the same wire path production uses. Layout seeding lands in Task 4.
type distributedFixture struct {
	coord *Process
	hosts map[string]*Process // hostID -> host-role *Process
	order []string                // declared host order (for HostIDs())
}

func newDistributedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	cfg.normalize()
	coords.SetCellSize(cfg.CellSize)

	// 1. Coord-role process on an ephemeral port.
	coordMode := "coordinator"
	if cfg.WithGateway {
		coordMode = "coordinator,gateway"
	}
	coord := New(Config{
		CellsX:        cfg.CellsX,
		CellsY:        cfg.CellsY,
		CellSize:      cfg.CellSize,
		Mode:          coordMode,
		ControlListen: "127.0.0.1:0",
		Headless:      true,
		ConnManager:   net.NewConnManager(),
		Logger:        logger.New(),
		LoginHandler:  func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()

	// Spin up the coord's event router when an embedded gateway is present
	// so session-handoff tests (s6 family) can drive login events through
	// ConnMgr.Events() + the loginTicker. Without this the gateway would
	// never see the client connect event.
	coordCtx, coordCancel := context.WithCancel(context.Background())
	if coord.gateway != nil {
		go coord.routeEvents(coordCtx)
	}
	t.Cleanup(func() {
		coordCancel()
		coord.Shutdown()
	})

	coordAddr := coord.controlListener.Addr().String()

	// 2. One host-role process per host ID, each dialing the coord.
	hosts := make(map[string]*Process, len(cfg.HostIDs))
	for _, hid := range cfg.HostIDs {
		host := New(Config{
			CellsX:          cfg.CellsX,
			CellsY:          cfg.CellsY,
			CellSize:        cfg.CellSize,
			Mode:            "host",
			CoordinatorAddr: coordAddr,
			HostID:          hid,
			GatewayMode:     cfg.GatewayMode,
			Headless:        true,
			ConnManager:     net.NewConnManager(),
			Logger:          logger.New(),
			LoginHandler:    func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
		})
		host.SetWorld(func(base *WorldBase) GameWorld { return base })
		host.Build()
		t.Cleanup(host.Shutdown)
		hosts[hid] = host
	}

	// 3. Wait until every host has reached the coord's hostRegistry. The
	// settle loop may still be running; that's fine — Task 4 bypasses it
	// before seeding layout.
	regDeadline := time.Now().Add(3 * time.Second)
	for _, hid := range cfg.HostIDs {
		for {
			if rh := coord.hostRegistry.Get(hid); rh != nil {
				break
			}
			if time.Now().After(regDeadline) {
				t.Fatalf("distributedFixture: host %q failed to register within 3s", hid)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}

	// 4. Bypass the 5s settle window — our tests seed layout explicitly.
	// Flip settled before dispatching so the settle loop's first rebalance
	// is a no-op rather than a rendezvous placement that would stomp our
	// seeding. firstRegistered is already true (set by onHostRegistered
	// during step 3), so the settle loop has already picked up the clock.
	ae := coord.assignmentEngine
	ae.mu.Lock()
	ae.settled = true
	ae.mu.Unlock()

	// 4b. Explicitly broadcast the current PeerList so every host learns
	// about every other host. Without this, onHostRegistered only sends a
	// TARGETED initial PeerList to the newly-registering host — host-a
	// never learns host-b exists unless a settle-window rebalance fires.
	// With settled=true we've just disabled that rebalance, so we must
	// drive the broadcast manually.
	ae.broadcastPeerList()

	// 5. Drive CellAssign for every (cell, host) in the declared layout.
	// dispatchCellAssign sends NetIDRangeGrant + CellAssign over MeshControl;
	// the host executor creates the cell and responds with CellReady,
	// which updates hostRegistry.OwnedCells on the coord side.
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		ae.dispatchCellAssign(hostID, cellKey)
	}

	// 6. Wait for every (cell, host) pair to actually land on the host
	// (not just the registry — dispatchCellAssign marks OwnedCells at
	// dispatch time, which races ahead of the host's async cell creation).
	// Checking the host-side *Cell is the authoritative signal. Deadline is
	// generous; per-cell createNode + CellReady roundtrip is well under
	// 500ms in practice.
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer seedCancel()
	for _, cellKey := range sortedKeys(cfg.Layout) {
		hostID := cfg.Layout[cellKey]
		if err := waitForCellOnHost(seedCtx, hosts, hostID, cellKey); err != nil {
			t.Fatalf("distributedFixture: seed %s -> %s: %v", cellKey, hostID, err)
		}
	}

	// 6b. Now that every seeded cell has actually landed on its host, push
	// a fresh PeerList so hosts and the embedded gateway (when WithGateway
	// is set) see the full cell-ownership view. The earlier broadcast on
	// step 4b fired before any CellAssign dispatched, so its PeerList had
	// zero CellOwnership entries — without this second broadcast, the
	// embedded gateway's cachedTopology.cells stays empty and login
	// dispatch falls through to anyCellID() (which picks a random host).
	ae.broadcastPeerList()

	// 7. Wait until every host sees every OTHER host as a HostNetwork
	// peer. PeerList broadcasts that populate host.Network.peers are
	// async — layout seeding can complete before the broadcast has
	// landed on every host. Without this wait, the first cross-host
	// operation (split/merge/migrate) can fail with "no peer host-X"
	// when the orchestrator's executor tries to ship state.
	peerCtx, peerCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer peerCancel()
	for _, hid := range cfg.HostIDs {
		for _, otherID := range cfg.HostIDs {
			if otherID == hid {
				continue
			}
			if err := waitForPeerVisible(peerCtx, hosts[hid], otherID); err != nil {
				t.Fatalf("distributedFixture: host %q peer %q: %v", hid, otherID, err)
			}
		}
	}

	// 8. Wait for every host's cellToHostMap to contain ALL seeded cells.
	// The final PeerList broadcast (step 6b) is async — each host processes
	// it on its meshControlClient recv goroutine. Tests that call
	// SendHandoffPrepare immediately after the fixture returns will see an
	// empty cellToHostMap on the source host, causing grpcBridge.resolveDest
	// to return "" and silently drop the message. Waiting here closes that
	// race: once cellToHostMap has all cells, the grpcBridge can resolve
	// destinations correctly and route messages over gRPC.
	cellCtx, cellCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cellCancel()
	allCellKeys := sortedKeys(cfg.Layout)
	for _, hid := range cfg.HostIDs {
		h := hosts[hid]
		if err := waitForCellToHostMap(cellCtx, h, allCellKeys); err != nil {
			t.Fatalf("distributedFixture: host %q cellToHostMap: %v", hid, err)
		}
	}

	return &distributedFixture{
		coord: coord,
		hosts: hosts,
		order: append([]string(nil), cfg.HostIDs...),
	}
}

// waitForCellToHostMap polls the host-role Process's cellToHostMap until
// every key in wantKeys is present, or ctx expires. Closes the race between
// ae.broadcastPeerList() (step 6b) and the test body: the broadcast lands
// asynchronously on each host's meshControlClient recv goroutine, so
// grpcBridge.resolveDest can see an empty map if the test sends before the
// PeerList is processed.
func waitForCellToHostMap(ctx context.Context, host *Process, wantKeys []string) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		host.Control.mu.RLock()
		missing := 0
		for _, k := range wantKeys {
			if _, ok := host.Control.cellToHostMap[k]; !ok {
				missing++
			}
		}
		host.Control.mu.RUnlock()
		if missing == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cellToHostMap missing %d/%d cells before deadline", missing, len(wantKeys))
		case <-tick.C:
		}
	}
}

// waitForPeerVisible polls the host-role Process's HostNetwork.peers
// map until otherID is present or ctx expires. Mirrors the check in
// s4_5_cross_host_test.go — closes the PeerList-broadcast race window
// before the fixture hands control to the test body.
func waitForPeerVisible(ctx context.Context, host *Process, otherID string) error {
	if host == nil {
		return fmt.Errorf("nil host")
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		h := host.localHost()
		if h != nil && h.Network != nil {
			h.Network.mu.RLock()
			_, ok := h.Network.peers[otherID]
			h.Network.mu.RUnlock()
			if ok {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("peer %s not visible before deadline", otherID)
		case <-tick.C:
		}
	}
}

// waitForCellOnHost polls the host-role Process's own Hosts map
// until cellKey is present as an in-process *Cell, or ctx expires.
// Host-side presence is the authoritative "cell is alive here" signal —
// the coord's hostRegistry marks OwnedCells at dispatch time (before
// the CellReady roundtrip), so a registry-only poll would race.
func waitForCellOnHost(ctx context.Context, hosts map[string]*Process, hostID, cellKey string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if h, ok := hosts[hostID]; ok {
			if lh := h.localHost(); lh != nil && lh.CellByID(cellKey) != nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cell %s not present on host %s before deadline", cellKey, hostID)
		case <-tick.C:
		}
	}
}

func (f *distributedFixture) Coord() *Process { return f.coord }
func (f *distributedFixture) HostIDs() []string   { return f.order }

// AnyCell returns the in-process *Cell for cellKey regardless of which
// host owns it. Equivalent to CellOn(CellOwner(cellKey), cellKey).
// Returns nil if no host currently owns the cell.
func (f *distributedFixture) AnyCell(cellKey string) *Cell {
	owner := f.CellOwner(cellKey)
	if owner == "" {
		return nil
	}
	return f.CellOn(owner, cellKey)
}

func (f *distributedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

// HostOwnsCell reaches into the host-role Process's local Host.
// Each host-role *Process has exactly one local Host (itself).
func (f *distributedFixture) HostOwnsCell(hostID, cellKey string) bool {
	return f.CellOn(hostID, cellKey) != nil
}

func (f *distributedFixture) CellOn(hostID, cellKey string) *Cell {
	host, ok := f.hosts[hostID]
	if !ok {
		return nil
	}
	lh := host.localHost()
	if lh == nil {
		return nil
	}
	return lh.CellByID(cellKey)
}

// WaitForCellOwner polls host-side state — not just the coord's registry —
// because dispatchCellAssign updates hostRegistry.OwnedCells at dispatch
// time (before the host has actually created the cell via CellReady).
// Waiting on the host-side *Cell is the authoritative "cell is alive
// here" signal and avoids the race that tripped early migrate tests.
func (f *distributedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	if err := waitForCellOnHost(ctx, f.hosts, hostID, cellKey); err != nil {
		return fmt.Errorf("distributedFixture: %w", err)
	}
	return nil
}

// StopHost requests a graceful shutdown of the named host. In
// distributed mode it calls Shutdown() on the host-role Process,
// which sends GracefulLeave over the control stream and waits for
// CellsDrained before returning — matches production.
func (f *distributedFixture) StopHost(ctx context.Context, hostID string) error {
	host, ok := f.hosts[hostID]
	if !ok {
		return fmt.Errorf("StopHost: unknown host %q", hostID)
	}
	// host.Shutdown() sends GracefulLeave over the control stream and
	// waits for CellsDrained before returning — matches production.
	host.Shutdown()
	delete(f.hosts, hostID)
	filtered := f.order[:0]
	for _, h := range f.order {
		if h != hostID {
			filtered = append(filtered, h)
		}
	}
	f.order = filtered
	return nil
}

// WaitForCellReleased polls until the host-role Process's own Hosts
// map no longer reports cellKey, or ctx expires. Used after migrate /
// drain to observe the source host's async CellRelease completing —
// applyMigrateCommit's remote branch fires CellRelease fire-and-forget
// over MeshControl, so req.Done can close before the source teardown
// actually lands. STAGE-2 TODO: make BeginMigrate block req.Done until
// CellStopped arrives, so this helper collapses to a no-op.
func (f *distributedFixture) WaitForCellReleased(ctx context.Context, cellKey, hostID string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		host, ok := f.hosts[hostID]
		if !ok {
			return nil // unknown host can't own anything
		}
		lh := host.localHost()
		if lh == nil || lh.CellByID(cellKey) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("distributedFixture: cell %s still on host %s before deadline", cellKey, hostID)
		case <-tick.C:
		}
	}
}
