package universe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// gracefulLeaveDrainTimeout bounds how long the coordinator spends migrating
// a leaving host's cells before it gives up and sends CellsDrained anyway.
// The leaving node's Shutdown has a slightly larger timeout so the coord
// wins the race and surfaces a meaningful log before the node moves on.
const gracefulLeaveDrainTimeout = 30 * time.Second

// recvResult holds the result of a single stream.Recv() call.
type recvResult struct {
	msg *meshpb.HostMessage
	err error
}

// meshControlServer implements meshpb.MeshControl. It accepts bidi
// streams from remote nodes and gateway processes, dispatches inbound
// HostMessage variants based on the first message type:
//   - RegisterHost  → handleHostControl (node path, unchanged from S4)
//   - RegisterGateway → handleGatewayControl (new gateway path, T4+)
//
// Host streams are tracked in streams/streamMu/streamKill.
// Gateway streams are tracked in gatewayStreams/gatewayMu/gatewayKill.
// One instance per coordinator process.
type meshControlServer struct {
	meshpb.UnimplementedMeshControlServer // forward-compat
	coord           *Coordinator
	log             *logger.Logger
	registry        *HostRegistry
	gatewayRegistry *GatewayRegistry
	engine          *assignmentEngine

	mu         sync.RWMutex
	streams    map[string]meshpb.MeshControl_ControlServer // hostID -> stream
	streamMu   map[string]*sync.Mutex                      // per-stream send mutex
	streamKill map[string]chan struct{}                     // hostID -> kill signal from `host kill` cmd

	gatewayMu      map[string]*sync.Mutex                      // per-gateway send mutex
	gatewayStreams  map[string]meshpb.MeshControl_ControlServer // gatewayID -> stream
	gatewayKill    map[string]chan struct{}                     // gatewayID -> kill signal
}

// Control is the bidi streaming RPC entry point. Dispatches on the first
// message variant: RegisterHost opens a node control stream; RegisterGateway
// opens a gateway control stream. Any other first message is rejected.
func (s *meshControlServer) Control(stream meshpb.MeshControl_ControlServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	switch v := first.Msg.(type) {
	case *meshpb.HostMessage_Register:
		return s.handleHostControl(stream, v.Register)
	case *meshpb.HostMessage_RegisterGateway:
		return s.handleGatewayControl(stream, v.RegisterGateway)
	default:
		return fmt.Errorf("mesh control: first message must be RegisterHost or RegisterGateway, got %T", first.Msg)
	}
}

// handleHostControl manages a node's bidi control stream. This is the
// existing host path extracted from the old Control method; behaviour is
// unchanged.
func (s *meshControlServer) handleHostControl(stream meshpb.MeshControl_ControlServer, reg *meshpb.RegisterHost) error {
	hostID := reg.HostId

	kill := make(chan struct{})
	s.mu.Lock()
	if _, exists := s.streams[hostID]; exists {
		s.log.Log(CatMeshCell, "coordinator: host %s replacing stale control stream", hostID)
	}
	s.streams[hostID] = stream
	s.streamMu[hostID] = &sync.Mutex{}
	s.streamKill[hostID] = kill
	s.mu.Unlock()

	// Insert into HostRegistry and notify the assignment engine.
	host := s.registry.Register(hostID, reg.GrpcAddr)

	// Send RegisterAck immediately so the node knows its registration was
	// accepted. Carries the current coord epoch so the node can fence out
	// stale state from a previous coordinator instance.
	ack := &meshpb.CoordMessage{
		CoordEpoch: s.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_RegisterAck{
			RegisterAck: &meshpb.RegisterAck{Ok: true},
		},
	}
	if err := s.sendCoordMessageToHost(hostID, ack); err != nil {
		s.log.Log(CatMeshCell, "coordinator: RegisterAck to %s failed: %v", hostID, err)
		return err
	}

	if s.engine != nil {
		s.engine.onHostRegistered(host)
		// Send an initial PeerList so the new host knows about its peers
		// (and their cell ownership) before the settle window closes and
		// the first real rebalance runs. Targeted send to this one host.
		if initial := s.engine.buildPeerList(); initial != nil {
			if err := s.sendCoordMessageToHost(hostID, initial); err != nil {
				s.log.Log(CatMeshCell, "coordinator: initial PeerList to %s failed: %v", hostID, err)
			}
		}
	}

	s.log.Log(CatMeshCell, "coordinator: host %s registered from %s (epoch=%d)", hostID, reg.GrpcAddr, s.coord.coordEpoch)

	defer func() {
		s.mu.Lock()
		delete(s.streams, hostID)
		delete(s.streamMu, hostID)
		delete(s.streamKill, hostID)
		s.mu.Unlock()

		if s.registry == nil {
			return
		}
		host := s.registry.Get(hostID)
		if host == nil {
			return
		}

		if len(host.OwnedCells) == 0 {
			// Graceful leave: the node reported every owned cell as
			// stopped before closing the stream. No reassignment needed.
			s.log.Log(CatMeshCell, "coordinator: host %s graceful leave — removing entry", hostID)
			s.registry.MarkLeaving(hostID)
			s.registry.Remove(hostID)
			return
		}

		// Stream closed with cells still owned. Treat as crash: mark
		// the host Dead and let the liveness watcher's reassignment
		// path pick up the orphaned cells on its next tick.
		s.log.Log(CatMeshCell, "coordinator: host %s stream closed with %d owned cells — treating as crash", hostID, len(host.OwnedCells))
		s.registry.MarkDead(hostID)
		// The liveness watcher wakes every 500ms; its next tick will
		// see the Dead state and reassign. But for faster recovery on
		// stream close we trigger reassignment inline via the engine.
		if s.engine != nil {
			s.engine.reassignOrphanedCells(host)
		}
	}()

	// Drain subsequent messages until EOF, error, or admin kill signal.
	// Recv runs in a goroutine so the main loop can also select on the
	// kill channel (populated by `host kill` console command).
	recvCh := make(chan recvResult, 1)
	recvOne := func() {
		msg, err := stream.Recv()
		recvCh <- recvResult{msg: msg, err: err}
	}
	go recvOne()

	for {
		select {
		case r := <-recvCh:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					s.log.Log(CatMeshCell, "coordinator: host %s stream closed", hostID)
					return nil
				}
				s.log.Log(CatMeshCell, "coordinator: host %s recv error: %v", hostID, r.err)
				return r.err
			}
			msg := r.msg
			switch v := msg.Msg.(type) {
			case *meshpb.HostMessage_CellReady:
				ready := v.CellReady
				if ready != nil {
					if s.registry != nil {
						_ = s.registry.AssignCell(ready.HostId, ready.CellId)
					}
					s.log.Log(CatMeshCell, "coordinator: host %s reports cell %s READY", ready.HostId, ready.CellId)
				}

			case *meshpb.HostMessage_CellStopped:
				stopped := v.CellStopped
				if stopped != nil {
					if s.registry != nil {
						s.registry.ReleaseCell(stopped.HostId, stopped.CellId)
					}
					s.log.Log(CatMeshCell, "coordinator: host %s reports cell %s STOPPED", stopped.HostId, stopped.CellId)
				}

			case *meshpb.HostMessage_Heartbeat:
				hb := v.Heartbeat
				if hb != nil && s.registry != nil {
					s.registry.Touch(hb.HostId)
				}

			case *meshpb.HostMessage_PlayerMigrated:
				pm := v.PlayerMigrated
				if pm != nil {
					s.coord.notifyPlayerMigrated(pm.GatewayId, pm.ConnId, pm.FromHostId, pm.ToHostId, pm.ToCellId)
				}

			case *meshpb.HostMessage_CellTransferReady:
				ready := v.CellTransferReady
				if ready != nil && s.coord.orchestrator != nil {
					s.coord.orchestrator.OnReady(ready.RequestId, ready.DestCellId, ready.HostId, ready.Ok, ready.Error)
				}

			case *meshpb.HostMessage_GracefulLeave:
				gl := v.GracefulLeave
				if gl != nil {
					// S7-T7: drain every cell owned by the leaving host by
					// migrating to a surviving host, then reply with
					// CellsDrained so the node's Shutdown can proceed.
					// Runs in a goroutine so the drain loop keeps servicing
					// CellTransferReady messages from this same host as the
					// migrations progress — the source host for every
					// migration IS the leaving node, and BeginMigrate
					// dispatches CellTransfer commands back through this
					// very control stream.
					leavingID := gl.HostId
					s.log.Log(CatMeshCell, "coordinator: host %s requested GracefulLeave", leavingID)
					go s.handleGracefulLeave(leavingID)
				}

			default:
				s.log.Log(CatMeshMsg, "coordinator: host %s sent %T", hostID, msg.Msg)
			}
			go recvOne() // queue the next Recv

		case <-kill:
			s.log.Log(CatMeshCell, "coordinator: host %s stream killed by admin", hostID)
			// The leaked recvOne goroutine will unblock with an error once
			// gRPC tears down the stream; its buffered result is discarded.
			return fmt.Errorf("stream killed by admin")
		}
	}
}

// handleGracefulLeave runs the coordinator side of a node's S7-T7 graceful
// leave: drain every cell owned by leavingID via BeginMigrate, then reply
// with CellsDrained on the node's control stream so it can finish shutdown.
// Runs in a goroutine so the caller (the handleHostControl drain loop)
// keeps servicing CellTransferReady messages from the same leaving host
// while the migrations progress — every BeginMigrate dispatches a
// CellTransfer command to the leaving node, which reports Ready back
// through the exact stream we must keep alive.
//
// The ack is sent unconditionally once drainHost returns (or its timeout
// fires). A half-drained state is strictly better than hanging the node's
// Shutdown forever — the leaving node is exiting regardless.
func (s *meshControlServer) handleGracefulLeave(leavingID string) {
	ctx, cancel := context.WithTimeout(context.Background(), gracefulLeaveDrainTimeout)
	defer cancel()
	if err := s.coord.drainHost(ctx, leavingID); err != nil {
		s.log.Log(CatMeshCell, "coordinator: drainHost %s: %v", leavingID, err)
	}

	// Reconcile HostRegistry bookkeeping with the post-drain reality.
	// The orchestrator's per-commit applyRegistryDelta already handled
	// every migrated cell at commit time, so by the time we reach this
	// point the leaving host's OwnedCells set should already be empty
	// for every cell that had a destination. This sweep covers cells
	// that had no destination (no surviving hosts) — they're simply
	// released so the handleHostControl defer treats the subsequent
	// EOF as a graceful leave.
	if s.registry != nil {
		host := s.registry.Get(leavingID)
		if host != nil {
			for cellID := range host.OwnedCells {
				s.registry.ReleaseCell(leavingID, cellID)
			}
		}
	}

	ack := &meshpb.CoordMessage{
		CoordEpoch: s.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CellsDrained{
			CellsDrained: &meshpb.CellsDrained{HostId: leavingID},
		},
	}
	if err := s.sendCoordMessageToHost(leavingID, ack); err != nil {
		s.log.Log(CatMeshCell, "coordinator: CellsDrained to %s failed: %v", leavingID, err)
		return
	}
	s.log.Log(CatMeshCell, "coordinator: host %s drain complete — CellsDrained sent", leavingID)
}

// handleGatewayControl manages a gateway process's bidi control stream.
// Mirrors handleHostControl but with gateway-specific semantics:
//   - inserts into gatewayStreams / gatewayMu / gatewayKill
//   - registers with GatewayRegistry (no assignment engine involvement)
//   - sends RegisterAck but no initial PeerList (wired in T5)
//   - drain loop handles Heartbeat (touch registry) and SessionAnnounce
//     (log-only in T4; real tracking lands T5)
//   - graceful-vs-crash distinction uses Sessions map emptiness
//   - dead cleanup removes sessions from sessionRoutes via RemoveByGateway
func (s *meshControlServer) handleGatewayControl(stream meshpb.MeshControl_ControlServer, reg *meshpb.RegisterGateway) error {
	gatewayID := reg.GatewayId

	kill := make(chan struct{})
	s.mu.Lock()
	if _, exists := s.gatewayStreams[gatewayID]; exists {
		s.log.Log(CatMeshCell, "coordinator: gateway %s replacing stale control stream", gatewayID)
	}
	s.gatewayStreams[gatewayID] = stream
	s.gatewayMu[gatewayID] = &sync.Mutex{}
	s.gatewayKill[gatewayID] = kill
	s.mu.Unlock()

	// Register in GatewayRegistry.
	s.gatewayRegistry.Register(gatewayID, reg.WsAddr, reg.GrpcAddr)

	// Send RegisterAck so the gateway knows its registration was accepted.
	ack := &meshpb.CoordMessage{
		CoordEpoch: s.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_RegisterAck{
			RegisterAck: &meshpb.RegisterAck{Ok: true},
		},
	}
	if err := s.sendCoordMessageToGateway(gatewayID, ack); err != nil {
		s.log.Log(CatMeshCell, "coordinator: RegisterAck to gateway %s failed: %v", gatewayID, err)
		return err
	}

	// No initial PeerList for gateways in T4 — wired in T5 once the gateway
	// has a topology cache to consume it.

	s.log.Log(CatMeshCell, "coordinator: gateway %s registered ws=%s grpc=%s (epoch=%d)", gatewayID, reg.WsAddr, reg.GrpcAddr, s.coord.coordEpoch)

	// Send an initial PeerList so the gateway's cachedTopology is populated
	// immediately. Without this the gateway only learns about nodes when the
	// next broadcastPeerList fires (after a rebalance), which may never happen
	// if all nodes are already settled.
	if s.engine != nil {
		if initial := s.engine.buildPeerList(); initial != nil {
			if err := s.sendCoordMessageToGateway(gatewayID, initial); err != nil {
				s.log.Log(CatMeshCell, "coordinator: initial PeerList to gateway %s failed: %v", gatewayID, err)
			}
		}
	}

	// Broadcast the updated PeerList (now including this gateway) to all live
	// nodes so they open MeshData streams back to the gateway.
	if s.engine != nil {
		s.engine.broadcastPeerList()
	}

	defer func() {
		s.mu.Lock()
		delete(s.gatewayStreams, gatewayID)
		delete(s.gatewayMu, gatewayID)
		delete(s.gatewayKill, gatewayID)
		s.mu.Unlock()

		if s.gatewayRegistry == nil {
			return
		}
		gw := s.gatewayRegistry.Get(gatewayID)
		if gw == nil {
			return
		}

		if len(gw.Sessions) == 0 {
			// Graceful leave: no live sessions when the stream closed.
			s.log.Log(CatMeshCell, "coordinator: gateway %s graceful leave — removing entry", gatewayID)
			s.gatewayRegistry.MarkLeaving(gatewayID)
			s.gatewayRegistry.Remove(gatewayID)
			return
		}

		// Stream closed with sessions still tracked. Treat as crash: mark
		// the gateway Dead first (so checkGatewayLiveness cannot observe a
		// Live gateway while sessions are mid-cleanup), then remove routes.
		s.gatewayRegistry.MarkDead(gatewayID)
		n := s.coord.sessionRoutes.RemoveByGateway(gatewayID)
		s.log.Log(CatMeshCell, "coordinator: gateway %s stream closed with %d sessions — treating as crash, cleaned %d routes", gatewayID, len(gw.Sessions), n)
	}()

	recvCh := make(chan recvResult, 1)
	recvOne := func() {
		msg, err := stream.Recv()
		recvCh <- recvResult{msg: msg, err: err}
	}
	go recvOne()

	for {
		select {
		case r := <-recvCh:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					s.log.Log(CatMeshCell, "coordinator: gateway %s stream closed", gatewayID)
					return nil
				}
				s.log.Log(CatMeshCell, "coordinator: gateway %s recv error: %v", gatewayID, r.err)
				return r.err
			}
			msg := r.msg
			switch v := msg.Msg.(type) {
			case *meshpb.HostMessage_Heartbeat:
				hb := v.Heartbeat
				if hb != nil {
					s.gatewayRegistry.Touch(gatewayID)
				}

			case *meshpb.HostMessage_SessionAnnounce:
				sa := v.SessionAnnounce
				if sa != nil {
					key := SessionKey{GatewayID: sa.GatewayId, ConnID: sa.ConnId}
					if sa.TargetHostId == "" {
						// Tombstone: gateway is removing a session (clean disconnect).
						s.log.Log(CatMeshCell, "coordinator: gateway %s removes session %s:%d user=%s",
							gatewayID, sa.GatewayId, sa.ConnId, sa.Username)
						s.coord.sessionRoutes.Remove(key)
						s.gatewayRegistry.RemoveSession(gatewayID, key)
					} else {
						// New session announcement.
						s.log.Log(CatMeshCell, "coordinator: gateway %s announces session %s:%d user=%s target=%s/%s",
							gatewayID, sa.GatewayId, sa.ConnId, sa.Username, sa.TargetHostId, sa.TargetCellId)
						// Populate the coordinator's routing table so notifyPlayerMigrated
						// and the disconnect cleanup path can find the session.
						s.coord.sessionRoutes.Set(&SessionRoute{
							Key:      key,
							Username: sa.Username,
							HostID:   sa.TargetHostId,
							CellID:   sa.TargetCellId,
							Epoch:    1,
						})
						// Track the session on the RemoteGateway entry for crash cleanup.
						s.gatewayRegistry.AddSession(gatewayID, key)
					}
				}

			case *meshpb.HostMessage_PlayerMigrated:
				// PlayerMigrated is sent by nodes, not gateways. Log as protocol error.
				pm := v.PlayerMigrated
				if pm != nil {
					s.log.Log(CatMeshCell, "coordinator: gateway %s sent unexpected PlayerMigrated for %s:%d (protocol error — expected from nodes)",
						gatewayID, pm.GatewayId, pm.ConnId)
				}

			default:
				s.log.Log(CatMeshMsg, "coordinator: gateway %s sent %T", gatewayID, msg.Msg)
			}
			go recvOne()

		case <-kill:
			s.log.Log(CatMeshCell, "coordinator: gateway %s stream killed by admin", gatewayID)
			return fmt.Errorf("stream killed by admin")
		}
	}
}

// cancelStream force-closes the control stream for the given host,
// simulating a crash. Used by the admin console `host kill` command.
// Closes the per-host kill channel which the Control recv loop selects on.
// Returns true if a stream was found and cancelled.
func (s *meshControlServer) cancelStream(hostID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kill, ok := s.streamKill[hostID]
	if !ok {
		return false
	}
	// Close idempotently: check whether the channel is already closed
	// by attempting a non-blocking receive. If it drains (already closed),
	// do nothing; otherwise close it.
	select {
	case <-kill:
		// already closed — nothing to do
	default:
		close(kill)
	}
	return true
}

// sendCoordMessageToHost pushes a CoordMessage onto the given host's control
// stream. Returns an error if there is no stream for the host or the
// Send call fails. Uses a per-stream mutex because grpc-go server
// streams are not safe for concurrent Send.
func (s *meshControlServer) sendCoordMessageToHost(hostID string, msg *meshpb.CoordMessage) error {
	s.mu.RLock()
	stream := s.streams[hostID]
	smu := s.streamMu[hostID]
	s.mu.RUnlock()
	if stream == nil || smu == nil {
		return fmt.Errorf("no control stream for host %q", hostID)
	}
	smu.Lock()
	defer smu.Unlock()
	return stream.Send(msg)
}

// sendCoordMessageToGateway pushes a CoordMessage onto the given gateway's
// control stream. Returns an error if there is no stream for the gateway or
// the Send call fails. Uses a per-stream mutex because grpc-go server streams
// are not safe for concurrent Send.
func (s *meshControlServer) sendCoordMessageToGateway(gatewayID string, msg *meshpb.CoordMessage) error {
	s.mu.RLock()
	stream := s.gatewayStreams[gatewayID]
	smu := s.gatewayMu[gatewayID]
	s.mu.RUnlock()
	if stream == nil || smu == nil {
		return fmt.Errorf("no control stream for gateway %q", gatewayID)
	}
	smu.Lock()
	defer smu.Unlock()
	return stream.Send(msg)
}
