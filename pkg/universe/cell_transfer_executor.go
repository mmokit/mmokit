package universe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
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
// Entity records are TransferFrame bytes produced by WorldBase.SerializeEntity.
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

	mu      sync.Mutex
	pending map[uint64]*pendingReceive
}

// pendingReceive tracks a cell that was just created on this host in response
// to a RECEIVE but has not yet been committed by the orchestrator. If the
// orchestrator rolls back (Abort), we need to tear the cell down.
type pendingReceive struct {
	requestID uint64
	cellID    CellID
	cellKey   string
	cell      *Cell
}

// newCellTransferExecutor builds an executor for the given host.
func newCellTransferExecutor(coord *Process, host *Host) *cellTransferExecutor {
	return &cellTransferExecutor{
		coord:   coord,
		host:    host,
		log:     coord.Log,
		pending: make(map[uint64]*pendingReceive),
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
	ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
	defer cancel()
	runErr := srcCell.Engine.RunOnLoop(ctx, func() error {
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
		sess = serializeEntitylessSessions(srcCell)
		return nil
	})
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			return fmt.Errorf("executor: serialize timeout on %s", cmd.SrcCellID)
		}
		return fmt.Errorf("executor: serialize %s on %s: %w", cmd.Kind, cmd.SrcCellID, runErr)
	}

	// Compute world-space bounds for the destination cell.
	destCell, err := ParseCellID(cmd.DestCellID)
	if err != nil {
		return fmt.Errorf("executor: parse dest cell %q: %w", cmd.DestCellID, err)
	}
	minX, minY, maxX, maxY := destCell.WorldBounds(coords.CellSize)

	proto := &meshpb.CellTransfer{
		RequestId:  cmd.RequestID,
		Kind:       toProtoKind(cmd.Kind),
		SrcCellId:  cmd.SrcCellID,
		DestCellId: cmd.DestCellID,
		DestHostId: cmd.DestHostID,
		Quadrant:   cmd.Quadrant,
		Entities:   packRecords(ents),
		Sessions:   packRecords(sess),
		Bounds: &meshpb.CellBounds{
			MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY,
		},
	}

	e.log.Log(CatMeshCell, "executor[%s]: Execute req=%d kind=%s src=%s dest=%s dest-host=%s ents=%d sess=%d",
		e.host.ID, cmd.RequestID, cmd.Kind, cmd.SrcCellID, cmd.DestCellID, cmd.DestHostID, len(ents), len(sess))

	return e.shipToDestination(cmd.DestHostID, cmd.DestCellID, proto)
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

	existing := e.host.CellByID(proto.DestCellId)

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
		ctx, cancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		perr := existing.Engine.RunOnLoop(ctx, func() error {
			_, err := e.populateCell(existing, proto)
			return err
		})
		cancel()
		if perr != nil {
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
	e.pending[proto.RequestId] = &pendingReceive{
		requestID: proto.RequestId,
		cellID:    destCellID,
		cellKey:   proto.DestCellId,
		cell:      node,
	}
	e.mu.Unlock()

	// Two-phase init: World.Init then system Init. Mirrors Build()'s order.
	node.World.Init()
	initSystems(systems)

	// Start the game loop before enqueuing the populate closure so the
	// PendingAdminCmds drain actually runs.
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

	// The pending entry stays in e.pending until an explicit Commit or
	// Abort fires. For now (no Commit message — the orchestrator's commit
	// step only mutates cellToHostMap), we can safely forget the pending
	// record on successful Ready: once the orchestrator commits the
	// topology change, an Abort for this request_id will never come.
	e.mu.Lock()
	delete(e.pending, proto.RequestId)
	e.mu.Unlock()

	e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d kind=%v dest=%s OK (adopted %d)",
		e.host.ID, proto.RequestId, proto.Kind, proto.DestCellId, len(adoptedUsers))
	e.reportReady(proto, true, "", adoptedUsers)
	return nil
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

	var adoptedUsers []string
	for i, blob := range entBlobs {
		entity, frame, err := cell.Base.SpawnFromTransferCore(blob)
		if err != nil {
			return nil, fmt.Errorf("spawn entity %d: %w", i, err)
		}
		if frame == nil || frame.ConnID == 0 || frame.Username == "" {
			continue
		}

		// Register (or re-wire) the destination-local engine PlayerSession
		// for this migrated player. RegisterSessionTransfer skips OnEnter
		// callbacks so it won't spawn a duplicate entity — the entity we
		// just created via SpawnFromTransferCore is the one that should be
		// authoritative on this cell.
		cell.Engine.Players.RegisterSessionTransfer(frame.ConnID, frame.Username, "active", nil)
		if sess := cell.Engine.Players.ByConnID(frame.ConnID); sess != nil {
			sess.Entity = entity
		}
		adoptedUsers = append(adoptedUsers, frame.Username)

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
		cell.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
	}
	return adoptedUsers, nil
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
	e.teardownPending(proto.RequestId)
}

// teardownPending removes the pending record for the given request ID and
// shuts down the associated cell. Safe to call with an unknown request_id.
func (e *cellTransferExecutor) teardownPending(requestID uint64) {
	e.mu.Lock()
	pr, ok := e.pending[requestID]
	if ok {
		delete(e.pending, requestID)
	}
	e.mu.Unlock()

	if !ok || pr == nil {
		return
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
		e.coord.orchestrator.OnReady(proto.RequestId, proto.DestCellId, e.host.ID, ok, errMsg, adoptedUsers)
		return
	}
	// Remote: send up the node's control stream to the coordinator.
	if e.coord.controlClient != nil {
		msg := &meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellTransferReady{
				CellTransferReady: &meshpb.CellTransferReady{
					RequestId:     proto.RequestId,
					DestCellId:    proto.DestCellId,
					HostId:        e.host.ID,
					Ok:            ok,
					Error:         errMsg,
					AdoptedUsers:  adoptedUsers,
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
				SrcCellId:  cmd.SrcCellID,
				DestCellId: cmd.DestCellID,
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

// ═══════════════════════════════════════════════════════════════════════════
// Helpers: serialization, framing, kind mapping, coord hooks
// ═══════════════════════════════════════════════════════════════════════════

// serializeQuadrantEntities runs on the source cell's game loop. Walks all
// non-ghost, non-replica entities with a Position and keeps only those whose
// local coordinates fall in the requested quadrant (0=BL, 1=BR, 2=TL, 3=TR).
// The source cell's Size(coords.CellSize) determines the half-plane split.
func serializeQuadrantEntities(src *Cell, quadrant int) ([][]byte, error) {
	if quadrant < 0 || quadrant > 3 {
		return nil, fmt.Errorf("invalid quadrant %d", quadrant)
	}
	half := src.Cell.Size(coords.CellSize) / 2
	wantXi := int32(quadrant & 1)
	wantYi := int32((quadrant >> 1) & 1)

	posMap := ecs.NewMap1[component.Position](src.Engine.ECS)
	ghostMap := ecs.NewMap1[component.Ghost](src.Engine.ECS)
	filter := ecs.NewFilter1[component.Position](src.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())

	// Collect matching entities first so we can safely mutate (add Ghost)
	// after the query closes — adding components during iteration can
	// invalidate the query cursor.
	var toGhost []ecs.Entity
	var out [][]byte
	query := filter.Query()
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
		data, err := src.Base.SerializeEntity(entity)
		if err != nil {
			query.Close()
			return nil, fmt.Errorf("serialize entity: %w", err)
		}
		out = append(out, data)
		toGhost = append(toGhost, entity)
	}

	// Tag serialized entities as Ghost on the source. The child cell now
	// owns authoritative state; source's ReplicationSystem must stop
	// sending updates for these entities — otherwise the client gets
	// alternating frames from both cells and the player rubberbands.
	// PlayerViewerSource filters ghosts, so the player's own viewer
	// session naturally drops off the source → farewell fires → client
	// cleans up parent's view → child's fresh frames take over.
	for _, e := range toGhost {
		if !ghostMap.HasAll(e) {
			ghostMap.Add(e, &component.Ghost{})
		}
	}
	return out, nil
}

// drainDonorResidualsToSurvivor walks each donor cell after the merge
// commit's initial serialize ran, finds any entities that arrived after
// the snapshot (via in-flight cross-sibling handoffs that landed during
// the commit window), and ships them into the survivor. Without this
// drain those entities die with the donor when it shuts down.
//
// Each donor's serialize and the survivor's populate run on their
// respective game loops via PendingAdminCmds, so reads and writes are
// race-free with their own systems. Two passes is enough in practice
// because by the second pass the donors have stopped receiving new
// arrivals (the orchestrator's handoff-dest-gone protection prevents
// further sends to deleted donors, and the first pass shipped
// everything that was in flight when the commit fired).
//
// Best-effort: if a donor's admin queue is full or its game loop has
// already exited, we skip and accept the (now small) loss. Logged at
// CatMeshCell so it can be triaged if it persists in production.
func (c *Process) drainDonorResidualsToSurvivor(donors []*Cell, survivor *Cell) {
	for _, d := range donors {
		// Phase 1: serialize residuals on the donor's own game loop.
		var data [][]byte
		srcCtx, srcCancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		serr := d.Engine.RunOnLoop(srcCtx, func() error {
			var err error
			data, err = serializeAllEntities(d)
			return err
		})
		srcCancel()
		if serr != nil {
			if errors.Is(serr, context.DeadlineExceeded) {
				c.Log.Log(CatMeshCell, "merge drain: serialize timeout on %s", d.ID)
			} else {
				c.Log.Log(CatMeshCell, "merge drain: serialize %s: %v", d.ID, serr)
			}
			continue
		}
		if len(data) == 0 {
			continue
		}
		// Phase 2: populate residuals into the survivor on its own game loop.
		destCtx, destCancel := context.WithTimeout(context.Background(), executorAdminTimeout)
		perr := survivor.Engine.RunOnLoop(destCtx, func() error {
			for _, blob := range data {
				if _, _, err := survivor.Base.SpawnFromTransferCore(blob); err != nil {
					return err
				}
			}
			return nil
		})
		destCancel()
		if perr != nil {
			if errors.Is(perr, context.DeadlineExceeded) {
				c.Log.Log(CatMeshCell, "merge drain: populate timeout for %d residuals from %s", len(data), d.ID)
			} else {
				c.Log.Log(CatMeshCell, "merge drain: populate residuals from %s: %v", d.ID, perr)
			}
			continue
		}
		c.Log.Log(CatMeshCell, "merge drain: rescued %d entities from %s", len(data), d.ID)
	}
}

// serializeAllEntities runs on the source cell's game loop and serializes
// every non-ghost, non-replica entity. Used for MERGE and MIGRATE.
func serializeAllEntities(src *Cell) ([][]byte, error) {
	filter := ecs.NewFilter1[component.Position](src.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())

	var out [][]byte
	query := filter.Query()
	for query.Next() {
		entity := query.Entity()
		data, err := src.Base.SerializeEntity(entity)
		if err != nil {
			query.Close()
			return nil, fmt.Errorf("serialize entity: %w", err)
		}
		out = append(out, data)
	}
	return out, nil
}

// serializeEntitylessSessions picks up player sessions that don't have a
// live entity (docked, dead) and encodes them as JSON-packed SessionTransfer
// records. Runs on the source cell's game loop.
func serializeEntitylessSessions(src *Cell) [][]byte {
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
			ConnID:   sess.ConnID,
			Username: sess.Username,
			StateTag: src.Engine.Players.StateName(sess.State),
			// Data is opaque []byte by convention. If games stash a struct
			// here it won't survive JSON round-trip; the cross-process code
			// path already expects []byte.
			Data: sess.Data,
		}
		raw, err := json.Marshal(st)
		if err != nil {
			continue
		}
		out = append(out, raw)
	}
	return out
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
		SrcCellID:  proto.SrcCellId,
		DestCellID: proto.DestCellId,
		SrcHostID:  srcHostID,
		DestHostID: proto.DestHostId,
		Quadrant:   proto.Quadrant,
	}
}
