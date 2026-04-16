package universe

// TestS6HandoffAcrossNodes is the S6 capstone validation gate.
//
// It stands up a coordinator + 2 nodes + 1 standalone gateway in-process
// (using GatewayMode=always-proxy so the MeshData codec path is exercised
// regardless), drives a fake client through login + synthesized cross-host
// migration + disconnect, and verifies the session survives the host boundary
// crossing.
//
// Real entity-driven handoff requires a working game loop with spawned entities.
// The test fixture uses WorldBase as the stub GameWorld. The cross-host migration
// is therefore synthesized via a direct coord.notifyPlayerMigrated call — the
// same entry point the HandoffDriver uses on real entity transfers. Validates the
// full S6 control + data plane wiring:
//
//   - Standalone gateway registers with coordinator (RegisterGateway)
//   - PeerList broadcast includes the gateway in pl.Gateways
//   - Nodes open peerKindGateway streams to the gateway
//   - Gateway opens peerKindNode streams to each node
//   - Login on gateway routes through cached topology to the right cell
//   - sessionRoutes populated via Set + flipped via Migrate
//   - Gateway.OnUpstreamSwitch flips local session state
//   - Disconnect propagation cleans up gateway.sessions + sessionRoutes

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	pkgnet "github.com/zenion/mmoserver/pkg/net"
)

// s6MockTransport implements pkgnet.Transport for injecting fake client traffic.
type s6MockTransport struct {
	mu         sync.Mutex
	reliable   [][]byte
	unreliable [][]byte
	input      [][]byte
	opInput    [][]byte
	closed     bool
}

func (m *s6MockTransport) SendReliable(data []byte) {
	m.mu.Lock()
	m.reliable = append(m.reliable, data)
	m.mu.Unlock()
}
func (m *s6MockTransport) SendUnreliable(data []byte) {
	m.mu.Lock()
	m.unreliable = append(m.unreliable, data)
	m.mu.Unlock()
}
func (m *s6MockTransport) DrainInput() [][]byte {
	m.mu.Lock()
	r := m.input
	m.input = nil
	m.mu.Unlock()
	return r
}
func (m *s6MockTransport) DrainOpInput() [][]byte {
	m.mu.Lock()
	r := m.opInput
	m.opInput = nil
	m.mu.Unlock()
	return r
}
func (m *s6MockTransport) InjectInput(data []byte) {
	m.mu.Lock()
	m.input = append(m.input, data)
	m.mu.Unlock()
}
func (m *s6MockTransport) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

func TestS6HandoffAcrossNodes(t *testing.T) {
	// 1. Stand up the coordinator in pure coordinator mode (no local cells, no
	//    in-process gateway). Bare "coordinator" role leaves the WebSocket
	//    termination entirely to the standalone gateway process below.
	coord := NewCoordinator(Config{
		CellsX:        2,
		CellsY:        2,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			return "", nil, ErrLoginPending
		},
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. Stand up two nodes pointed at the coordinator.
	// Host IDs "test-node-0" and "test-node-3" are chosen because the
	// rendezvous hash (fnv64a) deterministically produces a 2-2 cell
	// split for the 2x2 grid with these IDs. Generic names like "node-a"
	// / "node-b" produce a 4-0 split (all cells to one host).
	const hostIDA = "test-node-0"
	const hostIDB = "test-node-3"
	const gwID = "test-gateway"

	nodeA := startS45Node(t, coordAddr, hostIDA)
	t.Cleanup(nodeA.Shutdown)

	nodeB := startS45Node(t, coordAddr, hostIDB)
	t.Cleanup(nodeB.Shutdown)

	// 3. Stand up the standalone gateway.
	//
	// The LoginHandler parses synthetic login messages: first byte 0x01 is the
	// login marker; the remaining bytes are the username.
	gwConnMgr := pkgnet.NewConnManager()
	gw := NewCoordinator(Config{
		Mode:            "gateway",
		GatewayID:       gwID,
		CoordinatorAddr: coordAddr,
		Headless:        true,
		ConnManager:     gwConnMgr,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			for _, m := range msgs {
				if len(m) > 0 && m[0] == 0x01 {
					username := string(m[1:])
					if username == "" {
						return "", nil, ErrLoginPending
					}
					return username, nil, nil
				}
			}
			return "", nil, ErrLoginPending
		},
	})
	// SpawnResolver lives on the authoritative coordinator process (not the
	// gateway) so the standalone gateway's ResolveSpawn RPC lands on a process
	// that can answer. Returns the world-space center of the named cell so
	// CellAtPosition on the gateway side routes to the correct host.
	var (
		targetCellMu sync.RWMutex
		targetCellID string
	)
	coord.SetSpawnResolver(func(username string) (float32, float32, bool) {
		targetCellMu.RLock()
		id := targetCellID
		targetCellMu.RUnlock()
		if id == "" {
			return 0, 0, false
		}
		cid, err := ParseCellID(id)
		if err != nil {
			return 0, 0, false
		}
		minX, minY, maxX, maxY := cid.WorldBounds(coords.CellSize)
		return (minX + maxX) / 2, (minY + maxY) / 2, true
	})
	gw.Build()

	// Start the gateway's event router (routeEvents) in a goroutine.
	// Build() returns early for gateway mode; the event loop is started by Start().
	// We run Start() in a background goroutine and cancel via context on cleanup.
	// Start() calls Shutdown() internally when the context is cancelled, so we
	// do not call gw.Shutdown() separately to avoid a double-shutdown.
	gwCtx, gwCancel := context.WithCancel(context.Background())
	t.Cleanup(gwCancel)
	go gw.Start(gwCtx)

	// 4. Wait for settle window to close + both nodes to own cells (5s settle
	//    window; generous 12s timeout).
	waitFor(t, 12*time.Second, "both nodes should own all 4 cells between them", func() bool {
		a := coord.hostRegistry.Get(hostIDA)
		b := coord.hostRegistry.Get(hostIDB)
		if a == nil || b == nil {
			return false
		}
		return len(a.OwnedCells)+len(b.OwnedCells) == 4
	})

	// 5. Both nodes must have their cellToHostMap fully populated via PeerList.
	waitFor(t, 3*time.Second, "nodeA should see all 4 cells in cellToHostMap", func() bool {
		nodeA.mu.RLock()
		defer nodeA.mu.RUnlock()
		return len(nodeA.cellToHostMap) == 4
	})
	waitFor(t, 3*time.Second, "nodeB should see all 4 cells in cellToHostMap", func() bool {
		nodeB.mu.RLock()
		defer nodeB.mu.RUnlock()
		return len(nodeB.cellToHostMap) == 4
	})

	// 6. Gateway must have registered with the coordinator.
	waitFor(t, 5*time.Second, "coordinator should have the gateway registered", func() bool {
		return coord.gatewayRegistry.Get(gwID) != nil
	})

	// 7. Gateway's topology must be populated — its cached topology should have
	//    all 4 cells once the PeerList has been received and processed.
	waitFor(t, 5*time.Second, "gateway topology should have all 4 cells", func() bool {
		gw.mu.RLock()
		defer gw.mu.RUnlock()
		if gw.gateway == nil || gw.gateway.topology == nil {
			return false
		}
		gw.gateway.topology.mu.RLock()
		n := len(gw.gateway.topology.cells)
		gw.gateway.topology.mu.RUnlock()
		return n == 4
	})

	// 8. Gateway should have HostNetwork peers to both nodes (peerKindNode).
	waitFor(t, 5*time.Second, "gateway should have nodeA as a peer", func() bool {
		gw.mu.RLock()
		defer gw.mu.RUnlock()
		if gw.gateway == nil || gw.gateway.hostNetwork == nil {
			return false
		}
		gw.gateway.hostNetwork.mu.RLock()
		_, ok := gw.gateway.hostNetwork.peers[hostIDA]
		gw.gateway.hostNetwork.mu.RUnlock()
		return ok
	})
	waitFor(t, 3*time.Second, "gateway should have nodeB as a peer", func() bool {
		gw.mu.RLock()
		defer gw.mu.RUnlock()
		if gw.gateway == nil || gw.gateway.hostNetwork == nil {
			return false
		}
		gw.gateway.hostNetwork.mu.RLock()
		_, ok := gw.gateway.hostNetwork.peers[hostIDB]
		gw.gateway.hostNetwork.mu.RUnlock()
		return ok
	})

	// 9. Each node should have connected to the gateway as a peerKindGateway peer.
	waitFor(t, 3*time.Second, "nodeA should have the gateway as a peer", func() bool {
		h := nodeA.localHost()
		if h == nil || h.Network == nil {
			return false
		}
		h.Network.mu.RLock()
		p, ok := h.Network.peers[gwID]
		h.Network.mu.RUnlock()
		return ok && p.kind == peerKindGateway
	})
	waitFor(t, 3*time.Second, "nodeB should have the gateway as a peer", func() bool {
		h := nodeB.localHost()
		if h == nil || h.Network == nil {
			return false
		}
		h.Network.mu.RLock()
		p, ok := h.Network.peers[gwID]
		h.Network.mu.RUnlock()
		return ok && p.kind == peerKindGateway
	})

	// 10. Identify cells on each node so we can set up a cross-host migration.
	hostA := coord.hostRegistry.Get(hostIDA)
	hostB := coord.hostRegistry.Get(hostIDB)
	var cellOnA, cellOnB string
	for cid := range hostA.OwnedCells {
		cellOnA = cid
		break
	}
	for cid := range hostB.OwnedCells {
		cellOnB = cid
		break
	}
	if cellOnA == "" || cellOnB == "" {
		t.Skipf("rendezvous gave one node all cells (a=%d b=%d); can't exercise cross-host path",
			len(hostA.OwnedCells), len(hostB.OwnedCells))
	}

	// 11. Set the player router to route "alice" to a cell on nodeA (src host).
	targetCellMu.Lock()
	targetCellID = cellOnA
	targetCellMu.Unlock()

	// 12. Add a mock transport to the gateway's ConnManager to simulate a
	//     WebSocket connection from a client.
	mt := &s6MockTransport{}
	connID := gwConnMgr.AddTransport(mt)

	// 13. Wait for the gateway routeEvents goroutine to pick up the connect event
	//     and add the connID to the login pending queue. The connect event fires
	//     immediately but routeEvents is async.
	waitFor(t, 3*time.Second, "gateway login service should see the pending connection", func() bool {
		if gw.gateway == nil || gw.gateway.loginSvc == nil {
			return false
		}
		_, pending := gw.gateway.loginSvc.pending[connID]
		return pending
	})

	// 14. Inject a synthetic login message: 0x01 marker + "alice".
	loginMsg := append([]byte{0x01}, []byte("alice")...)
	mt.InjectInput(loginMsg)

	// 15. Wait for the session to be established in gw.gateway.sessions.
	//     The loginTicker fires at TickRate (50ms for default 20Hz), so
	//     we give it up to 3 seconds.
	waitFor(t, 3*time.Second, "gateway should have a session for alice (connID)", func() bool {
		if gw.gateway == nil {
			return false
		}
		gw.gateway.mu.RLock()
		_, ok := gw.gateway.sessions[connID]
		gw.gateway.mu.RUnlock()
		return ok
	})

	// 16. Verify the coordinator's sessionRoutes table was populated by the
	//     gateway's SessionAnnounce over the MeshControl stream.
	waitFor(t, 3*time.Second, "sessionRoutes should contain the session after login", func() bool {
		route, ok := coord.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
		return ok && route != nil && route.HostID == hostIDA
	})

	// Read the initial epoch before migration.
	route, ok := coord.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
	if !ok {
		t.Fatal("sessionRoutes entry missing after login wait")
	}
	initialEpoch := route.Epoch

	// 17. Synthesize a cross-host migration: directly call notifyPlayerMigrated,
	//     which is what the HandoffDriver calls on a real entity transfer.
	//     Source = hostIDA, destination = hostIDB, destination cell = cellOnB.
	coord.notifyPlayerMigrated(gwID, connID, hostIDA, hostIDB, cellOnB)

	// 18. Verify the gateway flipped its session's hostID to the destination host.
	//     OnUpstreamSwitch is called synchronously from notifyPlayerMigrated (standalone
	//     path sends a CoordMessage.UpstreamSwitch; the gateway dispatches it on its
	//     recv loop goroutine). Allow up to 3s for the round-trip.
	waitFor(t, 3*time.Second, "gateway session hostID should flip to destHost after migration", func() bool {
		if gw.gateway == nil {
			return false
		}
		gw.gateway.mu.RLock()
		sess, ok := gw.gateway.sessions[connID]
		if !ok {
			gw.gateway.mu.RUnlock()
			return false
		}
		host := sess.hostID
		gw.gateway.mu.RUnlock()
		return host == hostIDB
	})

	// 19. Verify sessionRoutes shows the new HostID and a bumped epoch.
	route2, ok2 := coord.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
	if !ok2 {
		t.Fatal("sessionRoutes entry missing after migration")
	}
	if route2.HostID != hostIDB {
		t.Errorf("sessionRoutes.HostID = %q, want %q", route2.HostID, hostIDB)
	}
	if route2.CellID != cellOnB {
		t.Errorf("sessionRoutes.CellID = %q, want %q", route2.CellID, cellOnB)
	}
	if route2.Epoch <= initialEpoch {
		t.Errorf("sessionRoutes.Epoch = %d, want > %d (should have been bumped by Migrate)", route2.Epoch, initialEpoch)
	}

	// 20. Inject a disconnect: Unregister fires a Disconnect PlayerEvent through
	//     ConnManager.Events(), which routeEvents picks up and delegates to
	//     gateway.handleDisconnect.
	gwConnMgr.Unregister(connID)

	// 21. Wait for the session to be removed from gw.gateway.sessions.
	waitFor(t, 3*time.Second, "gateway sessions should be cleaned up after disconnect", func() bool {
		if gw.gateway == nil {
			return false
		}
		gw.gateway.mu.RLock()
		_, still := gw.gateway.sessions[connID]
		gw.gateway.mu.RUnlock()
		return !still
	})

	// 22. Verify sessionRoutes is also cleaned up.
	//     handleDisconnect on a standalone gateway sends a ClientDisconnect
	//     MeshFrame to the remote node. The coordinator's sessionRoutes cleanup
	//     happens in Gateway.handleDisconnect when coord is non-nil (embedded mode).
	//     In standalone mode the coordinator receives a SessionGone notification
	//     via the HostMessage.PlayerMigrated or SessionAnnounce path. For T11 we
	//     verify coord.sessionRoutes.Get returns ok=false after the disconnect.
	//     The standalone path removes the route when the coordinator processes the
	//     SessionAnnounce removal — give it 3s for the round-trip.
	waitFor(t, 3*time.Second, "sessionRoutes should be cleaned up after disconnect", func() bool {
		_, still := coord.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
		return !still
	})
}
