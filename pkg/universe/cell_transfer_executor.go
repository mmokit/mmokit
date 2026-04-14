package universe

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
// the console's ExecOnGameLoop budget.
const executorAdminTimeout = 5 * time.Second

// ═══════════════════════════════════════════════════════════════════════════
// S7 T4 — cellTransferExecutor
//
// The executor is the host-side counterpart to the coordinator's
// cellTransferOrchestrator. It owns the "ship entities, create target cell,
// report ready" flow for SPLIT / MERGE / MIGRATE. One instance per local
// Host; wired up during Build() (see Coordinator.attachHostExecutors).
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
	coord *Coordinator
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
func newCellTransferExecutor(coord *Coordinator, host *Host) *cellTransferExecutor {
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
	// are race-free. Block on a result channel with a generous timeout.
	type payload struct {
		entities [][]byte
		sessions [][]byte
		err      error
	}
	resultCh := make(chan payload, 1)

	srcCell.Engine.PendingAdminCmds <- func() {
		var ents [][]byte
		var sess [][]byte
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
			resultCh <- payload{err: err}
			return
		}
		sess = serializeEntitylessSessions(srcCell)
		resultCh <- payload{entities: ents, sessions: sess}
	}

	var p payload
	select {
	case p = <-resultCh:
	case <-time.After(executorAdminTimeout):
		return fmt.Errorf("executor: serialize timeout on %s", cmd.SrcCellID)
	}
	if p.err != nil {
		return fmt.Errorf("executor: serialize %s on %s: %w", cmd.Kind, cmd.SrcCellID, p.err)
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
		Entities:   packRecords(p.entities),
		Sessions:   packRecords(p.sessions),
		Bounds: &meshpb.CellBounds{
			MinX: minX, MinY: minY, MaxX: maxX, MaxY: maxY,
		},
	}

	e.log.Log(CatMeshCell, "executor[%s]: Execute req=%d kind=%s src=%s dest=%s dest-host=%s ents=%d sess=%d",
		e.host.ID, cmd.RequestID, cmd.Kind, cmd.SrcCellID, cmd.DestCellID, cmd.DestHostID, len(p.entities), len(p.sessions))

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
	if e.host.Network == nil {
		return fmt.Errorf("executor: dest host %q is remote but no HostNetwork on %s", destHostID, e.host.ID)
	}
	frame := &meshpb.MeshFrame{
		DestCellId: destCellID,
		Msg:        &meshpb.MeshFrame_CellTransfer{CellTransfer: proto},
	}
	return e.host.Network.SendReliable(destHostID, frame)
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
		e.reportReady(proto, false, fmt.Sprintf("parse dest cell: %v", err))
		return err
	}

	// Idempotency: if the cell already exists (e.g. duplicate delivery), we
	// don't recreate it; we just report Ready. Either the orchestrator has
	// already committed and we're seeing a late retry, or the cell is live
	// from a prior iteration — both cases want ok=true.
	//
	// TODO(S7): this short-circuit is correct for split + retry delivery, but
	// for MERGE it drops the incoming donor entities on the floor — the merge
	// survivor's cell already exists on the target host (same ID as one of
	// the siblings), and this branch acks OK without populating the new
	// entities into it. TestS7MergeAcrossHosts file-header comment tracks the
	// gap. The fix: for kind=MERGE, fall through to a dedicated "populate
	// into existing cell" path that runs populateCell against the live cell
	// instead of creating a new one. Not trivial because the merge commit
	// path also renames the survivor from sibling ID to parent ID, so the
	// populate has to race correctly with the rename. Follow-up task.
	if existing := e.host.CellByID(proto.DestCellId); existing != nil {
		e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d cell %s already present — ack only",
			e.host.ID, proto.RequestId, proto.DestCellId)
		e.reportReady(proto, true, "")
		return nil
	}

	spatialCellSize := e.coord.resolveSpatialCellSize()

	e.coord.mu.Lock()
	// createNode self-registers the cell in coord.Cells / coord.CellOwner
	// under the coord lock.
	node, systems := e.coord.createNode(destCellID, spatialCellSize, true /*fromSplit*/)
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

	// Populate on the new cell's game loop. Block on a result channel.
	populateDone := make(chan error, 1)
	node.Engine.PendingAdminCmds <- func() {
		err := populateCell(node, proto)
		populateDone <- err
	}

	select {
	case perr := <-populateDone:
		if perr != nil {
			e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d populate failed: %v — aborting",
				e.host.ID, proto.RequestId, perr)
			e.teardownPending(proto.RequestId)
			e.reportReady(proto, false, perr.Error())
			return perr
		}
	case <-time.After(executorAdminTimeout):
		e.teardownPending(proto.RequestId)
		err := fmt.Errorf("executor: populate timeout on %s", proto.DestCellId)
		e.reportReady(proto, false, err.Error())
		return err
	}

	// The pending entry stays in e.pending until an explicit Commit or
	// Abort fires. For now (no Commit message — the orchestrator's commit
	// step only mutates cellToHostMap), we can safely forget the pending
	// record on successful Ready: once the orchestrator commits the
	// topology change, an Abort for this request_id will never come.
	e.mu.Lock()
	delete(e.pending, proto.RequestId)
	e.mu.Unlock()

	e.log.Log(CatMeshCell, "executor[%s]: Receive req=%d kind=%v dest=%s OK",
		e.host.ID, proto.RequestId, proto.Kind, proto.DestCellId)
	e.reportReady(proto, true, "")
	return nil
}

// populateCell unpacks entities + sessions from the proto and materializes
// them on the cell's game loop. Runs entirely synchronously on the game
// loop goroutine; safe to call ECS APIs without additional locking.
func populateCell(cell *Cell, proto *meshpb.CellTransfer) error {
	entBlobs, err := unpackRecords(proto.Entities)
	if err != nil {
		return fmt.Errorf("unpack entities: %w", err)
	}
	for i, blob := range entBlobs {
		if _, _, err := cell.Base.SpawnFromTransfer(blob); err != nil {
			return fmt.Errorf("spawn entity %d: %w", i, err)
		}
	}

	sessBlobs, err := unpackRecords(proto.Sessions)
	if err != nil {
		return fmt.Errorf("unpack sessions: %w", err)
	}
	for i, blob := range sessBlobs {
		var st SessionTransfer
		if err := json.Unmarshal(blob, &st); err != nil {
			return fmt.Errorf("decode session %d: %w", i, err)
		}
		if st.ConnID == 0 {
			continue
		}
		cell.Engine.Players.RegisterSessionTransfer(st.ConnID, st.Username, st.StateTag, st.Data)
	}
	return nil
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
func (e *cellTransferExecutor) reportReady(proto *meshpb.CellTransfer, ok bool, errMsg string) {
	// Local fast-path: the orchestrator lives in this process.
	if e.coord.orchestrator != nil && e.coord.controlClient == nil {
		e.coord.orchestrator.OnReady(proto.RequestId, proto.DestCellId, e.host.ID, ok, errMsg)
		return
	}
	// Remote: send up the node's control stream to the coordinator.
	if e.coord.controlClient != nil {
		msg := &meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellTransferReady{
				CellTransferReady: &meshpb.CellTransferReady{
					RequestId:  proto.RequestId,
					DestCellId: proto.DestCellId,
					HostId:     e.host.ID,
					Ok:         ok,
					Error:      errMsg,
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
// NewCoordinator.
type cellTransferDispatcherImpl struct {
	coord *Coordinator
}

func newCellTransferDispatcher(coord *Coordinator) *cellTransferDispatcherImpl {
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
	filter := ecs.NewFilter1[component.Position](src.Engine.ECS).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica]())

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
	}
	return out, nil
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
