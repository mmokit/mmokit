package universe

import (
	"context"
	"fmt"
)

// hostOps is the topology-abstract operation API used by commit paths.
// Local impl wraps a *Host (direct method calls, synchronous). Remote
// impl dispatches MeshControl messages and blocks on HostOpAck. Both
// honor the caller's ctx deadline.
type hostOps interface {
	// ReleaseCell shuts down the cell on the target host, blocking
	// until teardown is observable (game loop stopped, netID range
	// returned, Host.cells entry removed). Error on unknown cellKey or
	// ctx deadline.
	ReleaseCell(ctx context.Context, cellKey string) error

	// StartCell creates a cell on the target host, blocking until the
	// game loop is running and the host has acked CellReady. Error
	// if the cell already exists or ctx deadline fires.
	StartCell(ctx context.Context, cellID CellID) error

	// RenameCell rekeys a cell on the target host from `from` to `to`,
	// blocking until the rename is visible on the target's game loop.
	// Used by merge commit to rename the survivor sibling to the
	// parent ID. Error if the cell doesn't exist or ctx deadline fires.
	RenameCell(ctx context.Context, from, to string) error
}

// localHostOps is the in-process impl: method calls go directly to the
// local *Host. Synchronous; blocks trivially because the operations
// themselves complete before returning.
type localHostOps struct {
	host *Host
}

func (l *localHostOps) ReleaseCell(ctx context.Context, cellKey string) error {
	cell := l.host.CellByID(cellKey)
	if cell == nil {
		return fmt.Errorf("host %s: ReleaseCell: unknown cell %s", l.host.ID, cellKey)
	}
	l.host.RemoveCell(cell.Cell)
	cell.Shutdown()
	// Caller is responsible for releasing the NetID range (belongs to
	// the coord's netIDAlloc, not the host).
	return nil
}

func (l *localHostOps) StartCell(ctx context.Context, cellID CellID) error {
	// Local StartCell is wired in Phase 3.3 once the coord's createNode
	// path is refactored. Phase 3.1 stubs this so the interface
	// implementation compiles.
	return fmt.Errorf("localHostOps.StartCell: not yet implemented (Phase 3.3)")
}

func (l *localHostOps) RenameCell(ctx context.Context, from, to string) error {
	// Local RenameCell lands in Phase 4 alongside the remote impl.
	return fmt.Errorf("localHostOps.RenameCell: not yet implemented (Phase 4)")
}
