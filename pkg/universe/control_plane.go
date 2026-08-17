package universe

import (
	stdnet "net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"

	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/logger"
)

// ControlPlane holds the state belonging to the RoleCoordinator role.
// Always present on every Process. Fields migrate here from Process
// across Phases 1-8 of the role-separation refactor.
type ControlPlane struct {
	log *logger.Logger

	// process is the back-reference to the containing Process. Set in
	// New() right after ControlPlane is constructed. Used to read
	// Process-owned shared state (coordEpoch, Hosts map) without a
	// per-field bridge ref.
	process *Process

	// mu guards cellToHostMap and Topology.Neighbors. Separate from
	// Process.mu — cell-ownership + topology are ControlPlane concerns.
	//
	// Lock ordering: when both Process.mu and Control.mu must be held,
	// always acquire Process.mu first, then Control.mu. Never take
	// Control.mu before Process.mu.
	mu sync.RWMutex

	// cellToHostMap maps each cell's mesh-form ID (e.g. "cell_0_0") to its
	// current host. Authoritative for this Process's view of cell
	// ownership. Populated by Build() for local cells, applyPeerList for
	// remote cells, and commit paths for split/merge/migrate deltas.
	cellToHostMap map[MeshCellID]string

	hostRegistry    *HostRegistry
	gatewayRegistry *GatewayRegistry

	controlServer     *meshControlServer
	controlGrpcServer *grpc.Server
	controlListener   stdnet.Listener

	controlClient *meshControlClient

	assignmentEngine *assignmentEngine

	// Pending host-op acks. Keyed by req_id. Populated by remote
	// hostOps.ReleaseCell / StartCell / RenameCell; drained by the
	// meshControlServer's handleHostControl when HostOpAck arrives.
	pendingOps   sync.Map      // map[uint64]chan hostOpResult
	nextHostOpID atomic.Uint64 // monotonic ID allocator (1-based; 0 = no-ack sentinel)

	// Topology tracks cell-neighbor adjacency. Rebuilt incrementally on
	// ownership changes (hostRegistry.AssignCell / ReleaseCell) and
	// restructuring events (split, merge).
	Topology Topology
}

func newControlPlane(log *logger.Logger) *ControlPlane {
	return &ControlPlane{log: log}
}

// OwnerOf returns the host currently owning cellKey, or ("", false) if
// no host owns it. Unified view: consults hostRegistry first (the
// authoritative source in distributed deployments), falls back to the
// ControlPlane's own cellToHostMap (populated by Build() for local hosts
// and by applyPeerList on remote hosts).
func (c *ControlPlane) OwnerOf(cellKey MeshCellID) (string, bool) {
	if c.hostRegistry != nil {
		if h := c.hostRegistry.HostForCell(cellKey); h != "" {
			return h, true
		}
	}
	c.mu.RLock()
	h, ok := c.cellToHostMap[cellKey]
	c.mu.RUnlock()
	return h, ok
}

// AllOwnedCells iterates every (cellKey, hostID) pair currently known.
// Union of hostRegistry ownership and cellToHostMap entries. Distinct
// from Topology.AllCells (which returns all cells in the grid regardless
// of ownership).
//
// cellKey is yielded as a plain string (mesh form) to keep call-sites that
// feed it into ParseCellID or wire fields ergonomic; callers that need
// the typed form cast at use.
func (c *ControlPlane) AllOwnedCells(yield func(cellKey, hostID string) bool) {
	seen := make(map[MeshCellID]struct{})
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			for cellID := range h.OwnedCells {
				if _, dup := seen[cellID]; dup {
					continue
				}
				seen[cellID] = struct{}{}
				if !yield(string(cellID), h.ID) {
					return
				}
			}
		}
	}
	// Snapshot under the lock, then yield without holding it. Avoids
	// stalling writers if a yield body is slow or tries to re-enter the
	// lock.
	c.mu.RLock()
	snapshot := make([][2]string, 0, len(c.cellToHostMap))
	for k, v := range c.cellToHostMap {
		if _, dup := seen[k]; dup {
			continue
		}
		snapshot = append(snapshot, [2]string{string(k), v})
	}
	c.mu.RUnlock()
	for _, kv := range snapshot {
		if !yield(kv[0], kv[1]) {
			return
		}
	}
}

// CellsOwnedBy iterates cell keys owned by the named host. Empty if
// hostID is unknown.
func (c *ControlPlane) CellsOwnedBy(hostID string, yield func(cellKey string) bool) {
	c.AllOwnedCells(func(cellKey, owner string) bool {
		if owner != hostID {
			return true
		}
		return yield(cellKey)
	})
}

type hostOpResult struct {
	ok    bool
	error string
}

func (c *ControlPlane) allocHostOpID() uint64 {
	return c.nextHostOpID.Add(1)
}

func (c *ControlPlane) registerPendingOp(id uint64) chan hostOpResult {
	ch := make(chan hostOpResult, 1)
	c.pendingOps.Store(id, ch)
	return ch
}

func (c *ControlPlane) completePendingOp(id uint64, result hostOpResult) {
	if v, ok := c.pendingOps.LoadAndDelete(id); ok {
		ch := v.(chan hostOpResult)
		ch <- result
	}
}

// cancelPendingOp unblocks any goroutine waiting on the registered
// channel with an error result, then deletes the entry. Safe to call
// multiple times — only the first call that wins LoadAndDelete observes
// the channel.
func (c *ControlPlane) cancelPendingOp(id uint64, reason string) {
	if v, ok := c.pendingOps.LoadAndDelete(id); ok {
		ch := v.(chan hostOpResult)
		ch <- hostOpResult{ok: false, error: reason}
	}
}

// coordEpoch returns the parent coordinator's epoch. coordEpoch lives on
// Process (cross-plane shared state read by the Gateway for MeshData
// frames). Access via the process back-reference set in New().
func (c *ControlPlane) coordEpoch() uint64 {
	if c.process != nil {
		return c.process.coordEpoch
	}
	return 0
}

// hostProxy returns a hostOps implementation for the named host. If the
// target hostID matches any Host in this process's local Hosts map,
// direct method calls are used (localHostOps). Otherwise MeshControl
// routing is used (remoteHostOps).
//
// Uses c.process.Hosts under c.process.mu (Process.mu → Control.mu ordering
// is not triggered here since we only hold Process.mu briefly for the lookup).
func (c *ControlPlane) hostProxy(hostID string) hostOps {
	if c.process != nil {
		c.process.mu.RLock()
		h, ok := c.process.Hosts[hostID]
		c.process.mu.RUnlock()
		if ok && h != nil {
			return &localHostOps{host: h}
		}
	}
	return &remoteHostOps{control: c, hostID: hostID}
}

// rebuildTopologyForCell recomputes neighbor adjacency for cellKey
// after its ownership changes. Uses the existing
// Topology.RebuildNeighborsFor helper — scoped to the affected cell
// plus its former neighbors. Lazily initializes the Neighbors map on
// first call (pure-coord processes skip the initial ComputeTopology
// at Build and rely on this callback to populate topology as cells
// are assigned).
//
// Ownership-gated: if cellKey has no current owner (e.g. a release
// from a split/merge commit), the cell is removed from the Neighbors
// map instead of being (re-)inserted as a placeholder. Without this
// gate, a Release firing the callback after UpdateAfterSplit already
// deleted the parent would silently resurrect a dangling parent entry
// whose only "neighbors" relationship points at stale siblings —
// exactly what invTopologyNeighborsOwned was designed to catch.
//
// Safe to call from any goroutine — serializes through c.mu so
// concurrent callbacks and Topology readers in commit paths don't race.
func (c *ControlPlane) rebuildTopologyForCell(cellKey MeshCellID) {
	cid, err := ParseCellID(string(cellKey))
	if err != nil {
		return
	}
	// Snapshot ownership outside Control.mu since OwnerOf acquires
	// that same lock and we're about to take it for the topology
	// mutation below.
	_, owned := c.OwnerOf(cellKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Topology.Neighbors == nil {
		c.Topology.Neighbors = make(map[CellID][]CellID)
	}
	if !owned {
		// Release path: drop the cell entirely so RebuildNeighborsFor's
		// frontier pass skips it and the invariant stays satisfied.
		delete(c.Topology.Neighbors, cid)
		c.Topology.RebuildNeighborsFor([]CellID{cid}, c.baseCellSize())
		return
	}
	if _, ok := c.Topology.Neighbors[cid]; !ok {
		// First time we've seen this cell — insert an empty slot so
		// RebuildNeighborsFor treats it as present.
		c.Topology.Neighbors[cid] = nil
	}
	c.Topology.RebuildNeighborsFor([]CellID{cid}, c.baseCellSize())
}

// baseCellSize returns the containing process's cell geometry.
//
// Nil-guarded because ControlPlane is constructed as a bare struct literal in
// tests (process left nil) while production always sets the back-reference.
// Falling back to the package default keeps those fixtures working without
// giving every one of them a Process it does not otherwise need.
func (c *ControlPlane) baseCellSize() float32 {
	if c.process == nil {
		return coords.DefaultCellSize
	}
	return c.process.CellSize()
}
