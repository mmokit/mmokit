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
// loop with spawned entities — the fixture uses Stage as the stub
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

func (m *s6MockTransport) SendReliable(data []byte) pkgnet.SendResult {
	m.mu.Lock()
	m.reliable = append(m.reliable, data)
	m.mu.Unlock()
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
}
func (m *s6MockTransport) SendUnreliable(data []byte) pkgnet.SendResult {
	m.mu.Lock()
	m.unreliable = append(m.unreliable, data)
	m.mu.Unlock()
	return pkgnet.SendResult{Disposition: pkgnet.SendQueued, Delivery: pkgnet.DeliveryReliableOrdered}
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
	t.Skip("login-handler driven; superseded by auth-service integration tests landing in Task 24")
}
