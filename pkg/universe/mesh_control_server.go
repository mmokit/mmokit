package universe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/service"
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
	coord           *Process
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

	// clusterClockSeq is the monotonic counter for CoordTimeSync
	// broadcasts. Bumped for both the initial-sync on RegisterHost and
	// the periodic broadcast loop in Task C4.
	clusterClockSeq atomic.Uint64
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
	host := s.registry.Register(hostID, reg.GrpcAddr, reg.GetHasPlayerDb(), reg.GetServiceOnly())

	if s.coord != nil && s.coord.commitLog != nil {
		s.coord.commitLog.Append(CommitEvent{
			Kind:    EventHostJoin,
			Step:    "registered",
			HostIDs: []string{hostID},
			Success: true,
		})
	}

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

	// Replication Timeline Redesign: send initial CoordTimeSync so the
	// host's ClusterClock.Observed() flips true before any CellAssign
	// can arrive. Without this, a remote host would start receiving cell
	// assignments before it has a cluster-coherent clock and would emit
	// samples stamped with local wall-time.
	if err := s.sendCoordMessageToHost(hostID, &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_CoordTimeSync{
			CoordTimeSync: &meshpb.CoordTimeSync{
				CoordTimeMs: uint64(time.Now().UnixMilli()),
				Seq:         s.clusterClockSeq.Add(1),
			},
		},
	}); err != nil {
		s.log.Log(CatMeshCell, "coordinator: CoordTimeSync send (initial) to host=%s failed: %v", hostID, err)
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
			// Service framework: clean up any service instances that lived
			// on this host. Idempotent — also handles cases where the host
			// only ran services and never owned cells.
			if s.coord != nil && s.coord.coordServices != nil {
				s.coord.coordServices.UnregisterByHost(hostID)
				if s.coord.serviceEventRouter != nil {
					s.coord.serviceEventRouter.RemoveProcess(hostID)
				}
				s.coord.broadcastPeerListOnServiceChange()
			} else if s.coord != nil && s.coord.serviceEventRouter != nil {
				// Even if no coordServices (no service framework on this
				// process) we still want to drop the routing entry so
				// publishers don't dispatch to a dead peer.
				s.coord.serviceEventRouter.RemoveProcess(hostID)
				s.coord.broadcastPeerListOnServiceChange()
			}
			if s.coord != nil && s.coord.commitLog != nil {
				s.coord.commitLog.Append(CommitEvent{
					Kind:    EventHostLeave,
					Step:    "removed",
					HostIDs: []string{hostID},
					Success: true,
					Context: map[string]string{"reason": "graceful"},
				})
			}
			return
		}

		// Stream closed with cells still owned. Treat as crash: mark
		// the host Dead and let the liveness watcher's reassignment
		// path pick up the orphaned cells on its next tick.
		s.log.Log(CatMeshCell, "coordinator: host %s stream closed with %d owned cells — treating as crash", hostID, len(host.OwnedCells))
		s.registry.MarkDead(hostID)
		// Service framework: drop any service instances + service-event
		// routing entries owned by the dead host so publishers don't keep
		// dispatching at it. Idempotent on processes without the service
		// framework wired.
		if s.coord != nil && s.coord.coordServices != nil {
			s.coord.coordServices.UnregisterByHost(hostID)
		}
		if s.coord != nil && s.coord.serviceEventRouter != nil {
			s.coord.serviceEventRouter.RemoveProcess(hostID)
			s.coord.broadcastPeerListOnServiceChange()
		}
		if s.coord != nil && s.coord.commitLog != nil {
			s.coord.commitLog.Append(CommitEvent{
				Kind:    EventHostLeave,
				Step:    "marked-dead",
				HostIDs: []string{hostID},
				Success: true,
				Context: map[string]string{"reason": "dead"},
			})
		}
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
						// Wire boundary: ready.CellId is proto string mesh form.
						_ = s.registry.AssignCell(ready.HostId, MeshCellID(ready.CellId))
					}
					s.log.Log(CatMeshCell, "coordinator: host %s reports cell %s READY", ready.HostId, ready.CellId)
				}

			case *meshpb.HostMessage_CellStopped:
				stopped := v.CellStopped
				if stopped != nil {
					if s.registry != nil {
						// Wire boundary: stopped.CellId is proto string mesh form.
						s.registry.ReleaseCell(stopped.HostId, MeshCellID(stopped.CellId))
					}
					s.log.Log(CatMeshCell, "coordinator: host %s reports cell %s STOPPED", stopped.HostId, stopped.CellId)
				}

			case *meshpb.HostMessage_Heartbeat:
				hb := v.Heartbeat
				if hb != nil && s.registry != nil {
					s.registry.Touch(hb.HostId)
					// Always call apply — even with zero reports it serves as
					// the eviction signal that this host now owns no cells.
					s.coord.applyRemoteCellMetrics(hb.HostId, hb.Metrics)
				}

			case *meshpb.HostMessage_LogBatch:
				if s.coord.remoteLogBatch != nil && v.LogBatch != nil {
					entries := make([]RemoteLogEntry, 0, len(v.LogBatch.Entries))
					for _, e := range v.LogBatch.Entries {
						entries = append(entries, RemoteLogEntry{
							HostID: hostID,
							Cat:    e.Cat,
							Msg:    e.Msg,
							TimeMs: e.TsUnixMs,
						})
					}
					s.coord.remoteLogBatch(entries)
				}

			case *meshpb.HostMessage_PlayerMigrated:
				pm := v.PlayerMigrated
				if pm != nil {
					s.coord.notifyPlayerMigrated(pm.GatewayId, pm.ConnId, pm.FromHostId, pm.ToHostId, MeshCellID(pm.ToCellId))
				}

			case *meshpb.HostMessage_CellTransferReady:
				ready := v.CellTransferReady
				if ready != nil && s.coord.orchestrator != nil {
					s.coord.orchestrator.OnReady(ready.RequestId, MeshCellID(ready.DestCellId), ready.HostId, ready.Ok, ready.Error, ready.AdoptedUsers)
				}

			case *meshpb.HostMessage_HostOpAck:
				ack := v.HostOpAck
				if ack != nil && s.coord.Control != nil {
					s.coord.Control.completePendingOp(ack.ReqId, hostOpResult{
						ok:    ack.Ok,
						error: ack.Error,
					})
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

			case *meshpb.HostMessage_CommandResponse:
				// C3: host replied to a CommandRequest we sent. Deliver to orchestrator.
				resp := v.CommandResponse
				if resp != nil && s.coord.transport != nil {
					s.coord.transport.orch.OnResponse(resp)
				}

			case *meshpb.HostMessage_CommandRequest:
				// C3: host → coord command (coord-routed verbs). Execute locally
				// and send response back on this host's stream.
				req := v.CommandRequest
				if req != nil && s.coord.dispatcher != nil {
					go s.handleInboundCommandRequest(hostID, req)
				}

			case *meshpb.HostMessage_ServiceAnnounce:
				// Service framework: a remote service-host has spun up an
				// instance. Validate against the cluster registry and re-
				// broadcast PeerList so gateways pick up the new routing.
				ann := v.ServiceAnnounce
				if ann != nil && s.coord.coordServices != nil {
					inst := service.CoordInstance{
						Kind:       ann.GetKind(),
						InstanceID: ann.GetInstanceId(),
						HostID:     ann.GetHostId(),
						OpCodes:    append([]uint32(nil), ann.GetOpCodes()...),
						JoinedAt:   time.Now(),
					}
					if err := s.coord.coordServices.Register(inst); err != nil {
						s.log.Log(CatMeshCell, "coordinator: ServiceAnnounce %q from %s rejected: %v",
							ann.GetInstanceId(), hostID, err)
					} else {
						s.log.Log(CatMeshCell, "coordinator: registered service %s/%s on %s (codes=%v)",
							inst.Kind, inst.InstanceID, inst.HostID, inst.OpCodes)
						s.coord.broadcastPeerListOnServiceChange()
					}
				}

			case *meshpb.HostMessage_ServiceLeave:
				// Service framework: instance going away — remove and re-broadcast.
				lv := v.ServiceLeave
				if lv != nil && s.coord.coordServices != nil {
					s.coord.coordServices.Unregister(lv.GetInstanceId())
					s.log.Log(CatMeshCell, "coordinator: unregistered service %q (host=%s)", lv.GetInstanceId(), hostID)
					s.coord.broadcastPeerListOnServiceChange()
				}

			case *meshpb.HostMessage_ServiceEventSubscribe:
				// Service event bus: a remote process announced its complete
				// type-set. Whole-set replace; empty drops the entry.
				sub := v.ServiceEventSubscribe
				if sub != nil && s.coord.serviceEventRouter != nil {
					s.coord.serviceEventRouter.UpdateProcess(hostID, sub.GetTypeNames())
					s.log.Log(CatServicesBus, "coordinator: %s subscribes to %d types: %v",
						hostID, len(sub.GetTypeNames()), sub.GetTypeNames())
					// Rebroadcast PeerList so every process learns the new
					// routing table entry. Re-uses the existing service-
					// change broadcast path.
					s.coord.broadcastPeerListOnServiceChange()
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
			// Service framework: a gateway,service process publishes
			// service instances + bus subscriptions keyed on its gatewayID.
			// Drop both so publishers don't dispatch at a dead peer.
			if s.coord != nil && s.coord.coordServices != nil {
				s.coord.coordServices.UnregisterByHost(gatewayID)
			}
			if s.coord != nil && s.coord.serviceEventRouter != nil {
				s.coord.serviceEventRouter.RemoveProcess(gatewayID)
				s.coord.broadcastPeerListOnServiceChange()
			}
			return
		}

		// Stream closed with sessions still tracked. Treat as crash: mark
		// the gateway Dead first (so checkGatewayLiveness cannot observe a
		// Live gateway while sessions are mid-cleanup), then remove routes.
		s.gatewayRegistry.MarkDead(gatewayID)
		n := s.coord.sessionRoutes.RemoveByGateway(gatewayID)
		// Service framework cleanup (mirrors graceful path above).
		if s.coord != nil && s.coord.coordServices != nil {
			s.coord.coordServices.UnregisterByHost(gatewayID)
		}
		if s.coord != nil && s.coord.serviceEventRouter != nil {
			s.coord.serviceEventRouter.RemoveProcess(gatewayID)
			s.coord.broadcastPeerListOnServiceChange()
		}
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
						// Tombstone: gateway connection closed. The host's
						// cell still holds a grace-period session for `Username`;
						// keep the activeUsers + players entries so the next
						// login for the same user can detect reconnect via
						// handleInboundResolveSpawn (Active=false). Drop only
						// the routing-layer entries (sessionRoutes,
						// gatewayRegistry) since the connID is dead.
						//
						// activeUsers leaks slowly when users never come back;
						// the next login for the same uuid replaces the entry.
						// Real grace-expired removal would require host→coord
						// signalling on session-removed (deferred).
						s.log.Log(CatMeshCell, "coordinator: gateway %s session %s:%d user=%s -> grace",
							gatewayID, sa.GatewayId, sa.ConnId, sa.Username)
						s.coord.sessionRoutes.Remove(key)
						s.gatewayRegistry.RemoveSession(gatewayID, key)
						if sa.Username != "" {
							// Locate the existing route to recover host/cell
							// (gateway sent empty values in the tombstone).
							s.coord.mu.RLock()
							loc := s.coord.players[sa.Username]
							s.coord.mu.RUnlock()
							if loc != nil {
								s.coord.notifySessionDisconnected(sa.Username, loc.HostID, loc.CellID)
							}
						}
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
							CellID:   MeshCellID(sa.TargetCellId),
							Epoch:    1,
						})
						// Track the session on the RemoteGateway entry for crash cleanup.
						s.gatewayRegistry.AddSession(gatewayID, key)
						// Keep the username→hostID player index in sync so admin
						// dispatch (ActiveUserHost) resolves RoutePlayerOwner targets
						// in distributed mode where the host's local session callback
						// doesn't reach this process.
						if sa.Username != "" {
							s.coord.notifySessionActive(sa.Username, sa.TargetHostId, MeshCellID(sa.TargetCellId))
						}
						// Stamp the UUID-keyed activeUsers index so the next
						// login for this user_id can detect reconnect/kick-old
						// via handleInboundResolveSpawn. Embedded mode populates
						// activeUsers inline at dispatchPostAuthAssignment;
						// standalone mode hops through here.
						if sa.UserId != "" {
							if uid, err := uuid.Parse(sa.UserId); err == nil && uid != uuid.Nil {
								s.coord.registerAuthenticatedSession(uid, sa.Username, sa.GatewayId, sa.ConnId, sa.TargetHostId, MeshCellID(sa.TargetCellId))
							}
						}
					}
				}

			case *meshpb.HostMessage_PlayerMigrated:
				// PlayerMigrated is sent by nodes, not gateways. Log as protocol error.
				pm := v.PlayerMigrated
				if pm != nil {
					s.log.Log(CatMeshCell, "coordinator: gateway %s sent unexpected PlayerMigrated for %s:%d (protocol error — expected from nodes)",
						gatewayID, pm.GatewayId, pm.ConnId)
				}

			case *meshpb.HostMessage_ResolveSpawn:
				// Gateway asked us to resolve a login spawn position. Run the
				// coordinator's registered SpawnResolver and reply with the world
				// coords (or ok=false if no saved position).
				req := v.ResolveSpawn
				if req != nil {
					go s.handleInboundResolveSpawn(gatewayID, req)
				}

			case *meshpb.HostMessage_ServiceAnnounce:
				// Service framework: a gateway,service process announced an
				// instance via its mesh-gateway control stream. Same handling
				// as the host-stream case above — register + re-broadcast.
				// Without this, services hosted on standalone-gateway processes
				// never appear in coordServices and RouteService dispatch fails.
				ann := v.ServiceAnnounce
				if ann != nil && s.coord.coordServices != nil {
					inst := service.CoordInstance{
						Kind:       ann.GetKind(),
						InstanceID: ann.GetInstanceId(),
						HostID:     ann.GetHostId(),
						OpCodes:    append([]uint32(nil), ann.GetOpCodes()...),
						JoinedAt:   time.Now(),
					}
					if err := s.coord.coordServices.Register(inst); err != nil {
						s.log.Log(CatMeshCell, "coordinator: ServiceAnnounce %q from gateway %s rejected: %v",
							ann.GetInstanceId(), gatewayID, err)
					} else {
						s.log.Log(CatMeshCell, "coordinator: registered service %s/%s on %s via gateway %s (codes=%v)",
							inst.Kind, inst.InstanceID, inst.HostID, gatewayID, inst.OpCodes)
						s.coord.broadcastPeerListOnServiceChange()
					}
				}

			case *meshpb.HostMessage_ServiceLeave:
				// Service framework: instance going away on a gateway,service
				// process. Same handling as the host-stream path.
				lv := v.ServiceLeave
				if lv != nil && s.coord.coordServices != nil {
					s.coord.coordServices.Unregister(lv.GetInstanceId())
					s.log.Log(CatMeshCell, "coordinator: unregistered service %q (gateway=%s)", lv.GetInstanceId(), gatewayID)
					s.coord.broadcastPeerListOnServiceChange()
				}

			case *meshpb.HostMessage_ServiceEventSubscribe:
				// Service event bus: a standalone gateway (or gateway,service)
				// process announced its complete type-set. Whole-set replace
				// keyed on the gateway ID; empty drops the entry. Mirrors the
				// host-stream handler so subscriptions originating on
				// gateway-bearing processes populate the routing table.
				sub := v.ServiceEventSubscribe
				if sub != nil && s.coord.serviceEventRouter != nil {
					s.coord.serviceEventRouter.UpdateProcess(gatewayID, sub.GetTypeNames())
					s.log.Log(CatServicesBus, "coordinator: gw %s subscribes to %d types: %v",
						gatewayID, len(sub.GetTypeNames()), sub.GetTypeNames())
					s.coord.broadcastPeerListOnServiceChange()
				}

			case *meshpb.HostMessage_CommandResponse:
				// gateway,service replied to a CommandRequest the coord sent.
				// Deliver to the in-flight orchestrator. Mirrors the host-stream
				// handler so service-routed commands targeting a gateway,service
				// process complete instead of timing out.
				resp := v.CommandResponse
				if resp != nil && s.coord.transport != nil {
					s.coord.transport.orch.OnResponse(resp)
				}

			case *meshpb.HostMessage_CommandRequest:
				// gateway → coord command (coord-routed verbs originating from a
				// gateway,service process). Execute locally and send response
				// back on this gateway's stream. Parity with the host-stream path.
				req := v.CommandRequest
				if req != nil && s.coord.dispatcher != nil {
					go s.handleInboundCommandRequestFromGateway(gatewayID, req)
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

// handleInboundResolveSpawn answers a gateway's ResolveSpawn request by
// invoking the coordinator's registered SpawnResolver (if any) and sending
// SpawnResolved back on the gateway's stream. Runs in a goroutine so the
// recv loop stays live.
//
// When req.UserId is set and the coordinator's activeUsers index has a
// lingering disconnected session for that UUID, the response carries
// is_reconnect=true plus the existing host/cell so the gateway routes the
// PlayerAssignment to the user's existing entity instead of spawning a fresh
// one. Active sessions for the same UUID are kicked first (kick-old policy,
// matching embedded gateway behavior).
func (s *meshControlServer) handleInboundResolveSpawn(gatewayID string, req *meshpb.ResolveSpawn) {
	s.coord.mu.RLock()
	resolver := s.coord.spawnResolver
	cellSize := s.coord.cfg.CellSize
	s.coord.mu.RUnlock()

	resp := &meshpb.SpawnResolved{RequestId: req.RequestId, Ok: true}
	var session engine.PlayerSession
	session.Username = req.Username
	if req.UserId != "" {
		if uid, err := uuid.Parse(req.UserId); err == nil {
			session.UserID = uid
		}
	}

	var loc coords.Location
	if resolver != nil {
		loc = resolver(&session)
	} else {
		loc = defaultSpawnLocation(cellSize)
	}
	resp.WorldX = loc.X
	resp.WorldY = loc.Y

	if req.UserId != "" {
		if uid, err := uuid.Parse(req.UserId); err == nil && uid != uuid.Nil {
			s.coord.applyResolveSpawnReconnect(uid, resp)
		}
	}

	if err := s.sendCoordMessageToGateway(gatewayID, &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_SpawnResolved{SpawnResolved: resp},
	}); err != nil {
		s.log.Log(CatMeshMsg, "coordinator: SpawnResolved to gateway %s failed: %v", gatewayID, err)
	}
}

// handleInboundCommandRequest executes an inbound CommandRequest that arrived
// from a host on the coordinator's control stream, then sends the response back
// on that same host's stream. Runs in a goroutine so the recv loop stays live.
func (s *meshControlServer) handleInboundCommandRequest(hostID string, req *meshpb.CommandRequest) {
	ctx, cancel := context.WithDeadline(
		context.Background(),
		timeFromUnixNanos(req.DeadlineUnixNanos),
	)
	defer cancel()

	resp := executeCommandRequest(ctx, s.coord.dispatcher, hostID, req)
	msg := &meshpb.CoordMessage{
		CoordEpoch: s.coord.coordEpoch,
		Msg:        &meshpb.CoordMessage_CommandResponse{CommandResponse: resp},
	}
	if err := s.sendCoordMessageToHost(hostID, msg); err != nil {
		s.log.Log(CatMeshMsg, "coordinator: CommandResponse to host %s failed: %v", hostID, err)
	}
}

// handleInboundCommandRequestFromGateway is the gateway-stream parity of
// handleInboundCommandRequest: executes a CommandRequest that arrived on a
// gateway,service control stream and replies on that same gateway stream.
// Runs in a goroutine so the recv loop stays live.
func (s *meshControlServer) handleInboundCommandRequestFromGateway(gatewayID string, req *meshpb.CommandRequest) {
	ctx, cancel := context.WithDeadline(
		context.Background(),
		timeFromUnixNanos(req.DeadlineUnixNanos),
	)
	defer cancel()

	resp := executeCommandRequest(ctx, s.coord.dispatcher, gatewayID, req)
	msg := &meshpb.CoordMessage{
		CoordEpoch: s.coord.coordEpoch,
		Msg:        &meshpb.CoordMessage_CommandResponse{CommandResponse: resp},
	}
	if err := s.sendCoordMessageToGateway(gatewayID, msg); err != nil {
		s.log.Log(CatMeshMsg, "coordinator: CommandResponse to gateway %s failed: %v", gatewayID, err)
	}
}

// timeFromUnixNanos converts a deadline expressed as Unix nanoseconds to a
// time.Time. Returns a time 30s from now when nanos is zero or negative.
func timeFromUnixNanos(nanos int64) time.Time {
	if nanos <= 0 {
		return time.Now().Add(30 * time.Second)
	}
	return time.Unix(0, nanos)
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

// broadcastCoordTimeSync sends CoordTimeSync to every currently
// registered remote host. Per-stream errors are logged but do not
// prevent the remaining hosts from receiving the update. In-process
// (Local) hosts are skipped — they have no MeshControl stream and
// their ClusterClock is pre-seeded with offset=0.
func (s *meshControlServer) broadcastCoordTimeSync() {
	if s.registry == nil {
		return
	}
	nowMs := uint64(time.Now().UnixMilli())
	seq := s.clusterClockSeq.Add(1)
	msg := &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_CoordTimeSync{
			CoordTimeSync: &meshpb.CoordTimeSync{
				CoordTimeMs: nowMs,
				Seq:         seq,
			},
		},
	}
	for _, h := range s.registry.LiveHosts() {
		if h.Local {
			continue
		}
		if err := s.sendCoordMessageToHost(h.ID, msg); err != nil {
			s.log.Log(CatMeshCell,
				"coordinator: CoordTimeSync broadcast to host=%s failed: %v",
				h.ID, err)
		}
	}
}

// sendCoordMessageToHost pushes a CoordMessage onto the given host's control
// stream. Returns an error if there is no stream for the host or the
// Send call fails. Uses a per-stream mutex because grpc-go server
// streams are not safe for concurrent Send.
//
// Falls back to the gateway control stream when no host stream is found
// for the same identifier. This handles gateway,service processes that
// announce services with the gateway's ID — RouteService dispatch then
// targets that ID, but the only live control stream for it is the
// gateway-stream, not a host-stream. Cheap and unambiguous: gatewayID
// and hostID never legitimately collide on the same coord, so the
// fallback is a no-op when both maps are populated correctly.
func (s *meshControlServer) sendCoordMessageToHost(hostID string, msg *meshpb.CoordMessage) error {
	s.mu.RLock()
	stream := s.streams[hostID]
	smu := s.streamMu[hostID]
	if stream == nil {
		// Fallback: try gateway control streams. A gateway,service process
		// registers via the gateway control stream and announces services
		// keyed on its gatewayID, so service-routed commands targeting
		// that ID need to traverse this stream.
		stream = s.gatewayStreams[hostID]
		smu = s.gatewayMu[hostID]
	}
	s.mu.RUnlock()
	if stream == nil || smu == nil {
		return fmt.Errorf("no control stream for host or gateway %q", hostID)
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
