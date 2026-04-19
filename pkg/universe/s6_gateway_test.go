package universe

// TestS6HandoffAcrossNodes is the S6 capstone validation gate.
//
// Runs under both colocated (all preset, single host) and distributed
// (coord-role with RoleGateway + separate host-role processes) topologies
// via forEachTopology. In colocated mode the embedded gateway dispatches
// to cell.Inbox directly; in distributed mode (WithGateway=true) the
// coord-role Process adds RoleGateway so it owns its own HostNetwork
// and routes frames over the MeshData wire to the host-role processes.
//
// The test drives a fake client through login + synthesized cross-host
// migration + disconnect, and verifies the session survives the host
// boundary crossing. Real entity-driven handoff requires a working game
// loop with spawned entities — the fixture uses WorldBase as the stub
// GameWorld, so the cross-host migration is synthesized via a direct
// coord.notifyPlayerMigrated call (the same entry point HandoffDriver
// uses on real entity transfers). This validates:
//
//   - sessionRoutes populated via Set + flipped via Migrate
//   - Gateway.OnUpstreamSwitch flips local session state
//   - Disconnect propagation cleans up gateway.sessions + sessionRoutes

import (
	"sync"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
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
	forEachTopology(t, FixtureConfig{
		CellsX: 2, CellsY: 2, CellSize: 1024,
		HostIDs:     []string{"host-a", "host-b"},
		WithGateway: true,
	}, func(t *testing.T, fx clusterFixture) {
		const hostIDA = "host-a"
		const hostIDB = "host-b"

		coord := fx.Coord()

		// 1. The fixture seeded a deterministic 2x2 layout, so the default
		//    LoginHandler (ErrLoginPending) stays in place for most tests.
		//    Override it here so the synthetic login marker actually resolves
		//    to the "alice" username. Both topologies end up with the same
		//    loginSvc instance on the coord-role Process (embedded gateway).
		if coord.loginSvc != nil {
			coord.loginSvc.handler = func(connID uint32, msgs [][]byte) (string, any, error) {
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
			}
		}

		// 2. SpawnResolver: route "alice" to a cell on host-a. The resolver is
		//    topology-blind — returns world-space coords only; the gateway
		//    calls CellAtPosition to find the current owning cell.
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

		// 3. Identify cells on each host. With round-robin layout over
		//    [host-a, host-b] on a 2x2 grid: cell_0_0→host-a, cell_1_0→host-b,
		//    cell_0_1→host-a, cell_1_1→host-b.
		cellKeys := []string{
			MeshCellID(CellID{X: 0, Y: 0}),
			MeshCellID(CellID{X: 1, Y: 0}),
			MeshCellID(CellID{X: 0, Y: 1}),
			MeshCellID(CellID{X: 1, Y: 1}),
		}
		var cellOnA, cellOnB string
		for _, k := range cellKeys {
			switch fx.CellOwner(k) {
			case hostIDA:
				if cellOnA == "" {
					cellOnA = k
				}
			case hostIDB:
				if cellOnB == "" {
					cellOnB = k
				}
			}
		}
		if cellOnA == "" || cellOnB == "" {
			t.Fatalf("expected cells on both hosts (a=%q b=%q)", cellOnA, cellOnB)
		}

		// 4. Point the router at cellOnA so login lands on host-a.
		targetCellMu.Lock()
		targetCellID = cellOnA
		targetCellMu.Unlock()

		// 5. Add a mock transport to the gateway's ConnManager to simulate a
		//    WebSocket client. ConnMgr is the gateway's ConnManager in both
		//    topologies (embedded gateway shares it with the coord).
		mt := &s6MockTransport{}
		connID := coord.ConnMgr.AddTransport(mt)

		// 6. Wait for the gateway routeEvents goroutine to pick up the connect
		//    event and add the connID to the login pending queue.
		waitFor(t, 3*time.Second, "gateway login service should see the pending connection", func() bool {
			if coord.gateway == nil || coord.gateway.loginSvc == nil {
				return false
			}
			_, pending := coord.gateway.loginSvc.pending[connID]
			return pending
		})

		// 7. Inject a synthetic login message: 0x01 marker + "alice".
		loginMsg := append([]byte{0x01}, []byte("alice")...)
		mt.InjectInput(loginMsg)

		// 8. Wait for the session to be established in coord.gateway.sessions.
		gwID := coord.gateway.id
		waitFor(t, 3*time.Second, "gateway should have a session for alice", func() bool {
			if coord.gateway == nil {
				return false
			}
			coord.gateway.mu.RLock()
			_, ok := coord.gateway.sessions[connID]
			coord.gateway.mu.RUnlock()
			return ok
		})

		// 9. Verify the coordinator's sessionRoutes table was populated.
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

		// 10. Synthesize a cross-host migration: directly call
		//     notifyPlayerMigrated, the same entry point HandoffDriver uses on
		//     real entity transfers.
		coord.notifyPlayerMigrated(gwID, connID, hostIDA, hostIDB, cellOnB)

		// 11. Verify the gateway flipped its session's hostID to destHost.
		waitFor(t, 3*time.Second, "gateway session hostID should flip to destHost after migration", func() bool {
			if coord.gateway == nil {
				return false
			}
			coord.gateway.mu.RLock()
			sess, ok := coord.gateway.sessions[connID]
			if !ok {
				coord.gateway.mu.RUnlock()
				return false
			}
			host := sess.hostID
			coord.gateway.mu.RUnlock()
			return host == hostIDB
		})

		// 12. Verify sessionRoutes shows the new HostID and a bumped epoch.
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

		// 13. Inject a disconnect: Unregister fires a Disconnect PlayerEvent
		//     through ConnManager.Events(), which routeEvents picks up and
		//     delegates to gateway.handleDisconnect.
		coord.ConnMgr.Unregister(connID)

		// 14. Wait for the session to be removed from coord.gateway.sessions.
		waitFor(t, 3*time.Second, "gateway sessions should be cleaned up after disconnect", func() bool {
			if coord.gateway == nil {
				return false
			}
			coord.gateway.mu.RLock()
			_, still := coord.gateway.sessions[connID]
			coord.gateway.mu.RUnlock()
			return !still
		})

		// 15. Verify sessionRoutes is also cleaned up.
		waitFor(t, 3*time.Second, "sessionRoutes should be cleaned up after disconnect", func() bool {
			_, still := coord.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
			return !still
		})
	})
}
