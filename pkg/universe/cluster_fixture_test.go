package universe

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/net"
)

// ─────────────────────────────────────────────────────────────────────────────
// Dual-topology test harness — see
// docs/superpowers/specs/2026-04-18-dual-topology-test-harness-design.md
//
// Tests that want to run under both colocated and distributed topologies
// use forEachTopology. Each subtest receives a clusterFixture that answers
// ownership/layout questions identically regardless of wire path, so the
// test body doesn't know or care which mode it's running under.
// ─────────────────────────────────────────────────────────────────────────────

// clusterFixture is the topology-abstract handle test bodies receive.
// Implementations: colocatedFixture, distributedFixture.
type clusterFixture interface {
	// Coord returns the coord-role Process. Test code drives
	// orchestrator.BeginSplit/Merge/Migrate through Coord().orchestrator.
	Coord() *Process

	// HostIDs returns every host participating in the cluster, in the
	// deterministic order FixtureConfig.HostIDs declared.
	HostIDs() []string

	// CellOwner returns the host ID currently owning cellKey, or "" if
	// no host owns it. Always goes through HostForCellID so the answer
	// matches production code.
	CellOwner(cellKey string) string

	// HostOwnsCell is true iff the named host has an in-process *Cell
	// for this key on its own side.
	HostOwnsCell(hostID, cellKey string) bool

	// CellOn returns the in-process *Cell on the given host, or nil if
	// that host doesn't own it. Used by tests for execOnLoop and direct
	// ECS access.
	CellOn(hostID, cellKey string) *Cell

	// WaitForCellOwner blocks until cellKey is owned by hostID, or ctx
	// expires. Colocated returns immediately; distributed polls the
	// coord's hostRegistry until the CellReady roundtrip lands.
	WaitForCellOwner(ctx context.Context, cellKey, hostID string) error

	// WaitForCellReleased blocks until the named host no longer has an
	// in-process *Cell for cellKey, or ctx expires. Mirror of
	// WaitForCellOwner for the opposite transition — used after migrate
	// to observe the source host's async CellRelease completing. In
	// colocated mode this returns immediately (source teardown is
	// synchronous with req.Done); in distributed mode it polls the
	// host-role Process until the async release lands.
	WaitForCellReleased(ctx context.Context, cellKey, hostID string) error

	// AnyCell returns the in-process *Cell for cellKey from whichever host
	// currently owns it, without requiring the caller to know the host ID.
	// Equivalent to CellOn(CellOwner(cellKey), cellKey). Returns nil if no
	// host currently owns the cell.
	AnyCell(cellKey string) *Cell

	// StopHost requests a graceful shutdown of the named host. In
	// colocated mode this calls coord.drainHost(hostID) directly. In
	// distributed mode it calls Shutdown() on the host-role Process,
	// which sends GracefulLeave to the coord and waits for CellsDrained.
	// After StopHost returns, the host is no longer reachable; fx.HostIDs()
	// reflects the removal. Returns an error if the host is unknown.
	StopHost(ctx context.Context, hostID string) error
}

// FixtureConfig declares the cluster shape both topologies build against.
// Defaults (applied in normalize()): 2×2 grid, ["host-a"] single host,
// CellSize 1024. With a single host all cells land on host-a. Tests that
// need >1 host pass HostIDs explicitly; forEachTopology skips /colocated
// automatically for those — they run distributed-only.
type FixtureConfig struct {
	CellsX   uint32
	CellsY   uint32
	CellSize float32
	HostIDs  []string

	// Layout maps each cell key (MeshCellID form) to the host that
	// should own it at fixture creation. Leave nil to use the default
	// column-first round-robin over HostIDs.
	Layout map[string]string

	// WithGateway=true adds RoleGateway to the coord-role Process in
	// distributed mode. Colocated always has the gateway (it's part of
	// the "all" preset). Leave false unless the test needs an embedded
	// gateway (typically only s6 gateway + session-handoff tests).
	WithGateway bool

	// GatewayMode is forwarded to every host-role Process as
	// Config.GatewayMode. Defaults to "" (local-shortcut). Set to
	// "always-proxy" for tests that need to exercise the codec path
	// even for colocated destinations.
	GatewayMode string

	// ClusterClockSyncInterval is forwarded to the coord-role Process
	// as Config.ClusterClockSyncInterval. Zero falls back to the
	// engine default (10 s). Set to a short interval for tests that
	// need to observe the periodic CoordTimeSync broadcast loop.
	ClusterClockSyncInterval time.Duration

	// DynamicPartitioning is forwarded to the coord-role Process.
	// Nil means the partition monitor + partState stay disabled (the
	// engine default). Partition tests that exercise split/merge
	// cooldowns or the auto-monitor must set this explicitly.
	DynamicPartitioning *PartitionConfig
}

func (cfg *FixtureConfig) normalize() {
	if cfg.CellsX == 0 {
		cfg.CellsX = 2
	}
	if cfg.CellsY == 0 {
		cfg.CellsY = 2
	}
	if cfg.CellSize == 0 {
		cfg.CellSize = 1024
	}
	if len(cfg.HostIDs) == 0 {
		cfg.HostIDs = []string{"host-a"}
	}
	if cfg.Layout == nil {
		cfg.Layout = defaultRoundRobinLayout(cfg.CellsX, cfg.CellsY, cfg.HostIDs)
	}
}

// defaultRoundRobinLayout reproduces a column-first cell placement across
// the declared host IDs. Scanning order: for each row y, for each column
// x, assign cell (x,y) to hosts[i%N] where i is the visit index. Tests
// that hardcoded "host-a" keep passing.
func defaultRoundRobinLayout(cellsX, cellsY uint32, hostIDs []string) map[string]string {
	out := make(map[string]string, cellsX*cellsY)
	i := 0
	for y := uint32(0); y < cellsY; y++ {
		for x := uint32(0); x < cellsX; x++ {
			key := MeshCellID(CellID{X: int32(x), Y: int32(y)})
			out[key] = hostIDs[i%len(hostIDs)]
			i++
		}
	}
	return out
}

// topoBuilder constructs one fixture. Registered in the topologies table
// below so adding a new topology means one line here plus an impl.
type topoBuilder struct {
	name  string
	build func(t *testing.T, cfg FixtureConfig) clusterFixture
}

var topologies = []topoBuilder{
	{name: "colocated", build: newColocatedFixture},
	{name: "distributed", build: newDistributedFixture},
}

// forEachTopology runs body once per registered topology as a subtest,
// passing a fresh clusterFixture each time. Cleanup happens via t.Cleanup
// registered inside the builders.
func forEachTopology(t *testing.T, cfg FixtureConfig, body func(t *testing.T, fx clusterFixture)) {
	t.Helper()
	for _, topo := range topologies {
		topo := topo
		t.Run(topo.name, func(t *testing.T) {
			// Colocated supports exactly 1 host. Multi-host scenarios
			// (declared via cfg.HostIDs with >1 entry) run distributed-only.
			if topo.name == "colocated" && len(cfg.HostIDs) > 1 {
				t.Skip("colocated topology is single-host; multi-host runs via distributedFixture")
			}
			// Copy the config so one subtest can't mutate a map the next
			// subtest reads.
			copied := cfg
			if cfg.HostIDs != nil {
				copied.HostIDs = append([]string(nil), cfg.HostIDs...)
			}
			if cfg.Layout != nil {
				copied.Layout = make(map[string]string, len(cfg.Layout))
				for k, v := range cfg.Layout {
					copied.Layout[k] = v
				}
			}
			copied.normalize()
			fx := topo.build(t, copied)
			body(t, fx)
		})
	}
}

// sortedKeys returns map keys in sorted order — used by fixture methods
// that need deterministic iteration without leaking map-order nondeterminism
// into tests.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// waitForCellOwnerViaRegistry polls coord.HostForCellID every 25ms until
// it returns hostID or ctx expires. Reused by both fixtures; the only
// difference between them is whether they need to poll at all.
func waitForCellOwnerViaRegistry(ctx context.Context, coord *Process, cellKey, hostID string) error {
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if coord.HostForCellID(cellKey) == hostID {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waitForCellOwner: %s not owned by %s before deadline (current owner=%q)",
				cellKey, hostID, coord.HostForCellID(cellKey))
		case <-tick.C:
		}
	}
}

// colocatedFixture wraps a single Process running Roles={coordinator,
// host, gateway} with exactly one in-process host (no HostNetwork, no gRPC).
// Multi-host scenarios run via distributedFixture.
type colocatedFixture struct {
	coord *Process
	hosts []string
}

func newColocatedFixture(t *testing.T, cfg FixtureConfig) clusterFixture {
	t.Helper()
	cfg.normalize()
	coords.SetCellSize(cfg.CellSize)

	// Colocated = single process with RoleAll + exactly one host.
	// Multi-host-in-binary testing lives in distributedFixture.
	if len(cfg.HostIDs) > 1 {
		t.Fatalf("colocatedFixture: expected at most 1 host ID (got %d %v). Use the distributedFixture for multi-host scenarios; forEachTopology skips /colocated automatically when HostIDs > 1.", len(cfg.HostIDs), cfg.HostIDs)
	}
	hostID := "local"
	if len(cfg.HostIDs) >= 1 {
		hostID = cfg.HostIDs[0]
	}

	coord := New(Config{
		CellsX:              cfg.CellsX,
		CellsY:              cfg.CellsY,
		CellSize:            cfg.CellSize,
		HostID:              hostID,
		Headless:            true,
		InvariantMode:       InvariantPanic,
		StrictNetIDIndex:    true,
		ConnManager:         net.NewConnManager(),
		Logger:              logger.New(),
		DynamicPartitioning: cfg.DynamicPartitioning,
		LoginHandler: func(connID uint32, msgs [][]byte) (string, any, error) {
			return "", nil, ErrLoginPending
		},
		World: func(base *WorldBase) GameWorld { return base },
	})
	coord.Build()

	ctx, cancel := context.WithCancel(context.Background())
	for _, cell := range coord.Cells {
		go cell.Run(ctx)
	}
	// Start the coord's event router so gateway login events + loginTicker
	// processing run. The `all` preset always has RoleGateway so c.gateway
	// is non-nil; tests exercising session-handoff paths (e.g. s6_gateway)
	// depend on this goroutine draining ConnMgr.Events().
	if coord.gateway != nil {
		go coord.routeEvents(ctx)
	}
	// Let every cell drain its first admin-cmd pass before anything else
	// runs. Matches newMigrateTestCoord's 20ms sleep.
	time.Sleep(20 * time.Millisecond)

	t.Cleanup(func() {
		cancel()
		coord.Shutdown()
	})

	return &colocatedFixture{coord: coord, hosts: []string{hostID}}
}

func (f *colocatedFixture) Coord() *Process { return f.coord }
func (f *colocatedFixture) HostIDs() []string   { return f.hosts }

func (f *colocatedFixture) CellOwner(cellKey string) string {
	return f.coord.HostForCellID(cellKey)
}

func (f *colocatedFixture) HostOwnsCell(hostID, cellKey string) bool {
	return f.CellOn(hostID, cellKey) != nil
}

func (f *colocatedFixture) CellOn(hostID, cellKey string) *Cell {
	f.coord.mu.RLock()
	h, ok := f.coord.Hosts[hostID]
	f.coord.mu.RUnlock()
	if !ok || h == nil {
		return nil
	}
	return h.CellByID(cellKey)
}

func (f *colocatedFixture) AnyCell(cellKey string) *Cell {
	owner := f.CellOwner(cellKey)
	if owner == "" {
		return nil
	}
	return f.CellOn(owner, cellKey)
}

func (f *colocatedFixture) WaitForCellOwner(ctx context.Context, cellKey, hostID string) error {
	// Colocated placement is synchronous; no wait needed. Still poll in
	// case the caller passed a cellKey that isn't owned (so the error
	// message comes from the shared helper, not from a silent mismatch).
	return waitForCellOwnerViaRegistry(ctx, f.coord, cellKey, hostID)
}

func (f *colocatedFixture) StopHost(ctx context.Context, hostID string) error {
	// Colocated path: drive drainHost directly on the coord — same entry
	// point the production GracefulLeave handler uses. After drain
	// completes (no surviving hosts = no migration = immediate ack), the
	// hostRegistry entry is removed.
	if err := f.coord.drainHost(ctx, hostID); err != nil {
		return err
	}
	// Remove from the declared host list so HostIDs() reflects reality.
	filtered := f.hosts[:0]
	for _, h := range f.hosts {
		if h != hostID {
			filtered = append(filtered, h)
		}
	}
	f.hosts = filtered
	return nil
}

func (f *colocatedFixture) WaitForCellReleased(ctx context.Context, cellKey, hostID string) error {
	// Colocated teardown is synchronous with req.Done — the check is a
	// single read. Poll defensively anyway so broken callers get a proper
	// deadline-based error rather than a silent mismatch.
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if !f.HostOwnsCell(hostID, cellKey) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("colocatedFixture: cell %s still on host %s before deadline", cellKey, hostID)
		case <-tick.C:
		}
	}
}

