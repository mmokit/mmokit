package universe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/mmokit/mmokit/gen/go/meshpb"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/logger"
)

// executorAdminTimeout bounds how long the executor will wait for a
// serialize / populate closure to run on a cell's game loop. 5s matches
// the console command handler's engine.RunOnLoop budget.
const executorAdminTimeout = 5 * time.Second

// ═══════════════════════════════════════════════════════════════════════════
// S7 T4 — cellTransferExecutor
//
// The executor is the host-side counterpart to the coordinator's
// cellTransferOrchestrator. It owns the "ship entities, create target cell,
// report ready" flow for SPLIT / MERGE / MIGRATE. One instance per local
// Host; wired up during Build() (see Process.attachHostExecutors).
//
// Orchestrator → Dispatcher → Executor.Execute(cmd)   (source host)
//       ─ entities serialized on source cell's game loop ─
//       ─ CellTransfer MeshFrame shipped to dest host ─
//       ↳  Executor.Receive(proto)                      (target host)
//          ─ createNode + populate on target host game loop ─
//          ─ CellTransferReady reported to coord ─
//
// Rollback path:
//   Orchestrator → Dispatcher.DispatchAbort → Executor.Abort(proto)
//                                              ─ tear down partial cell ─
//
// Wire framing for CellTransfer.entities / .sessions is a length-prefixed
// concatenation of opaque records:
//
//    bytes    description
//    ----     -----------
//    0:4      uint32 count        (big-endian)
//    4:...    per-record: 4-byte big-endian length + raw bytes
//
// Entity records are TransferFrame bytes produced by Stage.SerializeEntity.
// Session records are JSON-encoded SessionTransfer structs (stable, schema-free
// encoding — the SessionTransfer.Data field already crosses process boundaries
// as opaque game-specific bytes elsewhere in the codebase).
// ═══════════════════════════════════════════════════════════════════════════

// cellTransferExecutor carries out CellTransfer commands on behalf of the
// orchestrator. Attached to a single Host.
type cellTransferExecutor struct {
	coord *Process
	host  *Host
	log   *logger.Logger

	mu sync.Mutex
	// pending retains every newly-created SPLIT/MIGRATE destination until
	// the coordinator sends an explicit Commit or Abort. A split may place
	// multiple children for one request on the same host, so the inner map is
	// keyed by destination cell ID.
	pending map[uint64]map[MeshCellID]*pendingReceive
	// mergeSources tracks donor cells whose handoff_driver has been frozen
	// by Execute for a CELL_TRANSFER_MERGE request. One host may own multiple
	// donor cells for the same request, so the inner map is keyed by cell ID.
	// Abort clears every donor flag; success tears all donors down.
	mergeSources map[uint64]map[MeshCellID]*Cell
	// mergeSurvivors tracks survivor cells whose handoff_driver has been
	// frozen by Receive for a CELL_TRANSFER_MERGE request. Keyed by request
	// ID. Same shape as mergeSources but for the destination side: the
	// survivor cell stays alive across the merge commit, so the flag must
	// be explicitly cleared by renameCellOnNode (success path) or Abort
	// (rollback path) — without that, the survivor would stay frozen
	// forever and stop emitting handoffs.
	mergeSurvivors map[uint64]*Cell
	// mergeNativeLive snapshots the netIDs that were authoritative on the
	// survivor before the first donor for a request was materialized. Those
	// entities always win merge dedup. Entries not in this set came from an
	// earlier donor and may be replaced by a serial-newer donor copy.
	mergeNativeLive map[uint64]map[uint32]struct{}
	// mergeImportedLive and mergeImportedSessions retain the exact objects
	// materialized from donors. A failed three-donor merge must remove these
	// from the pre-existing survivor without disturbing native or concurrently
	// handed-off authority.
	mergeImportedLive     map[uint64]map[uint32]ecs.Entity
	mergeImportedSessions map[uint64]map[engine.SessionID]mergeImportedSession

	// transferSrcCells tracks every source cell whose connected viewers were
	// replication-suspended by Execute, keyed by request and cell ID, for ALL
	// transfer kinds. Abort resumes every viewer on generation N+2 if the
	// transfer rolls back and the source cells survive.
	transferSrcCells map[uint64]map[MeshCellID]*Cell
}

// pendingReceive tracks a cell that was just created on this host in response
// to a RECEIVE but has not yet been committed by the orchestrator. If the
// orchestrator rolls back (Abort), we need to tear the cell down.
type pendingReceive struct {
	requestID uint64
	cellID    CellID
	cellKey   MeshCellID
	cell      *Cell
}

type mergeImportedSession struct {
	session *engine.PlayerSession
	vcmKey  SessionKey
	dropVCM bool
}

// newCellTransferExecutor builds an executor for the given host.
func newCellTransferExecutor(coord *Process, host *Host) *cellTransferExecutor {
	return &cellTransferExecutor{
		coord:                 coord,
		host:                  host,
		log:                   coord.Log,
		pending:               make(map[uint64]map[MeshCellID]*pendingReceive),
		mergeSources:          make(map[uint64]map[MeshCellID]*Cell),
		mergeSurvivors:        make(map[uint64]*Cell),
		mergeNativeLive:       make(map[uint64]map[uint32]struct{}),
		mergeImportedLive:     make(map[uint64]map[uint32]ecs.Entity),
		mergeImportedSessions: make(map[uint64]map[engine.SessionID]mergeImportedSession),
		transferSrcCells:      make(map[uint64]map[MeshCellID]*Cell),
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Execute — source-host entry point for SPLIT / MERGE / MIGRATE
// ───────────────────────────────────────────────────────────────────────────

// Execute is invoked on the SOURCE host when the coordinator's orchestrator
// has decided this host should ship a cell's state somewhere. Runs the
// per-kind serialize step on the source cell's game loop, then dispatches
// the populated CellTransfer proto to the destination host.
//
// For SPLIT with 4 children on the same source host, the caller (dispatcher)
// issues 4 separate Execute calls, one per quadrant. Each call only
// serializes entities in that quadrant.
func (e *cellTransferExecutor) Execute(cmd cellTransferCommand) error {
	srcCell := e.host.CellByID(cmd.SrcCellID)
	if srcCell == nil {
		return fmt.Errorf("executor: source cell %q not on host %s", cmd.SrcCellID, e.host.ID)
	}

	// Serialize the payload on the source cell's game loop so ECS reads
	// are race-free. RunOnLoop detects on-loop reentrance and runs inline
	// when the caller is the loop itself, preventing the deadlock that
	// used to trigger when a console command handler (running on the loop)
	// called SplitCell and this executor tried to schedule back.
	var ents [][]byte
	var sess [][]byte
	var ctxEntries []*meshpb.BorderContextEntry
	ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
	defer cancel()
	runErr := srcCell.Engine.RunOnLoop(ctx, func() error {
		// For MERGE, freeze the donor's handoff_driver for the full
		// drain window (serialize → ship → commit → release). Keeps
		// late-ticking handoffs from racing the merge populate and
		// producing duplicate netIDs on the survivor. Setting the flag
		// inside RunOnLoop guarantees no handoff has fired in the
		// serialize-to-flag window (RunOnLoop serializes against the
		// game loop).
		if cmd.Kind == CellTransferMerge {
			srcCell.Stage.SetDrainingForMerge(true)
		}
		// Drain Replica→Live promotes BEFORE the commit-path serializer
		// walks the world. The serializer excludes Replicas, so a bot
		// mid-handoff (Replica with a pending promote whose commitTick
		// has already arrived but PostSystems hasn't fired yet this tick,
		// OR is numerically ahead of this cell's clusterTick due to
		// cross-host clock skew) would be skipped — and the cell's about
		// to be torn down by the commit, so PostSystems' later drain
		// happens on a doomed cell. See flushDuePromotesForCommit for
		// the per-kind draining policy.
		flushDuePromotesForCommit(srcCell, cmd.Kind)
		var err error
		switch cmd.Kind {
		case CellTransferSplit:
			ents, err = serializeQuadrantEntities(srcCell, int(cmd.Quadrant))
		case CellTransferMerge, CellTransferMigrate:
			ents, err = serializeAllEntities(srcCell)
		default:
			err = fmt.Errorf("unsupported kind %v", cmd.Kind)
		}
		if err != nil {
			return err
		}
		// Entity-less sessions have no position from which to choose a split
		// quadrant. Send them exactly once to the deterministic fallback child
		// (quadrant zero); serializing them into all four commands would create
		// four destination sessions/VCM mappings and leave commit routing at the
		// mercy of Ready arrival order. Merge and migrate each have one logical
		// destination per source, so they carry every entity-less session.
		if cmd.Kind != CellTransferSplit || cmd.Quadrant == 0 {
			sess, err = serializeEntitylessSessions(srcCell)
			if err != nil {
				return err
			}
		}

		// Collect cross-cell visibility context for the destination so its
		// first replication frame is complete — no async rubber-band while
		// border replication catches up. Runs on the same loop tick as the
		// authoritative serialize so the snapshot is coherent. Nil-safe
		// against DynamicPartitioning (defaults: include=true, radius=0,
		// maxCount=0). See the transparent-cell-transfers design doc §4.3.
		ctxEntries, err = collectTransferContext(e, srcCell, cmd)
		if err != nil {
			return fmt.Errorf("collect border context: %w", err)
		}
		// Stop this (doomed) source cell from viewing the players it just
		// serialized for transfer. Without this the source keeps them
		// StateActive — an active viewer — and keeps sending them WorldDelta
		// frames until its deferred commit teardown (~16 ticks later), so
		// BOTH source and destination send the player every tick → conflicting
		// same-tick samples → interpolation jitter. Runs last + on-loop, only
		// on full serialize success, so a serialize failure leaves the source
		// untouched (nothing to reactivate in the runErr path below).
		srcCell.Stage.DeactivateTransferViewers()
		return nil
	})
	if runErr != nil {
		// Serialize failed: donor stays alive. Release the drain flag so
		// its handoff_driver resumes normal operation.
		if cmd.Kind == CellTransferMerge {
			srcCell.Stage.SetDrainingForMerge(false)
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return fmt.Errorf("executor: serialize timeout on %s", cmd.SrcCellID)
		}
		return fmt.Errorf("executor: serialize %s on %s: %w", cmd.Kind, cmd.SrcCellID, runErr)
	}

	// Compute world-space bounds for the destination cell.
	destCell, err := ParseCellID(string(cmd.DestCellID))
	if err != nil {
		return fmt.Errorf("executor: parse dest cell %q: %w", cmd.DestCellID, err)
	}
	minX, minY, maxX, maxY := destCell.WorldBounds(e.coord.CellSize())

	proto := &meshpb.CellTransfer{
		RequestId:  cmd.RequestID,
		Kind:       toProtoKind(cmd.Kind),
		SrcCellId:  string(cmd.SrcCellID),
		DestCellId: string(cmd.DestCellID),
		DestHostId: cmd.DestHostID,
		Quadrant:   cmd.Quadrant,
		Entities:   packRecords(ents),
		Sessions:   packRecords(sess),
		Bounds: &meshpb.CellBounds{
			MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY,
		},
		Context: ctxEntries,
	}

	e.log.Log(CatMeshCell, "executor[%s]: Execute req=%d kind=%s src=%s dest=%s dest-host=%s ents=%d sess=%d ctx=%d",
		e.host.ID, cmd.RequestID, cmd.Kind, cmd.SrcCellID, cmd.DestCellID, cmd.DestHostID, len(ents), len(sess), len(ctxEntries))

	// Register source-cell state for the request BEFORE shipping, so a racing
	// Abort (even one arriving before ship completes) can find and undo it.
	// transferSrcCells (all kinds) lets Abort reactivate the players we just
	// deactivated; mergeSources (merge only) lets it clear the drain flag.
	// On ship failure we undo inline below.
	e.mu.Lock()
	if e.transferSrcCells[cmd.RequestID] == nil {
		e.transferSrcCells[cmd.RequestID] = make(map[MeshCellID]*Cell)
	}
	e.transferSrcCells[cmd.RequestID][srcCell.MeshID()] = srcCell
	if cmd.Kind == CellTransferMerge {
		if e.mergeSources[cmd.RequestID] == nil {
			e.mergeSources[cmd.RequestID] = make(map[MeshCellID]*Cell)
		}
		e.mergeSources[cmd.RequestID][srcCell.MeshID()] = srcCell
	}
	e.mu.Unlock()
	if err := e.shipToDestination(cmd.DestHostID, string(cmd.DestCellID), proto); err != nil {
		// Ship failed (e.g. destination host unreachable). Undo source-side
		// state: release the merge drain flag and reactivate the players we
		// deactivated, so the surviving source keeps serving them.
		if cmd.Kind == CellTransferMerge {
			srcCell.Stage.SetDrainingForMerge(false)
		}
		e.mu.Lock()
		if sources := e.mergeSources[cmd.RequestID]; sources != nil {
			delete(sources, srcCell.MeshID())
			if len(sources) == 0 {
				delete(e.mergeSources, cmd.RequestID)
			}
		}
		if sources := e.transferSrcCells[cmd.RequestID]; sources != nil {
			delete(sources, srcCell.MeshID())
			if len(sources) == 0 {
				delete(e.transferSrcCells, cmd.RequestID)
			}
		}
		e.mu.Unlock()
		e.reactivateSourceViewers(srcCell)
		return err
	}
	return nil
}

// reactivateSourceViewers returns a source cell's transfer-deactivated players
// to StateActive, on the cell's game loop. Called on ship failure or abort
// when the source cell survives a rolled-back transfer. No-op if the cell has
// no deactivated viewers.
func (e *cellTransferExecutor) reactivateSourceViewers(cell *Cell) {
	if cell == nil || cell.Stage == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
	defer cancel()
	err := cell.Engine.RunOnLoop(ctx, func() error {
		// Destination preparation may have pointed a shared same-host VCM
		// record and the reconnect index at the speculative destination. Restore
		// both before the source viewer becomes visible again. Cross-host source
		// VCM records already point here; RegisterSession is idempotent there.
		for _, session := range cell.Stage.transferDeactivatedViewers {
			if session == nil {
				continue
			}
			var gatewayID string
			var gatewayConnID uint32
			if e.coord.vcm != nil {
				if key, epoch, ok := e.coord.vcm.LookupRouteByLocal(session.ConnID); ok {
					e.coord.vcm.RegisterSession(key, session.Username, epoch, cell.MeshID())
					gatewayID = key.GatewayID
					gatewayConnID = key.ConnID
				}
			}
			e.coord.touchActiveUser(session.UserID, session.Username, gatewayID, gatewayConnID, e.host.ID, cell.MeshID())
		}
		cell.Stage.ReactivateTransferViewers()
		return nil
	})
	if err != nil {
		// Discarding this was hiding two different failures. ErrLoopStopped
		// means the source cell is gone, so there are no viewers left to
		// reactivate and the rollback is complete; anything else means live
		// viewers stayed deactivated and are now invisible to themselves.
		if errors.Is(err, engine.ErrLoopStopped) {
			e.coord.Log.Log(CatMeshCell,
				"executor: source cell %s retired before viewer reactivation", cell.MeshID())
		} else {
			e.coord.Log.Log(CatMeshCell,
				"executor: reactivating source viewers on %s failed: %v", cell.MeshID(), err)
		}
	}
}

// shipToDestination routes a populated CellTransfer to the destination host.
// If the dest is local (another Host in this process), skip the wire and
// call Receive directly on the dest executor. Otherwise marshal into a
// MeshFrame and SendReliable via HostNetwork.
func (e *cellTransferExecutor) shipToDestination(destHostID, destCellID string, proto *meshpb.CellTransfer) error {
	if destExec := e.coord.localHostExecutor(destHostID); destExec != nil {
		return destExec.Receive(proto)
	}
	frame := &meshpb.MeshFrame{
		DestCellId: destCellID,
		Msg:        &meshpb.MeshFrame_CellTransfer{CellTransfer: proto},
	}
	return e.host.SendReliable(destHostID, frame)
}

// ───────────────────────────────────────────────────────────────────────────
// Receive — target-host entry point
// ───────────────────────────────────────────────────────────────────────────

// Receive is invoked on the TARGET host once a CellTransfer arrives (either
// via local fast-path from a colocated source, or via a routed MeshFrame
// from a remote source). Creates the destination cell in-process, populates
// it with entities and sessions, and reports CellTransferReady to the
// coordinator.
//
// Sends Ready with ok=false if anything fails, so the orchestrator can roll
// back promptly instead of waiting for the timeout.
func (e *cellTransferExecutor) Receive(proto *meshpb.CellTransfer) error {
	destCellID, err := ParseCellID(proto.DestCellId)
	if err != nil {
		e.reportReady(proto, false, fmt.Sprintf("parse dest cell: %v", err), nil)
		return err
	}

	existing := e.host.CellByID(MeshCellID(proto.DestCellId))

	// MERGE populates donor entities + sessions INTO a live survivor cell
	// that already exists on this host. The orchestrator picks a surviving
	// sibling up front and dispatches 3 CellTransfer{MERGE} commands — one
	// per donor — all targeting that same sibling. Every Receive call here
	// must therefore route populateCell against the existing cell, not
	// create a new one (createNode would collide and trip the Host.Cells
	// unique-key invariant).
	//
	// No pending-receives tracking for merge because the survivor pre-exists
	// and outlives the commit; an Abort for a merge request can't tear the
	// cell down anyway (donors are already mixed in by then). Merge rollback
	// is best-effort and logged at the orchestrator level.
	if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE {
		if existing == nil {
			err := fmt.Errorf("merge target %s not present on host %s", proto.DestCellId, e.host.ID)
			e.reportReady(proto, false, err.Error(), nil)
			return err
		}
		// Track the survivor cell so Abort / renameCellOnNode can clear the
		// drain-for-merge freeze. Idempotent across the 3 donor Receives
		// for one merge — they all target the same survivor.
		e.mu.Lock()
		e.mergeSurvivors[proto.RequestId] = existing
		e.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		perr := existing.Engine.RunOnLoop(ctx, func() error {
			// Two-part fix for the post-merge stale-demote race:
			//
			//   (1) Cancel pending demotes on the survivor targeting any of
			//       the doomed donor siblings. These are demotes the survivor
			//       queued from a pre-merge handoff to a sibling that hasn't
			//       fired yet. Without this, the demote fires AFTER populate
			//       dedup'd the same netID — flipping the Live to a Replica
			//       with newSource = a torn-down donor → TTL expires → bot
			//       lost.
			//
			//   (2) Set drainingForMerge=true on the survivor for the merge
			//       window. The survivor cell keeps ticking between the 3
			//       Receives and between the last Receive and the eventual
			//       commit/rename — BoundarySystem keeps queueing crossings,
			//       and HandoffDriver.Tick keeps queueing pendingDemotes
			//       targeting whichever neighbors the topology still says
			//       exist (still the soon-to-be-doomed siblings until rename
			//       runs). Freezing the handoff_driver for the merge window
			//       prevents new stale demotes; (1) handles the few that
			//       slipped in before this Receive ran. The flag is cleared
			//       in renameCellOnNode (success) or Abort (rollback).
			//
			// Both run inside RunOnLoop so they're atomic w.r.t. the next
			// PostSystems Tick.
			cancelStaleDemotesOnSurvivor(existing, MeshCellID(proto.DestCellId))
			existing.Stage.SetDrainingForMerge(true)
			e.captureMergeNativeLive(proto.RequestId, existing)
			_, err := e.populateCell(existing, proto)
			return err
		})
		cancel()
		if perr != nil {
			// Keep the survivor frozen and its request bookkeeping intact until
			// the failure Ready drives Abort. Earlier donors may already have
			// populated this survivor and must be removed atomically before its
			// handoff driver resumes.
			if errors.Is(perr, context.DeadlineExceeded) {
				err := fmt.Errorf("executor: MERGE populate timeout on %s", proto.DestCellId)
				e.reportReady(proto, false, err.Error(), nil)
				return err
			}
			e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d MERGE populate failed: %v",
				e.host.ID, proto.RequestId, perr)
			e.reportReady(proto, false, perr.Error(), nil)
			return perr
		}
		e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d MERGE src=%s dest=%s OK",
			e.host.ID, proto.RequestId, proto.SrcCellId, proto.DestCellId)
		e.reportReady(proto, true, "", nil)
		return nil
	}

	// Idempotency for SPLIT/MIGRATE: if the dest cell already exists, this
	// is a duplicate delivery (retry or already committed) and we just ack.
	if existing != nil {
		e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d cell %s already present — ack only",
			e.host.ID, proto.RequestId, proto.DestCellId)
		e.reportReady(proto, true, "", nil)
		return nil
	}

	spatialCellSize := e.coord.resolveSpatialCellSize()

	e.coord.mu.Lock()
	// createNode self-registers the cell in coord.Cells / coord.CellOwner
	// under the coord lock.
	node, systems := e.coord.createNode(destCellID, spatialCellSize, e.host, true /*fromSplit*/)
	e.host.AddCell(destCellID, node)
	e.coord.mu.Unlock()

	// Track the new cell as pending until the orchestrator commits. If an
	// Abort comes in before commit, we'll unwind it.
	e.mu.Lock()
	if e.pending[proto.RequestId] == nil {
		e.pending[proto.RequestId] = make(map[MeshCellID]*pendingReceive)
	}
	e.pending[proto.RequestId][MeshCellID(proto.DestCellId)] = &pendingReceive{
		requestID: proto.RequestId,
		cellID:    destCellID,
		cellKey:   MeshCellID(proto.DestCellId),
		cell:      node,
	}
	e.mu.Unlock()

	// Two-phase init: World.Init then system Init. Mirrors Build()'s order.
	node.Stage.Init()
	initSystems(systems)
	fireCellSystemsReady(e.coord, node)

	// Start the game loop before enqueuing the populate closure so the
	// loop-job drain actually runs.
	go node.Run(context.Background())

	// Populate on the new cell's game loop via RunOnLoop (safe against
	// on-loop reentrance when the originator was a console command).
	var adoptedUsers []string
	popCtx, popCancel := context.WithTimeout(context.Background(), executorAdminTimeout)
	perr := node.Engine.RunOnLoop(popCtx, func() error {
		users, err := e.populateCell(node, proto)
		if err != nil {
			return err
		}
		adoptedUsers = users
		return nil
	})
	popCancel()
	if perr != nil {
		if errors.Is(perr, context.DeadlineExceeded) {
			e.teardownPending(proto.RequestId)
			err := fmt.Errorf("executor: populate timeout on %s", proto.DestCellId)
			e.reportReady(proto, false, err.Error(), nil)
			return err
		}
		e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d populate failed: %v — aborting",
			e.host.ID, proto.RequestId, perr)
		e.teardownPending(proto.RequestId)
		e.reportReady(proto, false, perr.Error(), nil)
		return perr
	}

	e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d kind=%v dest=%s OK (adopted %d)",
		e.host.ID, proto.RequestId, proto.Kind, proto.DestCellId, len(adoptedUsers))
	e.reportReady(proto, true, "", adoptedUsers)
	return nil
}

// captureMergeNativeLive records the survivor's authoritative set before the
// first donor for requestID is populated. It runs on the survivor game loop;
// the snapshot is immutable until Commit or Abort releases it.
func (e *cellTransferExecutor) captureMergeNativeLive(requestID uint64, cell *Cell) {
	e.mu.Lock()
	if _, ok := e.mergeNativeLive[requestID]; ok {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	native := make(map[uint32]struct{})
	netIDMap := cell.Stage.NetworkIDMap()
	filter := ecs.NewFilter1[component.NetworkID](cell.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	query := filter.Query()
	for query.Next() {
		entity := query.Entity()
		if netIDMap.HasAll(entity) {
			native[netIDMap.Get(entity).ID] = struct{}{}
		}
	}
	query.Close()

	e.mu.Lock()
	if _, ok := e.mergeNativeLive[requestID]; !ok {
		e.mergeNativeLive[requestID] = native
	}
	e.mu.Unlock()
}

func (e *cellTransferExecutor) mergeImportedEntity(requestID uint64, netID uint32, entity ecs.Entity) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	imports := e.mergeImportedLive[requestID]
	if imports == nil {
		return false
	}
	imported, ok := imports[netID]
	return ok && imported == entity
}

func (e *cellTransferExecutor) recordMergeImportedEntity(requestID uint64, netID uint32, entity ecs.Entity) {
	e.mu.Lock()
	if e.mergeImportedLive[requestID] == nil {
		e.mergeImportedLive[requestID] = make(map[uint32]ecs.Entity)
	}
	e.mergeImportedLive[requestID][netID] = entity
	e.mu.Unlock()
}

func (e *cellTransferExecutor) recordMergeImportedSession(
	requestID uint64,
	session *engine.PlayerSession,
	vcmKey SessionKey,
	dropVCM bool,
) {
	if session == nil {
		return
	}
	e.mu.Lock()
	if e.mergeImportedSessions[requestID] == nil {
		e.mergeImportedSessions[requestID] = make(map[engine.SessionID]mergeImportedSession)
	}
	e.mergeImportedSessions[requestID][session.ID] = mergeImportedSession{
		session: session,
		vcmKey:  vcmKey,
		dropVCM: dropVCM,
	}
	e.mu.Unlock()
}

// mergeSessionImportState snapshots whether destination preparation will
// create a PlayerSession and/or VCM key. Rollback removes only state created
// by this merge; a same-host key already belongs to the surviving source and
// must instead be pointed back there during source reactivation.
func mergeSessionImportState(
	cell *Cell,
	sourceConnID uint32,
	gatewayID string,
	gatewayConnID uint32,
) (prior *engine.PlayerSession, key SessionKey, dropVCM bool) {
	localID := sourceConnID
	if gatewayID != "" && gatewayConnID != 0 && cell != nil && cell.Stage != nil &&
		cell.Stage.coord != nil && cell.Stage.coord.vcm != nil {
		key = SessionKey{GatewayID: gatewayID, ConnID: gatewayConnID}
		if mapped, ok := cell.Stage.coord.vcm.LookupByKey(key); ok {
			localID = mapped
		} else {
			dropVCM = true
		}
	}
	if cell != nil && cell.Engine != nil {
		prior = cell.Engine.Players.ByConnID(localID)
	}
	return prior, key, dropVCM
}

// mergeTransferFrameIsNewer decides whether an incoming donor copy should
// replace a copy materialized by an earlier donor. Authority epoch is the
// primary fence; when it is equal, player stream generation breaks the tie.
// Both comparisons use serial arithmetic so uint32 wrap preserves ordering.
func mergeTransferFrameIsNewer(cell *Cell, current ecs.Entity, incoming *TransferFrame) bool {
	if !cell.Stage.netIDMap.HasAll(current) {
		return true
	}
	currentEpoch := cell.Stage.netIDMap.Get(current).Epoch
	incomingEpoch := incoming.Epoch + 1 // spawnCellTransferLive advances once
	if incomingEpoch != currentEpoch {
		return serialUint32After(incomingEpoch, currentEpoch)
	}
	if incoming.ConnID == 0 {
		return false
	}
	if !cell.Stage.playerMap.HasAll(current) {
		return true
	}
	currentConnID := cell.Stage.playerMap.Get(current).ConnID
	currentSession := cell.Engine.Players.ByConnID(currentConnID)
	if currentSession == nil {
		return true
	}
	incomingGeneration := incoming.StreamGeneration + 1 // same transfer bump
	return serialUint32After(incomingGeneration, currentSession.StreamGeneration)
}

// removeMergeDonorLive removes a losing donor copy without publishing an
// authoritative despawn. The winning copy is spawned immediately afterward
// under the same netID, and any player session is transfer-removed so its
// destination-local connID can be rebound cleanly.
func removeMergeDonorLive(cell *Cell, entity ecs.Entity) error {
	if !cell.Engine.ECS.Alive(entity) {
		return fmt.Errorf("merge donor entity is no longer alive")
	}
	if cell.Stage.playerMap.HasAll(entity) {
		connID := cell.Stage.playerMap.Get(entity).ConnID
		if session := cell.Engine.Players.ByConnID(connID); session != nil && session.Entity == entity {
			cell.Engine.Players.RemoveTransferred(session)
		}
	}
	if !cell.Engine.RemoveEntityNowWithPolicy(entity, engine.RemovalLocalOnly) {
		return fmt.Errorf("merge donor entity removal failed")
	}
	return nil
}

// destinationConnIDForSessionTransfer translates a source-host-local connID
// into the destination host's local namespace. A transfer without a stable
// SessionKey is the colocated/in-process case, where the original connID is
// already valid. A keyed transfer must never fall back to that source-local
// value: doing so registers a session that gateway input can never address.
func destinationConnIDForSessionTransfer(cell *Cell, st SessionTransfer) (uint32, error) {
	hasGatewayID := st.GatewayID != ""
	hasGatewayConnID := st.GatewayConnID != 0
	if !hasGatewayID && !hasGatewayConnID {
		return st.ConnID, nil
	}
	if !hasGatewayID || !hasGatewayConnID {
		return 0, fmt.Errorf("incomplete SessionKey gateway=%q conn=%d", st.GatewayID, st.GatewayConnID)
	}
	if cell == nil || cell.Stage == nil || cell.Stage.coord == nil || cell.Stage.coord.vcm == nil {
		return 0, fmt.Errorf("session %s:%d requires a destination VCM", st.GatewayID, st.GatewayConnID)
	}
	key := SessionKey{GatewayID: st.GatewayID, ConnID: st.GatewayConnID}
	return cell.Stage.coord.vcm.RegisterSession(key, st.Username, st.SessionEpoch, cell.MeshID()), nil
}

// populateCell unpacks entities + sessions from the proto and materializes
// them on the cell's game loop. Runs entirely synchronously on the game
// loop goroutine; safe to call ECS APIs without additional locking.
//
// For each transferred entity that carries a PlayerConn (i.e. a migrated
// player), this method:
//  1. Registers a destination-local engine PlayerSession in StateActive
//     without firing OnEnter (no duplicate spawn);
//  2. Attaches the freshly-spawned entity to that session so the input
//     router can dispatch ClientInput frames to it;
//  3. Remaps the session route in coord.sessionRoutes to THIS destination
//     cell via Migrate (which bumps the epoch atomically), so the
//     gateway's next ClientInput frame lands on the right host + cell.
//
// Without steps 1–3 the destination child cell has a live player entity
// but no PlayerSession, and InputRouter.ProcessInput silently drops all
// input for the player until they reconnect. This is the root cause of
// the "player freeze after split" S7 bug fixed alongside the bot freeze.
// populateCell materializes entities + sessions on the dest cell. Returns
// the usernames whose entities landed here — used by applySplitCommit to
// route each player's session to the child that actually received their
// entity rather than a blind children[0] fallback.
func (e *cellTransferExecutor) populateCell(cell *Cell, proto *meshpb.CellTransfer) ([]string, error) {
	entBlobs, err := unpackRecords(proto.Entities)
	if err != nil {
		return nil, fmt.Errorf("unpack entities: %w", err)
	}

	// Determine the dest host ID that session routes should point at.
	// proto.DestHostId is empty for local-host (same-process) transfers —
	// fall back to the executing host's ID in that case.
	destHostID := proto.DestHostId
	if destHostID == "" {
		destHostID = e.host.ID
	}

	// Build the netID-already-present set on the destination. MERGE needs the
	// entity handle as well as membership: a native survivor entity always
	// wins, while a copy materialized by an earlier donor may be replaced by
	// a serial-newer donor copy. SPLIT and MIGRATE populate a fresh cell, so
	// the set is empty there and this ordering logic is a no-op.
	existing := make(map[uint32]ecs.Entity)
	netIDMap := cell.Stage.NetworkIDMap()
	var mergeNative map[uint32]struct{}
	if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE {
		e.mu.Lock()
		mergeNative = e.mergeNativeLive[proto.RequestId]
		e.mu.Unlock()
	}
	// Commit-path serializer: Ghost and Replica excluded — only count
	// authoritative entities. Replica→Live is handled explicitly by
	// PromoteReplicaToLive on the destination side during handoff.
	entFilter := ecs.NewFilter1[component.NetworkID](cell.Stage.Engine().ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())
	func() {
		entQuery := entFilter.Query()
		// Scope-bound defer ensures world unlocks even if a body panic
		// would otherwise prevent the natural Next-returns-false unlock.
		defer entQuery.Close()
		for entQuery.Next() {
			entity := entQuery.Entity()
			if netIDMap.HasAll(entity) {
				existing[netIDMap.Get(entity).ID] = entity
			}
		}
	}()

	var adoptedUsers []string
	for i, blob := range entBlobs {
		frame, err := UnmarshalTransferFrame(blob)
		if err != nil {
			return nil, fmt.Errorf("unmarshal entity %d: %w", i, err)
		}
		var priorImportedSession *engine.PlayerSession
		var importedSessionKey SessionKey
		var dropImportedVCM bool
		if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE && frame.ConnID != 0 {
			priorImportedSession, importedSessionKey, dropImportedVCM = mergeSessionImportState(
				cell, frame.ConnID, frame.GatewayID, frame.GatewayConnID)
		}
		if existingEnt, dup := existing[frame.NetworkID]; dup {
			_, nativeSurvivor := mergeNative[frame.NetworkID]
			priorDonor := e.mergeImportedEntity(proto.RequestId, frame.NetworkID, existingEnt)
			replaceDonor := proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE &&
				!nativeSurvivor && priorDonor && mergeTransferFrameIsNewer(cell, existingEnt, frame)
			if replaceDonor {
				cell.Stage.Engine().Log.Log(CatMeshCell,
					"[%s] populate dedup: replacing older donor netID=%d", cell.MeshID(), frame.NetworkID)
				if err := removeMergeDonorLive(cell, existingEnt); err != nil {
					return nil, fmt.Errorf("replace donor entity %d: %w", i, err)
				}
				delete(existing, frame.NetworkID)
			} else {
				retained := "existing authority"
				if nativeSurvivor {
					retained = "native survivor"
				}
				cell.Stage.Engine().Log.Log(CatMeshCell,
					"[%s] populate dedup: retaining %s netID=%d", cell.MeshID(),
					retained, frame.NetworkID)
				// A player whose entity is already present on the survivor
				// (e.g. it crossed via boundary handoff before the merge
				// committed) still needs its session wired on this host so
				// gateway-routed input lands on the right entity. Locate the
				// existing entity by netID and re-register the session
				// against it — bots with ConnID==0 fall through the continue.
				if frame.ConnID != 0 && frame.Username != "" {
					if _, _, ok := cell.Stage.LookupNetID(frame.NetworkID); ok {
						// frame.ConnID is the SOURCE host's local ID; remap to
						// this host's local ID via the VCM so engine.Players
						// is keyed under the same value the gateway-forwarded
						// ClientInput frames will resolve to. Without this
						// remap a cross-host MERGE (entity already present on
						// survivor from a prior boundary handoff) leaves the
						// engine session under a connID that no inbound input
						// ever resolves to, and the player loses input.
						localID := frame.ConnID
						if frame.GatewayConnID != 0 && cell.Stage.coord != nil && cell.Stage.coord.vcm != nil {
							key := SessionKey{GatewayID: frame.GatewayID, ConnID: frame.GatewayConnID}
							localID = cell.Stage.coord.vcm.RegisterSession(key, frame.Username, 1, cell.MeshID())
						}
						cell.Engine.Players.RegisterSessionTransfer(localID, frame.Username, "active", nil)
						if sess := cell.Engine.Players.ByConnID(localID); sess != nil {
							sess.Entity = existingEnt
							// See the equivalent assignment in the
							// non-dedup branch below for why DebugFlags
							// must be set here, not in
							// onPlayerTransferReceived.
							sess.DebugFlags = engine.DebugFlag(frame.DebugFlags)
							sess.UserID = frame.UserID
						}
						if cell.Stage != nil && cell.Stage.coord != nil {
							cell.Stage.coord.touchActiveUser(frame.UserID, frame.Username, frame.GatewayID, frame.GatewayConnID, cell.Stage.coord.HostForCellID(cell.MeshID()), cell.MeshID())
						}
						adoptedUsers = append(adoptedUsers, frame.Username)
					}
				}
				continue
			}
		}
		// If the survivor holds a border-replica for this netID (sibling
		// cross-subcell replication common during merge), remove it
		// before spawning Live. The donor's serialized entity IS the
		// authoritative copy; the stale replica would otherwise make
		// SpawnFromTransferCore's netIDIndex reject Live-on-Replica.
		cell.Stage.RemoveReplicaByNetID(frame.NetworkID)
		entity, spawnedFrame, err := spawnCellTransferLive(cell.Stage, frame)
		if err != nil {
			return nil, fmt.Errorf("spawn entity %d: %w", i, err)
		}
		existing[frame.NetworkID] = entity
		if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE {
			e.recordMergeImportedEntity(proto.RequestId, frame.NetworkID, entity)
		}
		if spawnedFrame.ConnID == 0 || spawnedFrame.Username == "" {
			continue
		}

		// SpawnFromTransferCore already remapped frame.ConnID via the VCM
		// (source-local → destination-local) and returned the rewritten
		// frame. Use spawnedFrame.ConnID — NOT the un-remapped frame from
		// the populate-side decode, which still holds the source's local
		// ID and means nothing on this host. RegisterSessionTransfer
		// skips OnEnter callbacks so it won't spawn a duplicate entity —
		// the entity we just created is the one that should be
		// authoritative on this cell.
		cell.Engine.Players.RegisterSessionTransfer(spawnedFrame.ConnID, spawnedFrame.Username, "active", nil)
		if sess := cell.Engine.Players.ByConnID(spawnedFrame.ConnID); sess != nil {
			sess.Entity = entity
			sess.StreamGeneration = spawnedFrame.StreamGeneration
			// Restore the per-session debug bitmask carried in the
			// transfer frame. Must happen AFTER RegisterSessionTransfer
			// — Stage.onPlayerTransferReceived fires inside
			// SpawnFromTransferCore (line above) and tries the same
			// thing, but the session doesn't exist yet at that point
			// and its by-connID lookup silently no-ops. Setting it here
			// ensures the broadcaster's `if sess.DebugFlags == 0` gate
			// doesn't strand transferred players with no overlay
			// updates after split/merge/migrate.
			sess.DebugFlags = engine.DebugFlag(spawnedFrame.DebugFlags)
			sess.UserID = spawnedFrame.UserID
			if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE && priorImportedSession == nil {
				e.recordMergeImportedSession(proto.RequestId, sess, importedSessionKey, dropImportedVCM)
			}
		}
		if cell.Stage != nil && cell.Stage.coord != nil {
			cell.Stage.coord.touchActiveUser(spawnedFrame.UserID, spawnedFrame.Username, spawnedFrame.GatewayID, spawnedFrame.GatewayConnID, cell.Stage.coord.HostForCellID(cell.MeshID()), cell.MeshID())
		}
		adoptedUsers = append(adoptedUsers, spawnedFrame.Username)

		// Session route migration is handled by the commit path
		// (applyMigrateCommit's remapHostCell), which is the single
		// authoritative epoch bump. Doing it here as well caused a
		// double bump: populate bumped to N+1, commit bumped to N+2,
		// leaving VCM on N+1 while the gateway learned N+2.
	}

	sessBlobs, err := unpackRecords(proto.Sessions)
	if err != nil {
		return nil, fmt.Errorf("unpack sessions: %w", err)
	}
	for i, blob := range sessBlobs {
		var st SessionTransfer
		if err := json.Unmarshal(blob, &st); err != nil {
			return nil, fmt.Errorf("decode session %d: %w", i, err)
		}
		if st.ConnID == 0 {
			continue
		}
		priorSession, importedSessionKey, dropImportedVCM := mergeSessionImportState(
			cell, st.ConnID, st.GatewayID, st.GatewayConnID)
		localID, err := destinationConnIDForSessionTransfer(cell, st)
		if err != nil {
			return nil, fmt.Errorf("remap session %d: %w", i, err)
		}
		cell.Engine.Players.RegisterSessionTransfer(localID, st.Username, st.StateTag, st.Data)
		if sess := cell.Engine.Players.ByConnID(localID); sess != nil {
			sess.StreamGeneration = st.StreamGeneration
			sess.UserID = st.UserID
			if proto.Kind == meshpb.CellTransferKind_CELL_TRANSFER_MERGE && priorSession == nil {
				e.recordMergeImportedSession(proto.RequestId, sess, importedSessionKey, dropImportedVCM)
			}
		}
		if cell.Stage != nil && cell.Stage.coord != nil {
			cell.Stage.coord.touchActiveUser(st.UserID, st.Username, st.GatewayID, st.GatewayConnID, destHostID, cell.MeshID())
		}
		if st.Username != "" {
			adoptedUsers = append(adoptedUsers, st.Username)
		}
	}

	// Materialize cross-cell border context shipped by the source side
	// (sibling-quadrant locals + outer-neighbor replicas). Same TransferFrame
	// codec as authoritative entities; the only difference is the spawn API:
	// upsertBorderReplicaFromTransfer creates a PresenceReplica rather than a
	// PresenceLive entity. After this loop the dest cell's spatial grid holds
	// the complete visible set (locals + cross-cell context), so its first
	// ReplicationSystem tick emits a FreshSnapshot that needs no client-side
	// reconcile churn — the transparent-handoff payoff.
	for i, ctxEntry := range proto.Context {
		frame, err := UnmarshalTransferFrame(ctxEntry.Entity)
		if err != nil {
			cell.Stage.Engine().Log.Log(CatMeshCell,
				"[%s] populate context: unmarshal entry %d failed: %v", cell.MeshID(), i, err)
			continue
		}
		// Never let a context replica shadow an entity that is authoritative
		// (Live) on this cell — `existing` holds the Live netIDs spawned above
		// plus any the survivor already held. A Live→Replica clash would be
		// rejected by the netIDIndex anyway; skip cleanly. (Entities that are
		// already REPLICAs here aren't in `existing`, so upsertBorderReplica
		// just updates them in place, which is correct.)
		if _, isLive := existing[frame.NetworkID]; isLive {
			continue
		}
		cell.Stage.upsertBorderReplicaFromTransfer(frame, MeshCellID(ctxEntry.SourceCellId))
	}

	return adoptedUsers, nil
}

// spawnCellTransferLive materializes one new authoritative entity for a
// split, merge, or migrate transfer. A fresh authority must always advance
// NetworkID.Epoch and, for player entities, StreamGeneration. Every newly
// created ReplicationSystem starts its per-viewer sequence at one. Advancing
// the transferred copy keeps late frames from the old cell stale while the
// source session remains pinned until commit.
//
// Callers must perform duplicate-Live checks first. An entity already Live on
// a merge survivor keeps its current authority and epoch; only newly
// materialized donor entities pass through this helper.
func spawnCellTransferLive(stage *Stage, frame *TransferFrame) (ecs.Entity, *TransferFrame, error) {
	frame.Epoch++
	if frame.ConnID != 0 {
		frame.StreamGeneration++
	}
	blob, err := MarshalTransferFrame(frame)
	if err != nil {
		return ecs.Entity{}, nil, err
	}
	return stage.SpawnFromTransferCore(blob, PresenceLive)
}

// ───────────────────────────────────────────────────────────────────────────
// Abort — target-host teardown of a partial receive
// ───────────────────────────────────────────────────────────────────────────

// Abort is invoked on the TARGET host when the orchestrator rolls back a
// partial CellTransfer. Tears down the cell that was created by a previous
// Receive for the same request_id. Idempotent: if there is no pending
// record (e.g. the receive already committed, or no receive ever ran), Abort
// is a no-op.
func (e *cellTransferExecutor) Abort(proto *meshpb.CellTransferAbort) {
	if proto == nil {
		return
	}
	// Clear MERGE drain flags for this request if we set any:
	//   - mergeSources tracks the donor cell whose Execute set the flag.
	//     Donor stays alive on abort, so without this its handoff_driver
	//     would stay frozen until some later merge hits it.
	//   - mergeSurvivors tracks the survivor cell whose Receive set the
	//     flag. Same lifetime concern: clear or the survivor permanently
	//     stops emitting handoffs.
	e.mu.Lock()
	sources, hadSrc := e.mergeSources[proto.RequestId]
	if hadSrc {
		delete(e.mergeSources, proto.RequestId)
	}
	surv, hadSurv := e.mergeSurvivors[proto.RequestId]
	if hadSurv {
		delete(e.mergeSurvivors, proto.RequestId)
	}
	delete(e.mergeNativeLive, proto.RequestId)
	importedLive := e.mergeImportedLive[proto.RequestId]
	delete(e.mergeImportedLive, proto.RequestId)
	importedSessions := e.mergeImportedSessions[proto.RequestId]
	delete(e.mergeImportedSessions, proto.RequestId)
	deactivatedSources, hadDeact := e.transferSrcCells[proto.RequestId]
	if hadDeact {
		delete(e.transferSrcCells, proto.RequestId)
	}
	e.mu.Unlock()
	if hadSrc {
		for _, src := range sources {
			if src == nil || src.Stage == nil {
				continue
			}
			src.Stage.SetDrainingForMerge(false)
			e.log.Log(CatMeshCell, "executor[%s]: abort req=%d cleared merge drain flag on donor %s",
				e.host.ID, proto.RequestId, src.MeshID())
		}
	}
	if hadSurv && surv != nil && surv.Stage != nil {
		e.rollbackMergeImports(proto.RequestId, surv, importedLive, importedSessions)
	}
	// Reactivate any players the source deactivated for this transfer so the
	// surviving (un-committed) source cell resumes serving them. Rollback fans
	// Abort out to both successful destinations and every command source, so
	// this also runs when source and destination live on different hosts.
	if hadDeact {
		for _, src := range deactivatedSources {
			if src == nil {
				continue
			}
			e.reactivateSourceViewers(src)
			e.log.Log(CatMeshCell, "executor[%s]: abort req=%d reactivated source viewers on %s",
				e.host.ID, proto.RequestId, src.MeshID())
		}
	}
	e.teardownPending(proto.RequestId)
}

func (e *cellTransferExecutor) rollbackMergeImports(
	requestID uint64,
	survivor *Cell,
	importedLive map[uint32]ecs.Entity,
	importedSessions map[engine.SessionID]mergeImportedSession,
) {
	ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
	defer cancel()
	err := survivor.Engine.RunOnLoop(ctx, func() error {
		for netID, entity := range importedLive {
			current, presence, ok := survivor.Stage.LookupNetID(netID)
			if !ok || presence != PresenceLive || current != entity {
				continue
			}
			if err := removeMergeDonorLive(survivor, entity); err != nil {
				return fmt.Errorf("rollback imported netID %d: %w", netID, err)
			}
		}
		for _, imported := range importedSessions {
			session := imported.session
			if session == nil {
				continue
			}
			current := survivor.Engine.Players.ByConnID(session.ConnID)
			if current == session {
				survivor.Engine.Players.RemoveTransferred(session)
				current = nil
			}
			if imported.dropVCM && current == nil && e.coord.vcm != nil {
				if localID, ok := e.coord.vcm.LookupByKey(imported.vcmKey); ok && localID == session.ConnID {
					e.coord.vcm.DropSession(imported.vcmKey)
				}
			}
		}
		survivor.Stage.SetDrainingForMerge(false)
		return nil
	})
	if err != nil {
		e.log.Log(CatMeshCell, "executor[%s]: abort req=%d survivor %s import cleanup failed: %v",
			e.host.ID, requestID, survivor.MeshID(), err)
		return
	}
	e.log.Log(CatMeshCell,
		"executor[%s]: abort req=%d removed %d merge imports and cleared survivor %s drain flag",
		e.host.ID, requestID, len(importedLive), survivor.MeshID())
}

// Commit releases rollback bookkeeping without touching the live cells or
// sessions that the now-committed topology owns. The coordinator sends this
// only after all Ready responses succeeded and applyCellTransferCommit
// completed. Abort remains safe until this method runs.
func (e *cellTransferExecutor) Commit(requestID uint64) {
	e.mu.Lock()
	delete(e.pending, requestID)
	delete(e.mergeSources, requestID)
	delete(e.mergeSurvivors, requestID)
	delete(e.mergeNativeLive, requestID)
	delete(e.mergeImportedLive, requestID)
	delete(e.mergeImportedSessions, requestID)
	delete(e.transferSrcCells, requestID)
	e.mu.Unlock()
	e.log.Log(CatMeshCell, "executor[%s]: commit req=%d released transfer rollback state", e.host.ID, requestID)
}

// teardownPending removes the pending record for the given request ID and
// shuts down the associated cell. Safe to call with an unknown request_id.
func (e *cellTransferExecutor) teardownPending(requestID uint64) {
	e.mu.Lock()
	pending, ok := e.pending[requestID]
	if ok {
		delete(e.pending, requestID)
	}
	e.mu.Unlock()

	if !ok {
		return
	}
	for _, pr := range pending {
		if pr == nil {
			continue
		}
		e.log.Log(CatMeshCell, "executor[%s]: abort req=%d tearing down cell %s",
			e.host.ID, requestID, pr.cellKey)

		// Remove from coord maps + host.
		e.coord.mu.Lock()
		delete(e.coord.Cells, pr.cellKey)
		delete(e.coord.CellOwner, pr.cellID)
		e.host.RemoveCell(pr.cellID)
		e.coord.mu.Unlock()

		pr.cell.Shutdown()
	}
}

// ───────────────────────────────────────────────────────────────────────────
// Ready reporting
// ───────────────────────────────────────────────────────────────────────────

// reportReady notifies the coordinator that this host has completed (or
// failed) a CellTransfer receive. Local fast-path calls orchestrator.OnReady
// directly; remote hosts send HostMessage_CellTransferReady over MeshControl.
// adoptedUsers is the usernames whose entities landed on the dest cell — used
// by applySplitCommit to route sessions per-player instead of with a blind
// fallback. Empty for failed acks.
func (e *cellTransferExecutor) reportReady(proto *meshpb.CellTransfer, ok bool, errMsg string, adoptedUsers []string) {
	// Local fast-path: the orchestrator lives in this process.
	if e.coord.orchestrator != nil && e.coord.controlClient == nil {
		e.coord.orchestrator.OnReady(proto.RequestId, MeshCellID(proto.DestCellId), e.host.ID, ok, errMsg, adoptedUsers)
		return
	}
	// Remote: send up the node's control stream to the coordinator.
	if e.coord.controlClient != nil {
		msg := &meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellTransferReady{
				CellTransferReady: &meshpb.CellTransferReady{
					RequestId:    proto.RequestId,
					DestCellId:   proto.DestCellId,
					HostId:       e.host.ID,
					Ok:           ok,
					Error:        errMsg,
					AdoptedUsers: adoptedUsers,
				},
			},
		}
		if err := e.coord.controlClient.send(msg); err != nil {
			e.log.Log(CatMeshCell, "executor[%s]: send CellTransferReady req=%d failed: %v",
				e.host.ID, proto.RequestId, err)
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Real dispatcher (satisfies cellTransferDispatcher)
// ═══════════════════════════════════════════════════════════════════════════

// cellTransferDispatcherImpl routes cellTransferCommands from the
// orchestrator to source hosts. Installed on the orchestrator by
// New.
type cellTransferDispatcherImpl struct {
	coord *Process
}

func newCellTransferDispatcher(coord *Process) *cellTransferDispatcherImpl {
	return &cellTransferDispatcherImpl{coord: coord}
}

// Dispatch routes cmd to the source host's executor. Local source hosts take
// an in-process function call; remote source hosts get a
// CoordMessage_CellTransfer over their MeshControl stream.
func (d *cellTransferDispatcherImpl) Dispatch(cmd cellTransferCommand) error {
	if exec := d.coord.localHostExecutor(cmd.SrcHostID); exec != nil {
		return exec.Execute(cmd)
	}
	return d.sendCellTransferToRemote(cmd)
}

// DispatchAbort tells a target host to roll back a partial CellTransfer.
func (d *cellTransferDispatcherImpl) DispatchAbort(requestID uint64, hostID string) error {
	if exec := d.coord.localHostExecutor(hostID); exec != nil {
		exec.Abort(&meshpb.CellTransferAbort{RequestId: requestID})
		return nil
	}
	return d.sendCellTransferAbortToRemote(requestID, hostID)
}

// DispatchCommit tells a host that the topology mutation is durable and its
// rollback bookkeeping for requestID can be released.
func (d *cellTransferDispatcherImpl) DispatchCommit(requestID uint64, hostID string) error {
	if exec := d.coord.localHostExecutor(hostID); exec != nil {
		exec.Commit(requestID)
		return nil
	}
	return d.sendCellTransferCommitToRemote(requestID, hostID)
}

// sendCellTransferToRemote ships a cellTransferCommand to a remote source
// host via the control server's CoordMessage stream. Used only when the
// source host is registered through MeshControl.
func (d *cellTransferDispatcherImpl) sendCellTransferToRemote(cmd cellTransferCommand) error {
	if d.coord.controlServer == nil {
		return fmt.Errorf("dispatcher: remote src host %q but no control server", cmd.SrcHostID)
	}
	proto := &meshpb.CoordMessage{
		CoordEpoch: d.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellTransfer{
			CellTransfer: &meshpb.CellTransfer{
				RequestId:  cmd.RequestID,
				Kind:       toProtoKind(cmd.Kind),
				SrcCellId:  string(cmd.SrcCellID),
				DestCellId: string(cmd.DestCellID),
				DestHostId: cmd.DestHostID,
				Quadrant:   cmd.Quadrant,
			},
		},
	}
	return d.coord.controlServer.sendCoordMessageToHost(cmd.SrcHostID, proto)
}

// sendCellTransferAbortToRemote ships a CellTransferAbort to a remote target
// host via the control server. Used during orchestrator rollback when the
// target host is registered through MeshControl.
func (d *cellTransferDispatcherImpl) sendCellTransferAbortToRemote(requestID uint64, hostID string) error {
	if d.coord.controlServer == nil {
		return fmt.Errorf("dispatcher: remote abort host %q but no control server", hostID)
	}
	proto := &meshpb.CoordMessage{
		CoordEpoch: d.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellTransferAbort{
			CellTransferAbort: &meshpb.CellTransferAbort{
				RequestId: requestID,
			},
		},
	}
	return d.coord.controlServer.sendCoordMessageToHost(hostID, proto)
}

// sendCellTransferCommitToRemote releases rollback state on a remote host
// after the coordinator has committed the topology mutation.
func (d *cellTransferDispatcherImpl) sendCellTransferCommitToRemote(requestID uint64, hostID string) error {
	if d.coord.controlServer == nil {
		return fmt.Errorf("dispatcher: remote commit host %q but no control server", hostID)
	}
	proto := &meshpb.CoordMessage{
		CoordEpoch: d.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellTransferCommit{
			CellTransferCommit: &meshpb.CellTransferCommit{
				RequestId: requestID,
			},
		},
	}
	return d.coord.controlServer.sendCoordMessageToHost(hostID, proto)
}

// ═══════════════════════════════════════════════════════════════════════════
// Helpers: serialization, framing, kind mapping, coord hooks
// ═══════════════════════════════════════════════════════════════════════════

// serializeQuadrantEntities runs on the source cell's game loop. Walks all
// non-ghost, non-replica entities with a Position and keeps only those whose
// local coordinates fall in the requested quadrant (0=BL, 1=BR, 2=TL, 3=TR).
// The source cell's Size(stage cell size) determines the half-plane split.
func serializeQuadrantEntities(src *Cell, quadrant int) ([][]byte, error) {
	if quadrant < 0 || quadrant > 3 {
		return nil, fmt.Errorf("invalid quadrant %d", quadrant)
	}
	half := src.CellID().Size(src.Stage.CellSize()) / 2
	wantXi := int32(quadrant & 1)
	wantYi := int32((quadrant >> 1) & 1)

	posMap := ecs.NewMap1[component.Position](src.Engine.ECS)
	// Commit-path serializer: Ghost and Replica excluded — only
	// serialize authoritative entities belonging to this cell.
	filter := ecs.NewFilter1[component.Position](src.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())

	var out [][]byte
	query := filter.Query()
	// See serializeAllEntities for the defer Close() rationale.
	defer query.Close()
	for query.Next() {
		entity := query.Entity()
		pos := posMap.Get(entity)
		xi := int32(0)
		if pos.X >= half {
			xi = 1
		}
		yi := int32(0)
		if pos.Y >= half {
			yi = 1
		}
		if xi != wantXi || yi != wantYi {
			continue
		}
		data, err := src.Stage.SerializeEntity(entity)
		if err != nil {
			return nil, fmt.Errorf("serialize entity: %w", err)
		}
		out = append(out, data)
	}
	return out, nil
}

// collectTransferContext builds the per-kind shouldInclude predicate and runs
// it through Stage.collectBorderContextFor. Must be called on the source
// cell's game loop (it reads ECS via collectBorderContextFor). Returns nil
// (no error) when context collection is disabled or the kind doesn't define a
// predicate.
//
// SPLIT: a context entity is anything within the target child quadrant's AoI
// margin that is NOT already authoritative on that child — i.e. sibling-
// quadrant locals (attributed to the owning sibling child) and replicas
// (attributed to their existing source). MERGE/MIGRATE: only replicas qualify;
// the donor/source locals become authoritative on the destination, so they
// ship as Entities, not Context.
func collectTransferContext(e *cellTransferExecutor, src *Cell, cmd cellTransferCommand) ([]*meshpb.BorderContextEntry, error) {
	cfg := e.coord.cfg.DynamicPartitioning
	includeContext := cfg == nil || cfg.IncludeBorderContext
	if !includeContext {
		return nil, nil
	}

	aoiR := src.Stage.GetAoIRadius()
	if cfg != nil && cfg.BorderContextRadius > 0 {
		aoiR = cfg.BorderContextRadius
	}
	maxCount := 0
	if cfg != nil {
		maxCount = cfg.BorderContextMaxCount
	}

	var shouldInclude func(en ecs.Entity, px, py float32, isRep bool, repSrc string) (bool, string)
	switch cmd.Kind {
	case CellTransferSplit:
		half := src.CellID().Size(src.Stage.CellSize()) / 2
		q := int(cmd.Quadrant)
		qx := float32(q & 1)
		qy := float32((q >> 1) & 1)
		// Child quadrant rect + the px>=half classification below intentionally
		// MIRROR serializeQuadrantEntities exactly, so context inclusion stays
		// consistent with which entities ship as authoritative (the dedup on
		// the destination relies on that agreement). Both assume the parent's
		// local origin is 0 — correct for a depth-0 parent (its entities are
		// base-cell-local [0,parentSize) and the parent IS the root). For a
		// depth>=1 parent the cell occupies a sub-region offset from the root
		// origin, so this math is offset-wrong — but identically to the
		// authoritative serializer, so context still matches it. Fixing nested
		// (depth>=1) splits requires offsetting BOTH this and
		// serializeQuadrantEntities by the parent's base-local origin; that's a
		// pre-existing serializer limitation, tracked as a follow-up, not a
		// regression introduced by border-context.
		rectMinX := qx * half
		rectMaxX := rectMinX + half
		rectMinY := qy * half
		rectMaxY := rectMinY + half
		children := src.CellID().Children() // [4]CellID ordered by quadrant index
		shouldInclude = func(en ecs.Entity, px, py float32, isRep bool, repSrc string) (bool, string) {
			if pointRectDist(px, py, rectMinX, rectMinY, rectMaxX, rectMaxY) > aoiR {
				return false, ""
			}
			if isRep {
				return true, repSrc
			}
			// Local: authoritative on THIS child iff it sits in this
			// quadrant — those ship as Entities, so skip them here.
			exi := 0
			if px >= half {
				exi = 1
			}
			eyi := 0
			if py >= half {
				eyi = 1
			}
			eq := exi | (eyi << 1)
			if eq == q {
				return false, ""
			}
			return true, string(children[eq].MeshID())
		}
	case CellTransferMerge, CellTransferMigrate:
		// Only replicas qualify: donor/source locals become authoritative on
		// the destination and ship as Entities. Replicas keep their existing
		// source attribution so the destination seeds them as replicas.
		shouldInclude = func(en ecs.Entity, px, py float32, isRep bool, repSrc string) (bool, string) {
			if isRep {
				return true, repSrc
			}
			return false, ""
		}
	default:
		return nil, nil
	}

	return src.Stage.collectBorderContextFor(maxCount, shouldInclude)
}

// pointRectDist returns the Euclidean distance from point (px,py) to the
// axis-aligned rectangle [minX,maxX]×[minY,maxY]. Returns 0 when the point
// is inside the rect. Used to AoI-gate split context against the target
// child quadrant's bounds.
func pointRectDist(px, py, minX, minY, maxX, maxY float32) float32 {
	dx := float32(0)
	if px < minX {
		dx = minX - px
	} else if px > maxX {
		dx = px - maxX
	}
	dy := float32(0)
	if py < minY {
		dy = minY - py
	} else if py > maxY {
		dy = py - maxY
	}
	if dx == 0 && dy == 0 {
		return 0
	}
	return float32(math.Sqrt(float64(dx*dx + dy*dy)))
}

// flushDuePromotesForCommit drains pending Replica→Live promotes BEFORE a
// commit-path serializer (split, merge, or migrate) walks the donor's world.
// Without this, a bot mid-handoff is still a Replica when the serializer
// scans (Replicas are excluded), and PostSystems' later promote fires on a
// cell that's about to be torn down — the bot leaks.
//
// MERGE drains EVERY queued promote, ignoring commitTick. The donor is
// guaranteed to be torn down by the merge commit, so any in-flight handoff
// targeting this donor would otherwise lose its blob. Cluster-tick skew
// between source and dest hosts can leave commitTick on the dest side
// numerically ahead of the dest's currentClusterTick (source fired demote
// already, dest hasn't reached commitTick yet) — `<=` gating would skip
// those entries. Survivor populate dedups by netID, so a sibling donor
// that ALSO ships the same entity is harmless.
//
// SPLIT and MIGRATE drain only DUE promotes (commitTick <= currentClusterTick).
// Force-draining future-commit promotes would create duplicate Lives across
// the unrelated source-of-handoff (which is not a donor and continues to
// hold its Live), since neither split-children nor migrate-target run any
// netID dedup against outside cells.
//
// Must be called from the cell's game loop goroutine.
func flushDuePromotesForCommit(c *Cell, kind CellTransferKind) {
	var clusterTick uint64
	if kind == CellTransferMerge {
		// Drain all queued promotes — any commitTick <= MaxUint64 fires.
		clusterTick = ^uint64(0)
	} else if cc := c.Stage.clusterClock; cc != nil {
		clusterTick = cc.ClusterTick(c.Engine.TickIntervalMs())
	} else {
		clusterTick = uint64(c.Engine.Tick)
	}
	c.drainPendingPromotes(clusterTick)
}

// drainDonorResidualsToSurvivor walks each donor cell after the merge
// commit's initial serialize ran, finds any entities that arrived after
// the snapshot (via in-flight cross-sibling handoffs that landed during
// the commit window), and ships them into the survivor. Without this
// drain those entities die with the donor when it shuts down.
//
// Each donor's serialize and the survivor's populate run on their
// respective game loops via RunOnLoop, so reads and writes are
// race-free with their own systems. Two passes is enough in practice
// because by the second pass the donors have stopped receiving new
// arrivals (the orchestrator's handoff-dest-gone protection prevents
// further sends to deleted donors, and the first pass shipped
// everything that was in flight when the commit fired).
//
// Best-effort: if a donor's loop is unavailable or the scheduling deadline
// expires, we skip and accept the (now small) loss. Logged at
// CatMeshCell so it can be triaged if it persists in production.
func (c *Process) drainDonorResidualsToSurvivor(donors []*Cell, survivor *Cell) {
	for _, d := range donors {
		// Phase 1: serialize residuals on the donor's own game loop.
		var data [][]byte
		srcCtx, srcCancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		serr := d.Engine.RunOnLoop(srcCtx, func() error {
			// Same race fix as the initial Execute path: a pending promote
			// targeting this donor would be skipped by serializeAllEntities
			// (Replicas are excluded) and then evaporate when the donor is
			// torn down. Promote inline before scanning. CellTransferMerge
			// drains all queued promotes (cluster-tick-skew-tolerant) since
			// this is the merge residual-drain pass.
			flushDuePromotesForCommit(d, CellTransferMerge)
			var err error
			data, err = serializeAllEntities(d)
			return err
		})
		srcCancel()
		if serr != nil {
			if errors.Is(serr, context.DeadlineExceeded) {
				c.Log.Log(CatMeshCell, "merge drain: serialize timeout on %s", d.MeshID())
			} else {
				c.Log.Log(CatMeshCell, "merge drain: serialize %s: %v", d.MeshID(), serr)
			}
			continue
		}
		if len(data) == 0 {
			continue
		}
		// Phase 2: populate residuals into the survivor on its own game
		// loop. Skip any blob whose netID is already present on the
		// survivor — those were shipped once by the executor's initial
		// populate and re-spawning them would duplicate the entity (same
		// netID, two live ECS rows). True residuals (entities that
		// arrived on the donor after the executor's snapshot) still go
		// through because their netID isn't in survivor yet.
		destCtx, destCancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		var rescued int
		perr := survivor.Engine.RunOnLoop(destCtx, func() error {
			existing := make(map[uint32]struct{})
			netIDMap := survivor.Stage.NetworkIDMap()
			filter := ecs.NewFilter1[component.NetworkID](survivor.Engine.ECS)
			func() {
				q := filter.Query()
				defer q.Close()
				for q.Next() {
					e := q.Entity()
					if !netIDMap.HasAll(e) {
						continue
					}
					existing[netIDMap.Get(e).ID] = struct{}{}
				}
			}()
			for _, blob := range data {
				frame, err := UnmarshalTransferFrame(blob)
				if err != nil {
					return err
				}
				if _, dup := existing[frame.NetworkID]; dup {
					continue
				}
				if _, _, err := spawnCellTransferLive(survivor.Stage, frame); err != nil {
					return err
				}
				existing[frame.NetworkID] = struct{}{}
				rescued++
			}
			return nil
		})
		destCancel()
		if perr != nil {
			if errors.Is(perr, context.DeadlineExceeded) {
				c.Log.Log(CatMeshCell, "merge drain: populate timeout for %d residuals from %s", len(data), d.MeshID())
			} else {
				c.Log.Log(CatMeshCell, "merge drain: populate residuals from %s: %v", d.MeshID(), perr)
			}
			continue
		}
		if rescued > 0 {
			c.Log.Log(CatMeshCell, "merge drain: rescued %d true residuals from %s (skipped %d already on survivor)",
				rescued, d.MeshID(), len(data)-rescued)
		}
	}
}

// serializeAllEntities runs on the source cell's game loop and serializes
// every non-ghost, non-replica entity. Used for MERGE and MIGRATE.
func serializeAllEntities(src *Cell) ([][]byte, error) {
	// Commit-path serializer: Ghost and Replica excluded — only
	// serialize authoritative entities belonging to this cell.
	filter := ecs.NewFilter1[component.Position](src.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())

	var out [][]byte
	query := filter.Query()
	// defer Close() so the world unlocks even if SerializeEntity panics on
	// game-specific Scan code. Close is idempotent in ark v0.7.1, so calling
	// it again from the err branch (or after natural iteration) is a no-op.
	// Without this, a panic in the loop body propagates up through RunOnLoop,
	// gets recovered by the loop's processAdminCmds, and leaves the world's
	// lock counter pinned > 0 — every subsequent NewEntity on this cell
	// (e.g. drainPendingPromotes at PostSystems) panics with "world locked".
	defer query.Close()
	for query.Next() {
		entity := query.Entity()
		data, err := src.Stage.SerializeEntity(entity)
		if err != nil {
			return nil, fmt.Errorf("serialize entity: %w", err)
		}
		out = append(out, data)
	}
	return out, nil
}

// serializeEntitylessSessions picks up player sessions that don't have a
// live entity (docked, dead) and encodes them as JSON-packed SessionTransfer
// records. Runs on the source cell's game loop.
func serializeEntitylessSessions(src *Cell) ([][]byte, error) {
	var out [][]byte
	for _, sess := range src.Engine.Players.AllSessions() {
		if sess.ConnID == 0 {
			continue
		}
		if sess.State == engine.StatePending || sess.State == engine.StateTransferring {
			continue
		}
		if sess.Entity != (ecs.Entity{}) && src.Engine.ECS.Alive(sess.Entity) {
			continue
		}
		st := SessionTransfer{
			ConnID:           sess.ConnID,
			UserID:           sess.UserID,
			Username:         sess.Username,
			StreamGeneration: sess.StreamGeneration + 1,
			StateTag:         src.Engine.Players.StateName(sess.State),
			// Data is opaque []byte by convention. The auth-service rollout
			// removed PlayerSession.Data; games that need to round-trip
			// per-session game state through cell transfers must stash it
			// in their own side maps and rebuild on the destination cell.
			Data: nil,
		}
		// A node-local connID has no meaning on another host. Resolve it back
		// through the source VCM and carry the stable composite SessionKey.
		// Keep the route epoch distinct from StreamGeneration: the former
		// fences gateway↔host routing and advances at topology commit, while
		// the latter scopes client replication ordering and advances on the
		// transferred copy here.
		if src.Stage != nil && src.Stage.coord != nil && src.Stage.coord.vcm != nil {
			key, epoch, ok := src.Stage.coord.vcm.LookupRouteByLocal(sess.ConnID)
			if !ok {
				return nil, fmt.Errorf("serialize entity-less session %q: local connID %d has no VCM SessionKey", sess.Username, sess.ConnID)
			}
			st.GatewayID = key.GatewayID
			st.GatewayConnID = key.ConnID
			st.SessionEpoch = epoch
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return nil, fmt.Errorf("serialize entity-less session %q: %w", sess.Username, err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// packRecords encodes a slice of variable-length records into a single
// length-prefixed blob. Format: [4:count][4:len|data]*. Returns nil for an
// empty input so proto omits the field on the wire.
func packRecords(records [][]byte) []byte {
	if len(records) == 0 {
		return nil
	}
	total := 4
	for _, r := range records {
		total += 4 + len(r)
	}
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(records)))
	off := 4
	for _, r := range records {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(len(r)))
		off += 4
		copy(buf[off:off+len(r)], r)
		off += len(r)
	}
	return buf
}

// unpackRecords is the inverse of packRecords. Returns an empty slice for a
// nil or empty input.
func unpackRecords(buf []byte) ([][]byte, error) {
	if len(buf) == 0 {
		return nil, nil
	}
	if len(buf) < 4 {
		return nil, fmt.Errorf("unpackRecords: short header (%d bytes)", len(buf))
	}
	count := binary.BigEndian.Uint32(buf[0:4])
	// A record consumes at least its four-byte length prefix. Validate before
	// using an untrusted count as allocation capacity.
	if uint64(count) > uint64((len(buf)-4)/4) {
		return nil, fmt.Errorf("unpackRecords: count %d exceeds remaining payload", count)
	}
	out := make([][]byte, 0, count)
	off := 4
	for i := uint32(0); i < count; i++ {
		if off+4 > len(buf) {
			return nil, fmt.Errorf("unpackRecords: truncated length at record %d", i)
		}
		n := binary.BigEndian.Uint32(buf[off : off+4])
		off += 4
		if off+int(n) > len(buf) {
			return nil, fmt.Errorf("unpackRecords: truncated data at record %d (want %d)", i, n)
		}
		rec := make([]byte, n)
		copy(rec, buf[off:off+int(n)])
		off += int(n)
		out = append(out, rec)
	}
	return out, nil
}

// toProtoKind maps the Go-native CellTransferKind to the meshpb enum.
func toProtoKind(k CellTransferKind) meshpb.CellTransferKind {
	switch k {
	case CellTransferSplit:
		return meshpb.CellTransferKind_CELL_TRANSFER_SPLIT
	case CellTransferMerge:
		return meshpb.CellTransferKind_CELL_TRANSFER_MERGE
	case CellTransferMigrate:
		return meshpb.CellTransferKind_CELL_TRANSFER_MIGRATE
	default:
		return meshpb.CellTransferKind_CELL_TRANSFER_UNSPECIFIED
	}
}

// fromProtoKind is the inverse of toProtoKind. Used when inbound control
// messages carry a CellTransfer that the remote-source Execute path needs
// to turn back into a Go command.
func fromProtoKind(k meshpb.CellTransferKind) CellTransferKind {
	switch k {
	case meshpb.CellTransferKind_CELL_TRANSFER_SPLIT:
		return CellTransferSplit
	case meshpb.CellTransferKind_CELL_TRANSFER_MERGE:
		return CellTransferMerge
	case meshpb.CellTransferKind_CELL_TRANSFER_MIGRATE:
		return CellTransferMigrate
	default:
		return CellTransferUnspecified
	}
}

// commandFromProto converts an inbound CoordMessage.CellTransfer proto back
// into a cellTransferCommand that a remote source host's executor can
// Execute directly. Preserves every field including Quadrant.
func commandFromProto(proto *meshpb.CellTransfer, srcHostID string) cellTransferCommand {
	return cellTransferCommand{
		RequestID:  proto.RequestId,
		Kind:       fromProtoKind(proto.Kind),
		SrcCellID:  MeshCellID(proto.SrcCellId),
		DestCellID: MeshCellID(proto.DestCellId),
		SrcHostID:  srcHostID,
		DestHostID: proto.DestHostId,
		Quadrant:   proto.Quadrant,
	}
}
