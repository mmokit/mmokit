package universe

import (
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// TestSplit_SourceDeactivatesTransferredPlayerViewer is the regression guard
// for the dual-viewer race that caused ~1s of visual jitter after a split.
//
// Mechanism (pre-fix): the destination child cell starts ticking and sending
// WorldDelta frames to the migrated player as soon as it's created, but the
// source/parent cell kept the player StateActive — and thus an active viewer
// (NewPlayerViewerSource filters StateActive) — until its deferred teardown
// ~16 ticks later at commit. For that window BOTH cells sent the player a
// WorldDelta every tick, so every shared entity arrived twice per tick with
// conflicting same-producedAtMs positions, making the interpolation ring
// jitter (~1900 conflicting samples observed in a 2s window).
//
// Fix: when the source serializes a player for transfer, transition it to
// StateTransferring on the source so the source stops listing it as a viewer.
// The destination registers it StateActive. Net: the player is an active
// viewer in EXACTLY ONE cell at a time — no doubled frames.
func TestSplit_SourceDeactivatesTransferredPlayerViewer(t *testing.T) {
	coord, host, srcCell := newExecutorTestCoord(t)
	src := srcCell.CellID()
	half := src.Size(coords.CellSize) / 2

	const connID uint32 = 4242
	const netID uint32 = 7777

	// Register an Active player session on the source with a PlayerConn-bearing
	// entity in the TR quadrant (index 3).
	execOnLoop(t, srcCell, func() {
		srcCell.Engine.Players.RegisterSessionTransfer(connID, "p1", "active", nil)
		e := spawnTestEntity(srcCell, netID, half*1.5, half*1.5)
		ecs.NewMap1[component.PlayerConn](srcCell.Engine.ECS).Add(e, &component.PlayerConn{ConnID: connID})
		if sess := srcCell.Engine.Players.ByConnID(connID); sess != nil {
			sess.Entity = e
		}
	})

	// Precondition: player is StateActive (an active viewer) on the source.
	execOnLoop(t, srcCell, func() {
		sess := srcCell.Engine.Players.ByConnID(connID)
		if sess == nil || sess.State != engine.StateActive {
			t.Fatalf("precondition: player should be StateActive on source, got %v", sess)
		}
	})

	// Local dest host → Execute's shipToDestination takes the Receive fast-path.
	destHost := NewHost("dest-host")
	destHost.Log = coord.Log
	coord.Hosts[destHost.ID] = destHost
	coord.hostExecutors[destHost.ID] = newCellTransferExecutor(coord, destHost)
	coord.orchestrator.setDispatcher(&fakeDispatcher{})

	children := src.Children()
	const quadrant = 3
	req := &CellTransferRequest{
		ID:            100,
		Kind:          CellTransferSplit,
		SrcCell:       src,
		ExpectedReady: 1,
		receivedOK:    make(map[string]struct{}),
		Deadline:      time.Now().Add(5 * time.Second),
		Done:          make(chan struct{}),
		mutation:      topologyMutation{add: map[MeshCellID]string{}},
	}
	coord.orchestrator.mu.Lock()
	coord.orchestrator.inflight[req.ID] = req
	coord.orchestrator.mu.Unlock()

	exec := coord.hostExecutors[host.ID]
	cmd := cellTransferCommand{
		RequestID:  req.ID,
		Kind:       CellTransferSplit,
		SrcCellID:  srcCell.MeshID(),
		DestCellID: children[quadrant].MeshID(),
		SrcHostID:  host.ID,
		DestHostID: destHost.ID,
		Quadrant:   quadrant,
	}
	if err := exec.Execute(cmd); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// After transfer: source must NO LONGER view the player (it's been
	// transitioned out of StateActive), so the source stops sending it
	// WorldDelta. The session object lingers on the source until the parent's
	// commit teardown — we only require that it's not StateActive.
	execOnLoop(t, srcCell, func() {
		sess := srcCell.Engine.Players.ByConnID(connID)
		if sess != nil && sess.State == engine.StateActive {
			t.Errorf("player still StateActive on source after transfer — source keeps sending WorldDelta (dual-viewer race not fixed)")
		}
	})

	// And the destination must now be its active viewer.
	destCell := destHost.CellByID(cmd.DestCellID)
	if destCell == nil {
		t.Fatalf("dest cell %s not created", cmd.DestCellID)
	}
	execOnLoop(t, destCell, func() {
		sess := destCell.Engine.Players.ByConnID(connID)
		if sess == nil || sess.State != engine.StateActive {
			t.Errorf("player should be StateActive (an active viewer) on dest after transfer, got %v", sess)
		}
	})
}
