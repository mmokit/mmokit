// Package universe — mesh_gateway_client.go
//
// meshGatewayClient is the gateway-side long-lived connection to the
// coordinator's MeshControl service. It mirrors meshControlClient but
// sends RegisterGateway as the first message instead of RegisterHost.
//
// Lifecycle:
//  1. Start(ctx) spawns runConnectLoop and returns immediately.
//  2. runConnectLoop dials the coordinator with exponential backoff.
//     On each successful connection it opens a Control bidi stream,
//     sends RegisterGateway with the gateway's WS + gRPC addresses,
//     and runs the recv + heartbeat goroutines.
//  3. Shutdown cancels the root context; the connect loop exits after
//     the current attempt finishes.
//
// dispatch handles:
//   - RegisterAck  — logs success/failure
//   - PeerList     — calls gw.topology.applyPeerList + opens MeshData streams to nodes
//   - UpstreamSwitch — calls gw.OnUpstreamSwitch to flip session upstream
//
// One instance per standalone gateway process; stored on Gateway.controlClient.

package universe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
)

// meshGatewayClient connects a standalone gateway process to the coordinator
// via the MeshControl service. It mirrors meshControlClient but sends
// RegisterGateway as the first message.
type meshGatewayClient struct {
	gw        *Gateway
	coordAddr string

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// connMu guards the per-connection state (conn, stream, streamCancel).
	connMu       sync.Mutex
	conn         *grpc.ClientConn
	stream       meshpb.MeshControl_ControlClient
	streamCancel context.CancelFunc

	sendMu sync.Mutex // grpc-go ClientStream is not safe for concurrent Send

	epochMu      sync.RWMutex
	highestEpoch uint64

	// done is closed when runConnectLoop exits.
	done chan struct{}
}

// newMeshGatewayClient constructs the client without dialing. Start kicks
// off the reconnect loop.
func newMeshGatewayClient(gw *Gateway, coordAddr string) *meshGatewayClient {
	return &meshGatewayClient{
		gw:        gw,
		coordAddr: coordAddr,
		done:      make(chan struct{}),
	}
}

// Start spawns the reconnect loop and returns immediately.
func (c *meshGatewayClient) Start(ctx context.Context) error {
	c.rootCtx, c.rootCancel = context.WithCancel(ctx)
	go c.runConnectLoop()
	return nil
}

// runConnectLoop is the outer reconnect loop. Exits when rootCtx is cancelled.
func (c *meshGatewayClient) runConnectLoop() {
	defer close(c.done)

	backoff := connectBackoffMin
	firstAttempt := true

	for {
		if c.rootCtx.Err() != nil {
			return
		}

		if firstAttempt {
			c.gw.log.Log(CatMeshCell, "gateway: dialing coordinator %s", c.coordAddr)
			firstAttempt = false
		}

		err := c.runConnection()
		if c.rootCtx.Err() != nil {
			return
		}

		if err != nil {
			sleep := jitter(backoff)
			c.gw.log.Log(CatMeshCell, "gateway: control connection failed (%v), retrying in %s", err, sleep.Round(time.Millisecond))
			if !sleepCtx(c.rootCtx, sleep) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// Clean EOF — coordinator restarted; retry quickly.
		c.gw.log.Log(CatMeshCell, "gateway: control stream ended cleanly, reconnecting")
		backoff = connectBackoffMin
		if !sleepCtx(c.rootCtx, connectBackoffMin) {
			return
		}
	}
}

// runConnection attempts one full connection lifetime. Returns nil on clean
// EOF; returns an error on dial failure, stream open failure, or recv error.
func (c *meshGatewayClient) runConnection() error {
	conn, err := grpc.NewClient(c.coordAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                60 * time.Second,
			Timeout:             20 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallSendMsgSize(meshMaxMsgBytes),
			grpc.MaxCallRecvMsgSize(meshMaxMsgBytes),
		),
	)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	streamCtx, streamCancel := context.WithCancel(c.rootCtx)
	client := meshpb.NewMeshControlClient(conn)
	stream, err := client.Control(streamCtx)
	if err != nil {
		streamCancel()
		_ = conn.Close()
		return fmt.Errorf("open stream: %w", err)
	}

	c.connMu.Lock()
	c.conn = conn
	c.stream = stream
	c.streamCancel = streamCancel
	c.connMu.Unlock()

	defer func() {
		c.connMu.Lock()
		c.conn = nil
		c.stream = nil
		c.streamCancel = nil
		c.connMu.Unlock()
		streamCancel()
		_ = conn.Close()
	}()

	// Determine WS + gRPC addresses to advertise.
	wsAddr := c.gw.wsAddr
	grpcAddr := ""
	if c.gw.hostNetwork != nil {
		grpcAddr = c.gw.hostNetwork.Addr()
	}

	reg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_RegisterGateway{
			RegisterGateway: &meshpb.RegisterGateway{
				GatewayId: c.gw.id,
				WsAddr:    wsAddr,
				GrpcAddr:  grpcAddr,
			},
		},
	}
	if err := c.send(reg); err != nil {
		return fmt.Errorf("send RegisterGateway: %w", err)
	}
	c.gw.log.Log(CatMeshCell, "gateway: registered as %q to coordinator %s (ws=%s grpc=%s)", c.gw.id, c.coordAddr, wsAddr, grpcAddr)

	go c.runHeartbeatLoop(streamCtx)
	return c.runRecvLoop()
}

// runHeartbeatLoop sends a Heartbeat every heartbeatInterval until ctx is cancelled.
func (c *meshGatewayClient) runHeartbeatLoop(ctx context.Context) {
	tick := time.NewTicker(heartbeatInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			hb := &meshpb.HostMessage{
				Msg: &meshpb.HostMessage_Heartbeat{
					Heartbeat: &meshpb.Heartbeat{
						HostId: c.gw.id,
					},
				},
			}
			if err := c.send(hb); err != nil {
				return
			}
		}
	}
}

// send pushes a HostMessage onto the current control stream using the
// per-connection send mutex.
func (c *meshGatewayClient) send(msg *meshpb.HostMessage) error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		return fmt.Errorf("mesh gateway control: stream not ready")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

// Shutdown cancels the reconnect loop and waits for it to exit.
func (c *meshGatewayClient) Shutdown() {
	if c.rootCancel != nil {
		c.rootCancel()
	}
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		c.gw.log.Log(CatMeshCell, "gateway: control client shutdown timed out")
	}
}

// runRecvLoop reads CoordMessages from the current stream and dispatches them.
// Returns nil on clean EOF; returns an error on any other Recv failure.
func (c *meshGatewayClient) runRecvLoop() error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		return fmt.Errorf("recv: no active stream")
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.gw.log.Log(CatMeshCell, "gateway: control stream closed (EOF)")
				return nil
			}
			if c.rootCtx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}

		// Epoch enforcement.
		if msg.CoordEpoch > 0 {
			c.epochMu.Lock()
			if msg.CoordEpoch < c.highestEpoch {
				prev := c.highestEpoch
				c.epochMu.Unlock()
				c.gw.log.Log(CatMeshCell, "gateway: dropping stale CoordMessage epoch=%d (highest=%d)", msg.CoordEpoch, prev)
				continue
			}
			if msg.CoordEpoch > c.highestEpoch {
				c.highestEpoch = msg.CoordEpoch
			}
			c.epochMu.Unlock()
		}

		c.dispatch(msg)
	}
}

// dispatch routes a received CoordMessage to the appropriate handler.
func (c *meshGatewayClient) dispatch(msg *meshpb.CoordMessage) {
	switch v := msg.Msg.(type) {
	case *meshpb.CoordMessage_RegisterAck:
		ack := v.RegisterAck
		if ack != nil && ack.Ok {
			c.gw.log.Log(CatMeshCell, "gateway: registered successfully (epoch=%d)", msg.CoordEpoch)
		} else {
			reason := ""
			if ack != nil {
				reason = ack.Reason
			}
			c.gw.log.Log(CatMeshCell, "gateway: registration rejected: %s", reason)
		}

	case *meshpb.CoordMessage_PeerList:
		c.applyPeerList(v.PeerList)

	case *meshpb.CoordMessage_UpstreamSwitch:
		us := v.UpstreamSwitch
		if us != nil {
			c.gw.OnUpstreamSwitch(us.ConnId, us.NewHostId, MeshCellID(us.NewCellId), us.NewEpoch)
		}

	case *meshpb.CoordMessage_SpawnResolved:
		resp := v.SpawnResolved
		if resp != nil && c.gw.spawnOrch != nil {
			c.gw.spawnOrch.OnResponse(resp)
		}

	case *meshpb.CoordMessage_CommandRequest:
		// Coordinator dispatched a cmdsys command to this gateway,service
		// process. Execute against the local Process dispatcher and reply
		// via HostMessage_CommandResponse on this same gateway control
		// stream. Mirrors the host-side handler in mesh_control_client.go.
		req := v.CommandRequest
		if req != nil && c.gw.process != nil && c.gw.process.dispatcher != nil {
			go c.handleInboundCommandRequest(req)
		}

	case *meshpb.CoordMessage_CommandResponse:
		// Response to a CommandRequest this gateway sent earlier. Deliver
		// to the local transport orchestrator so the waiting Invoke
		// unblocks.
		resp := v.CommandResponse
		if resp != nil && c.gw.process != nil && c.gw.process.transport != nil {
			c.gw.process.transport.orch.OnResponse(resp)
		}

	case *meshpb.CoordMessage_CommandCancel:
		// Coordinator wants to cancel an in-flight handler on this gateway.
		cc := v.CommandCancel
		if cc != nil && c.gw.process != nil && c.gw.process.transport != nil {
			c.gw.process.transport.orch.OnCancel(cc.RequestId)
		}

	default:
		c.gw.log.Log(CatMeshMsg, "gateway: received %T (handler not wired)", v)
	}
}

// handleInboundCommandRequest executes a CommandRequest that the
// coordinator dispatched to this gateway,service process and replies
// with a HostMessage_CommandResponse on this gateway's control stream.
// Runs in a goroutine so the recv loop stays live.
func (c *meshGatewayClient) handleInboundCommandRequest(req *meshpb.CommandRequest) {
	ctx, cancel := context.WithDeadline(
		context.Background(),
		timeFromUnixNanos(req.DeadlineUnixNanos),
	)
	defer cancel()

	// Use the gateway ID as the executing host identifier — it matches
	// what was registered in coordServices via ServiceAnnounce (see
	// localServiceHostID) so resp.TargetId stays consistent with the
	// route the coordinator used to reach us.
	hostID := c.gw.id
	resp := executeCommandRequest(ctx, c.gw.process.dispatcher, hostID, req)
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_CommandResponse{CommandResponse: resp},
	}
	if err := c.send(msg); err != nil {
		c.gw.log.Log(CatMeshMsg, "gateway: CommandResponse send failed: %v", err)
	}
}

// applyPeerList updates the gateway's cached topology from a PeerList broadcast.
// It opens MeshData streams to any new nodes and updates the cell→host map.
func (c *meshGatewayClient) applyPeerList(pl *meshpb.PeerList) {
	if pl == nil {
		return
	}

	// Update cell→host topology snapshot.
	c.gw.topology.applyPeerList(pl.Cells)

	// Service framework: rebuild the gateway's routing index. Standalone
	// gateways own the only RoutingIndex that actually drives op forwarding,
	// so this call is critical — without it, services aren't reachable.
	// Use process (always set) rather than coord (nil for standalone).
	if c.gw.process != nil {
		c.gw.process.applyServicesToRoutingIndex(pl.Services)
	}

	// Apply event-bus routing table from PeerList. Whole-map replace —
	// coord's snapshot is authoritative. The cache is read on every
	// service.Publish to resolve remote subscribers.
	if c.gw.process != nil && c.gw.process.bus != nil {
		table := make(map[string][]string, len(pl.GetEventRouting()))
		for typeName, procs := range pl.GetEventRouting() {
			table[typeName] = append([]string(nil), procs.GetProcessIds()...)
		}
		c.gw.process.bus.SetRoutingCache(table)
	}

	if c.gw.hostNetwork == nil {
		c.gw.log.Log(CatMeshCell, "gateway: PeerList received but no hostNetwork (standalone mode not fully wired)")
		return
	}

	// Open MeshData streams to each node in the peer list.
	// ConnectPeer is idempotent — it replaces any existing stream for the same host ID.
	wanted := make(map[string]string, len(pl.Hosts))
	for _, hr := range pl.Hosts {
		wanted[hr.HostId] = hr.GrpcAddr
	}

	for hid, addr := range wanted {
		if addr == "" {
			continue
		}
		if err := c.gw.hostNetwork.ConnectPeer(hid, addr, peerKindNode); err != nil {
			c.gw.log.Log(CatMeshCell, "gateway: ConnectPeer node %s (%s) failed: %v", hid, addr, err)
		} else {
			c.gw.log.Log(CatMeshCell, "gateway: connected to node %s (%s)", hid, addr)
		}
	}

	// Disconnect nodes no longer in the wanted set.
	c.gw.hostNetwork.mu.RLock()
	existing := make([]string, 0, len(c.gw.hostNetwork.peers))
	for hid, peer := range c.gw.hostNetwork.peers {
		if peer.kind == peerKindNode {
			existing = append(existing, hid)
		}
	}
	c.gw.hostNetwork.mu.RUnlock()
	for _, hid := range existing {
		if _, keep := wanted[hid]; !keep {
			c.gw.hostNetwork.DisconnectPeer(hid)
			c.gw.log.Log(CatMeshCell, "gateway: disconnected from node %s", hid)
		}
	}

	c.gw.log.Log(CatMeshCell, "gateway: applied PeerList (%d nodes, %d cells)", len(wanted), len(pl.Cells))
}
