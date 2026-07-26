package universe

import (
	"context"
	"fmt"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
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
	ReleaseCell(ctx context.Context, cellKey MeshCellID) error

	// StartCell creates a cell on the target host, blocking until the
	// game loop is running and the host has acked CellReady. Error
	// if the cell already exists or ctx deadline fires.
	StartCell(ctx context.Context, cellID CellID) error

	// RenameCell rekeys a cell on the target host from `from` to `to`,
	// blocking until the rename is visible on the target's game loop.
	// Used by merge commit to rename the survivor sibling to the
	// parent ID. Error if the cell doesn't exist or ctx deadline fires.
	RenameCell(ctx context.Context, from, to MeshCellID) error
}

// localHostOps is the in-process impl: method calls go directly to the
// local *Host. Synchronous; blocks trivially because the operations
// themselves complete before returning.
type localHostOps struct {
	host *Host
}

func (l *localHostOps) ReleaseCell(ctx context.Context, cellKey MeshCellID) error {
	cell := l.host.CellByID(cellKey)
	if cell == nil {
		return fmt.Errorf("host %s: ReleaseCell: unknown cell %s", l.host.ID, cellKey)
	}
	l.host.RemoveCell(cell.CellID())
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

func (l *localHostOps) RenameCell(ctx context.Context, from, to MeshCellID) error {
	// Dispatch through the parent coordinator's renameCellOnNode so the
	// locking + RunOnLoop discipline lives in one place.
	if l.host.coord == nil {
		return fmt.Errorf("localHostOps.RenameCell: host has no parent coord")
	}
	return l.host.coord.renameCellOnNode(from, to)
}

// remoteHostOps dispatches MeshControl messages to a remote host and
// blocks on HostOpAck. The Control field is the coord-role ControlPlane
// hosting the pending-op registry.
type remoteHostOps struct {
	control *ControlPlane
	hostID  string
}

func (r *remoteHostOps) ReleaseCell(ctx context.Context, cellKey MeshCellID) error {
	if r.control.controlServer == nil {
		return fmt.Errorf("remoteHostOps: no control server (cannot dispatch to %s)", r.hostID)
	}
	reqID := r.control.allocHostOpID()
	ch := r.control.registerPendingOp(reqID)

	msg := &meshpb.CoordMessage{
		CoordEpoch: r.control.coordEpoch(),
		Msg: &meshpb.CoordMessage_CellRelease{
			CellRelease: &meshpb.CellRelease{
				CellId: string(cellKey),
				ReqId:  reqID,
			},
		},
	}
	if err := r.control.controlServer.sendCoordMessageToHost(r.hostID, msg); err != nil {
		r.control.cancelPendingOp(reqID, "dispatch failed")
		return fmt.Errorf("remoteHostOps: dispatch to %s failed: %w", r.hostID, err)
	}

	select {
	case result := <-ch:
		if !result.ok {
			return fmt.Errorf("remoteHostOps: %s ReleaseCell %s failed: %s", r.hostID, cellKey, result.error)
		}
		return nil
	case <-ctx.Done():
		r.control.cancelPendingOp(reqID, "ctx expired")
		return fmt.Errorf("remoteHostOps: %s ReleaseCell %s: ctx expired: %w", r.hostID, cellKey, ctx.Err())
	}
}

func (r *remoteHostOps) StartCell(ctx context.Context, cellID CellID) error {
	return fmt.Errorf("remoteHostOps.StartCell: not yet implemented (Phase 3.3)")
}

func (r *remoteHostOps) RenameCell(ctx context.Context, from, to MeshCellID) error {
	if r.control.controlServer == nil {
		return fmt.Errorf("remoteHostOps: no control server (cannot dispatch to %s)", r.hostID)
	}
	reqID := r.control.allocHostOpID()
	ch := r.control.registerPendingOp(reqID)

	msg := &meshpb.CoordMessage{
		CoordEpoch: r.control.coordEpoch(),
		Msg: &meshpb.CoordMessage_CellRename{
			CellRename: &meshpb.CellRename{
				FromCellId: string(from),
				ToCellId:   string(to),
				ReqId:      reqID,
			},
		},
	}
	if err := r.control.controlServer.sendCoordMessageToHost(r.hostID, msg); err != nil {
		r.control.cancelPendingOp(reqID, "dispatch failed")
		return fmt.Errorf("remoteHostOps: dispatch CellRename to %s: %w", r.hostID, err)
	}

	select {
	case result := <-ch:
		if !result.ok {
			return fmt.Errorf("remoteHostOps: %s CellRename %s->%s failed: %s", r.hostID, from, to, result.error)
		}
		return nil
	case <-ctx.Done():
		r.control.cancelPendingOp(reqID, "ctx expired")
		return fmt.Errorf("remoteHostOps: %s CellRename %s->%s: ctx expired: %w", r.hostID, from, to, ctx.Err())
	}
}
