package universe

import (
	stdnet "net"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/logger"
)

// ControlPlane holds the state belonging to the RoleCoordinator role.
// Always present on every Process. Fields migrate here from Process
// across Phases 1-5 of the role-separation refactor.
type ControlPlane struct {
	log *logger.Logger

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

	// Phase 2 migration bridges: ControlPlane reads Process's raw
	// maps through these pointers while the fields live on Process.
	// Removed in Phase 6 after the raw maps are deleted.
	cellToHostMapRef *map[string]string
	coordMuRef       *sync.RWMutex

	coordEpochRef *uint64

	// Bridge to the process's local Hosts map. Nil on pure-coord
	// deployments with no local hosts. Set during Build()/buildRemoteHost
	// to point at Process.Hosts. hostProxy walks this (under coordMu)
	// to decide whether a target hostID is local. Works uniformly for
	// single-host "all" mode and pure remote-host workers.
	localHostsRef *map[string]*Host

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
// coord's cellToHostMap (populated by Build() for local hosts and by
// applyPeerList on remote hosts).
func (c *ControlPlane) OwnerOf(cellKey string) (string, bool) {
	if c.hostRegistry != nil {
		if h := c.hostRegistry.HostForCell(cellKey); h != "" {
			return h, true
		}
	}
	// cellToHostMap fallback — read with the parent coord's mu.
	// During Phase 2 migration this still lives on Process, so we
	// pass through to the coord via a field the coord sets at init.
	if c.cellToHostMapRef != nil {
		c.coordMuRef.RLock()
		h, ok := (*c.cellToHostMapRef)[cellKey]
		c.coordMuRef.RUnlock()
		return h, ok
	}
	return "", false
}

// AllOwnedCells iterates every (cellKey, hostID) pair currently known.
// Union of hostRegistry ownership and cellToHostMap entries. Distinct
// from Topology.AllCells (which returns all cells in the grid regardless
// of ownership).
func (c *ControlPlane) AllOwnedCells(yield func(cellKey, hostID string) bool) {
	seen := make(map[string]struct{})
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			for cellID := range h.OwnedCells {
				if _, dup := seen[cellID]; dup {
					continue
				}
				seen[cellID] = struct{}{}
				if !yield(cellID, h.ID) {
					return
				}
			}
		}
	}
	if c.cellToHostMapRef != nil {
		// Snapshot under the lock, then yield without holding it. Matches
		// snapshotCellOwnership in coordinator.go — avoids stalling writers
		// if a yield body is slow or tries to re-enter the coord lock.
		c.coordMuRef.RLock()
		snapshot := make([][2]string, 0, len(*c.cellToHostMapRef))
		for k, v := range *c.cellToHostMapRef {
			if _, dup := seen[k]; dup {
				continue
			}
			snapshot = append(snapshot, [2]string{k, v})
		}
		c.coordMuRef.RUnlock()
		for _, kv := range snapshot {
			if !yield(kv[0], kv[1]) {
				return
			}
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

// coordEpoch returns the parent coordinator's epoch. Temporary bridge
// until Phase 7 moves coordEpoch onto Process directly.
func (c *ControlPlane) coordEpoch() uint64 {
	if c.coordEpochRef != nil {
		return *c.coordEpochRef
	}
	return 0
}

// hostProxy returns a hostOps implementation for the named host. If the
// target hostID matches any Host in this process's local Hosts map,
// direct method calls are used (localHostOps). Otherwise MeshControl
// routing is used (remoteHostOps).
func (c *ControlPlane) hostProxy(hostID string) hostOps {
	if c.localHostsRef != nil && c.coordMuRef != nil {
		c.coordMuRef.RLock()
		h, ok := (*c.localHostsRef)[hostID]
		c.coordMuRef.RUnlock()
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
// Safe to call from any goroutine — serializes through coordMuRef so
// concurrent callbacks and Topology readers in commit paths (which
// hold c.mu) don't race.
func (c *ControlPlane) rebuildTopologyForCell(cellKey string) {
	cid, err := ParseCellID(cellKey)
	if err != nil {
		return
	}
	if c.coordMuRef == nil {
		return
	}
	c.coordMuRef.Lock()
	defer c.coordMuRef.Unlock()
	if c.Topology.Neighbors == nil {
		c.Topology.Neighbors = make(map[CellID][]CellID)
	}
	if _, ok := c.Topology.Neighbors[cid]; !ok {
		// First time we've seen this cell — insert an empty slot so
		// RebuildNeighborsFor treats it as present.
		c.Topology.Neighbors[cid] = nil
	}
	c.Topology.RebuildNeighborsFor([]CellID{cid}, coords.CellSize)
}
