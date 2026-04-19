package universe

import (
	"fmt"
	"sync"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
)

// Host is a process-level container that owns N Cells. In a distributed
// mesh, each OS process runs one Host with one or more Cells. In the
// default colocated mode, one Host owns all cells.
//
// Host holds shared resources (logger, future gRPC endpoint, metrics
// registry) and provides cell lookup by CellID.
//
// Concurrency: Host.Cells is guarded by Host.mu. The cell-transfer executor
// mutates the map on an admin goroutine (Receive / Abort / migrate commit)
// while the HostNetwork data-plane goroutines concurrently walk it from
// routeInboundFrame. Callers must go through AddCell/RemoveCell/CellByID
// rather than touching the map directly.
type Host struct {
	// ID is a stable process-scoped identifier. Set via --host-id flag
	// or auto-generated UUID. Does not change across cell migrations.
	ID string

	// mu guards the Cells map. Held briefly on every reader + writer; no
	// nested locks.
	mu sync.RWMutex

	// Cells maps CellID to the Cell running on this host. Access via
	// AddCell/RemoveCell/CellByID/CellByCellID — direct map mutation is
	// a race hazard (see the S7-T10 graceful-shutdown test and the
	// routeInboundFrame read path).
	Cells map[CellID]*Cell

	// Log is the shared logger for all cells on this host.
	Log *logger.Logger

	// Network is the gRPC data plane for this host. Nil in single-host
	// colocated mode — hosts gracefully skip it when unset.
	Network *HostNetwork

	// Host-plane state. Populated during Build() from Process's
	// corresponding fields. Phase 2 migration makes these
	// authoritative; Phase 6 drops Process's copies.
	netIDAlloc   *NetIDAllocator
	systemDefs   []engine.SystemDef
	worldFactory func(base *WorldBase) GameWorld
	onInit       func(w *WorldBase)
	executor     *cellTransferExecutor
	vcm          *VirtualConnManager

	// Back-reference to the parent Process. Populated during Build()
	// / buildRemoteHost. Used by localHostOps to route operations through
	// the coord's renameCellOnNode / (future) host-op dispatchers.
	coord *Process
}

// NewHost creates a Host with the given ID and no cells.
func NewHost(id string) *Host {
	return &Host{
		ID:    id,
		Cells: make(map[CellID]*Cell),
	}
}

// AddCell registers a cell on this host.
func (h *Host) AddCell(cellID CellID, cell *Cell) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Cells[cellID] = cell
}

// RemoveCell unregisters a cell from this host.
func (h *Host) RemoveCell(cellID CellID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.Cells, cellID)
}

// IsLocal reports whether the given hostID matches this host.
func (h *Host) IsLocal(hostID string) bool {
	return h.ID == hostID
}

// CellCount returns the number of cells on this host.
func (h *Host) CellCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.Cells)
}

// CellByID returns the Cell on this host whose string ID matches, or
// nil if no such cell exists. Used by HostNetwork.routeInboundFrame
// to dispatch inbound frames.
func (h *Host) CellByID(cellIDString string) *Cell {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.Cells {
		if c.ID == cellIDString {
			return c
		}
	}
	return nil
}

// CellByCellID returns the cell on this host keyed by its CellID struct
// (depth-aware), or nil if no such cell exists. Thread-safe counterpart
// to direct h.Cells[id] map reads.
func (h *Host) CellByCellID(id CellID) *Cell {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.Cells[id]
}

// SendLossy dispatches a MeshFrame via the host's HostNetwork using the
// lossy (fire-and-forget) path. Returns false if the host has no Network
// or the send was dropped.
func (h *Host) SendLossy(destHostID string, frame *meshpb.MeshFrame) bool {
	if h.Network == nil {
		return false
	}
	return h.Network.SendLossy(destHostID, frame)
}

// SendReliable dispatches a MeshFrame via the host's HostNetwork using the
// reliable (blocking with deadline) path. Returns an error if the host has
// no Network or the send fails.
func (h *Host) SendReliable(destHostID string, frame *meshpb.MeshFrame) error {
	if h.Network == nil {
		return errNoHostNetwork
	}
	return h.Network.SendReliable(destHostID, frame)
}

// NetworkAddr returns the gRPC listen address of this host's HostNetwork,
// or "(local)" if the host has no Network (single-host colocated mode).
func (h *Host) NetworkAddr() string {
	if h.Network == nil {
		return "(local)"
	}
	return h.Network.Addr()
}

var errNoHostNetwork = fmt.Errorf("no host network configured")

// SnapshotCellIDs returns a snapshot of every cell ID currently owned by
// this host. Used by tests and the drain path that need to iterate without
// holding h.mu for the full walk.
func (h *Host) SnapshotCellIDs() []CellID {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]CellID, 0, len(h.Cells))
	for id := range h.Cells {
		out = append(out, id)
	}
	return out
}

// EachCell iterates every local cell currently running on this host.
// Snapshots the cell set under h.mu, then yields without holding the
// lock — safe to call Cell/OwnsCell/CellByID/mutators from inside the
// yield body. Matches the snapshot-then-yield pattern used by
// ControlPlane.AllOwnedCells.
func (h *Host) EachCell(yield func(*Cell) bool) {
	h.mu.RLock()
	snapshot := make([]*Cell, 0, len(h.Cells))
	for _, c := range h.Cells {
		snapshot = append(snapshot, c)
	}
	h.mu.RUnlock()
	for _, c := range snapshot {
		if !yield(c) {
			return
		}
	}
}

// Cell returns the local *Cell for cellKey, or nil if this host doesn't
// own it. Acquires h.mu.RLock for the lookup. Alias for the existing
// CellByID with the shorter name the post-refactor code uses.
func (h *Host) Cell(cellKey string) *Cell {
	return h.CellByID(cellKey)
}

// OwnsCell is a bool convenience for Cell(cellKey) != nil.
func (h *Host) OwnsCell(cellKey string) bool {
	return h.Cell(cellKey) != nil
}
