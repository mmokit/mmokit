package universe

import "github.com/zenion/mmoserver/pkg/logger"

// Host is a process-level container that owns N Cells. In a distributed
// mesh, each OS process runs one Host with one or more Cells. In the
// default colocated mode, one Host owns all cells.
//
// Host holds shared resources (logger, future gRPC endpoint, metrics
// registry) and provides cell lookup by CellID.
type Host struct {
	// ID is a stable process-scoped identifier. Set via --host-id flag
	// or auto-generated UUID. Does not change across cell migrations.
	ID string

	// Cells maps CellID to the Cell running on this host.
	Cells map[CellID]*Cell

	// Log is the shared logger for all cells on this host.
	Log *logger.Logger
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
	h.Cells[cellID] = cell
}

// RemoveCell unregisters a cell from this host.
func (h *Host) RemoveCell(cellID CellID) {
	delete(h.Cells, cellID)
}

// IsLocal reports whether the given hostID matches this host.
func (h *Host) IsLocal(hostID string) bool {
	return h.ID == hostID
}

// CellCount returns the number of cells on this host.
func (h *Host) CellCount() int {
	return len(h.Cells)
}
