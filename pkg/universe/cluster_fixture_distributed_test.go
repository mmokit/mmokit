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

// distributedFixture spins up one coord-role *Coordinator (Mode=coordinator,
// ControlListen=127.0.0.1:0) plus N host-role *Coordinators (Mode=host,
// CoordinatorAddr=<coord addr>), connected via real gRPC MeshControl —
// the same wire path production uses. Layout seeding lands in Task 4.
type distributedFixture struct {
	coord *Coordinator
	hosts map[string]*Coordinator // hostID -> host-role *Coordinator
	order []string                // declared host order (for HostIDs())
}

func newDistributedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	coords.SetCellSize(cfg.CellSize)

	// 1. Coord-role process on an ephemeral port.
	coord := NewCoordinator(Config{
		CellsX:        cfg.CellsX,
		CellsY:        cfg.CellsY,
		CellSize:      cfg.CellSize,
		Mode:          "coordinator",
		ControlListen: "127.0.0.1:0",
		Headless:      true,
		ConnManager:   net.NewConnManager(),
		Logger:        logger.New(),
		LoginHandler:  func(connID uint32, msgs [][]byte) (string, any, error) { return "", nil, ErrLoginPending },
	})
	coord.SetWorld(func(base *WorldBase) GameWorld { return base })
	coord.Build()
	t.Cleanup(coord.Shutdown)

	coordAddr := coord.controlListener.Addr().String()

	// 2. One host-role process per host ID, each dialing the coord.
	hosts := make(map[string]*Coordinator, len(cfg.HostIDs))
	for _, hid := range cfg.HostIDs {
		host := NewCoordinator(Config{
			CellsX:          cfg.CellsX,
			CellsY:          cfg.CellsY,
			CellSize:        cfg.CellSize,
			Mode:            "host",
			CoordinatorAddr: coordAddr,
			HostID:          hid,
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

	return &distributedFixture{
		coord: coord,
		hosts: hosts,
		order: append([]string(nil), cfg.HostIDs...),
	}
}

func (f *distributedFixture) Coord() *Coordinator { return f.coord }
func (f *distributedFixture) HostIDs() []string   { return f.order }

func (f *distributedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

// HostOwnsCell reaches into the host-role Coordinator's local Host.
// Each host-role *Coordinator has exactly one local Host (itself).
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

func (f *distributedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	if err := waitForCellOwnerViaRegistry(ctx, f.coord, cellKey, hostID); err != nil {
		return fmt.Errorf("distributedFixture: %w", err)
	}
	return nil
}
