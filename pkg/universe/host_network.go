package universe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// Tunables — exported as package-level constants so tests can reference
// them even though they are not configurable in S3.
const (
	peerOutQueueSize = 256                    // depth of per-peer outbound channel
	peerSendDeadline = 250 * time.Millisecond // reliable send block cap
	meshMaxMsgBytes  = 16 << 20               // 16MB send/recv cap
)

// peerKind distinguishes node peers (other game server processes) from
// gateway peers (connection-terminating processes that hold WebSocket
// connections and forward client I/O). The distinction is needed because
// outbound ClientFrame routing in VirtualConnManager must find the
// correct gateway by its stable ID, not a host address.
type peerKind uint8

const (
	peerKindNode    peerKind = iota // another game server node
	peerKindGateway                 // a WebSocket gateway process
)

// HostNetwork owns the MeshData gRPC server and bidi client streams to
// peer hosts for one Host. It is nil in single-host colocated mode.
//
// Concurrency model:
//   - Each peer stream has exactly one dedicated sender goroutine
//     draining a buffered outbound channel. grpc-go's ClientStream.Send
//     is not safe for concurrent use; the channel + goroutine pattern
//     is the upstream-recommended workaround (see Documentation/concurrency.md).
//   - Each peer stream has exactly one dedicated receiver goroutine
//     calling Recv in a loop until EOF or context cancel. Send and Recv
//     on the same stream run concurrently — that is explicitly allowed.
//   - The peers map is guarded by a sync.RWMutex. All reads take RLock;
//     mutations (ConnectPeer, dropPeer, Shutdown) take Lock.
type HostNetwork struct {
	hostID   string
	grpcAddr string
	server   *grpc.Server
	listener net.Listener
	log      *logger.Logger
	host     *Host

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.RWMutex
	peers map[string]*hostPeer

	// vcm is set by node-mode Build() via SetVCM. Nil in all-in-one mode
	// or on coordinators. routeInboundFrame uses it to dispatch
	// ClientInput / PlayerAssignment / ClientDisconnect.
	vcm *VirtualConnManager
}

// hostPeer holds one outbound stream to a remote host along with its
// sender + receiver goroutines.
type hostPeer struct {
	hostID   string
	grpcAddr string
	kind     peerKind // node or gateway

	conn   *grpc.ClientConn
	stream meshpb.MeshData_DataClient
	cancel context.CancelFunc // cancels the stream context on shutdown or error

	outQ chan outboundFrame
	done chan struct{} // closed when the sender goroutine exits
}

// outboundFrame is the queue entry handed to a peer's sender goroutine.
// If result is non-nil, the sender writes the Send() error into it —
// SendReliable uses this to block until delivery completes (or fails).
// SendLossy leaves result nil and treats Send as fire-and-forget.
type outboundFrame struct {
	frame  *meshpb.MeshFrame
	result chan<- error
}

// NewHostNetwork binds a listener, constructs the gRPC server with
// keepalive + 16MB max-message caps, and starts serving in a goroutine.
// Addr() returns the bound address (useful when grpcAddr was ":0").
func NewHostNetwork(host *Host, grpcAddr string, log *logger.Logger) (*HostNetwork, error) {
	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return nil, fmt.Errorf("mesh listen %q: %w", grpcAddr, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	n := &HostNetwork{
		hostID:   host.ID,
		grpcAddr: listener.Addr().String(),
		listener: listener,
		log:      log,
		host:     host,
		ctx:      ctx,
		cancel:   cancel,
		peers:    make(map[string]*hostPeer),
	}

	n.server = grpc.NewServer(
		grpc.MaxRecvMsgSize(meshMaxMsgBytes),
		grpc.MaxSendMsgSize(meshMaxMsgBytes),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    60 * time.Second,
			Timeout: 20 * time.Second,
		}),
		// MinTime must stay below the client's Time, otherwise the server
		// will send GOAWAY too_many_pings. 30s < 60s leaves headroom.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             30 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	meshpb.RegisterMeshDataServer(n.server, &meshDataServer{net: n})

	go func() {
		if err := n.server.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Log(CatMeshMsg, "[%s] mesh grpc serve: %v", host.ID, err)
		}
	}()

	return n, nil
}

// Addr returns the actual bound listen address.
func (n *HostNetwork) Addr() string { return n.grpcAddr }

// HostID returns the owning host's ID.
func (n *HostNetwork) HostID() string { return n.hostID }

// SetVCM associates a VirtualConnManager with this HostNetwork. Called by
// node-mode Build() after construction so routeInboundFrame can dispatch
// ClientInput / PlayerAssignment / ClientDisconnect to the VCM.
func (n *HostNetwork) SetVCM(vcm *VirtualConnManager) {
	n.vcm = vcm
}

// ConnectPeer opens a bidi MeshData stream to a peer host and starts
// its dedicated sender + receiver goroutines. If a peer with the same
// ID already exists, it is replaced (old stream cancelled).
// kind distinguishes node peers from gateway peers; use peerKindNode for
// ordinary server-to-server links and peerKindGateway for gateways.
func (n *HostNetwork) ConnectPeer(hostID, grpcAddr string, kind peerKind) error {
	conn, err := grpc.NewClient(grpcAddr,
		// TODO(S4): replace with mTLS credentials once the cert management
		// layer lands. insecure is acceptable here because S3 only runs in
		// loopback (in-process 2-host integration tests + examples).
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
		return fmt.Errorf("mesh dial %s: %w", grpcAddr, err)
	}

	streamCtx, cancel := context.WithCancel(n.ctx)
	client := meshpb.NewMeshDataClient(conn)
	stream, err := client.Data(streamCtx)
	if err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("mesh open stream %s: %w", hostID, err)
	}

	peer := &hostPeer{
		hostID:   hostID,
		grpcAddr: grpcAddr,
		kind:     kind,
		conn:     conn,
		stream:   stream,
		cancel:   cancel,
		outQ:     make(chan outboundFrame, peerOutQueueSize),
		done:     make(chan struct{}),
	}

	n.mu.Lock()
	if old, ok := n.peers[hostID]; ok {
		old.cancel()
		_ = old.conn.Close()
	}
	n.peers[hostID] = peer
	n.mu.Unlock()

	go n.runPeerSender(peer)
	go n.runPeerReceiver(peer)
	return nil
}

// runPeerSender drains p.outQ and pushes each frame onto the stream in
// arrival order. Exits on context cancel, queue close, or Send error.
// On Send error, the peer is removed from the map (dropPeer) and the
// sender exits.
func (n *HostNetwork) runPeerSender(p *hostPeer) {
	defer close(p.done)
	for {
		select {
		case <-n.ctx.Done():
			return
		case of, ok := <-p.outQ:
			if !ok {
				return
			}
			err := p.stream.Send(of.frame)
			if of.result != nil {
				of.result <- err
			}
			if err != nil {
				if n.ctx.Err() == nil {
					n.log.Log(CatMeshMsg, "[%s] peer %s send error: %v", n.hostID, p.hostID, err)
				}
				n.dropPeer(p)
				return
			}
		}
	}
}

// runPeerReceiver calls Recv in a loop until EOF or error. Each received
// frame is routed via n.routeInboundFrame.
func (n *HostNetwork) runPeerReceiver(p *hostPeer) {
	for {
		frame, err := p.stream.Recv()
		if err != nil {
			if err := n.isUnexpectedRecvErr(p, err); err != nil {
				n.log.Log(CatMeshMsg, "[%s] peer %s recv error: %v", n.hostID, p.hostID, err)
			}
			n.dropPeer(p)
			return
		}
		if err := n.routeInboundFrame(frame); err != nil {
			n.log.Log(CatMeshMsg, "[%s] peer %s route error: %v", n.hostID, p.hostID, err)
		}
	}
}

// isUnexpectedRecvErr returns the error if it represents an actual
// problem worth logging, or nil if it is an expected stream teardown
// (EOF, root-context cancel, per-peer stream cancel, transient
// Unavailable during shutdown).
func (n *HostNetwork) isUnexpectedRecvErr(p *hostPeer, err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	if n.ctx.Err() != nil {
		return nil
	}
	if p.stream.Context().Err() != nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Canceled, codes.Unavailable:
		return nil
	}
	return err
}

// dropPeer removes the given peer from the map (by pointer identity) and
// closes its resources. Idempotent and safe against racing replacements:
// if a newer peer has already replaced `self` under the same hostID, that
// newer entry is left untouched.
func (n *HostNetwork) dropPeer(self *hostPeer) {
	n.mu.Lock()
	current, ok := n.peers[self.hostID]
	if ok && current == self {
		delete(n.peers, self.hostID)
	} else {
		ok = false
	}
	n.mu.Unlock()
	if !ok {
		// Either the peer was already dropped or a newer peer replaced it.
		// Still cancel + close our local resources in case the caller hasn't.
		self.cancel()
		_ = self.conn.Close()
		return
	}
	self.cancel()
	_ = self.conn.Close()
}

// DisconnectPeer closes the outbound stream to the given peer ID. Used
// for graceful peer removal when the coordinator's PeerList drops a
// host (after crash reassignment, graceful leave, etc.). Safe to call
// on an unknown peer — it's a no-op in that case.
//
// Parallel to dropPeer but driven by control-plane events rather than
// Send errors. Unlike dropPeer it identifies the peer by ID instead of
// by pointer, because the caller (PeerList reconciliation) only knows
// the host ID.
func (n *HostNetwork) DisconnectPeer(hostID string) {
	n.mu.Lock()
	peer, ok := n.peers[hostID]
	if ok {
		delete(n.peers, hostID)
	}
	n.mu.Unlock()
	if !ok {
		return
	}
	peer.cancel()
	_ = peer.conn.Close()
}

// SendLossy enqueues a frame for fire-and-forget delivery to hostID.
// Returns false if the peer is unknown or its outbound queue is full.
// Used for border frames: the 30-tick forced resync recovers the
// receiver so dropping a single frame is acceptable.
func (n *HostNetwork) SendLossy(hostID string, frame *meshpb.MeshFrame) bool {
	n.mu.RLock()
	p, ok := n.peers[hostID]
	n.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case p.outQ <- outboundFrame{frame: frame}:
		return true
	default:
		return false
	}
}

// SendReliable enqueues a frame and waits for the sender goroutine to
// push it onto the stream. Returns an error if the peer is unknown,
// the queue is full past peerSendDeadline, or the Send call itself
// failed. Used for handoff / action / chat / assignment frames which
// must not drop.
func (n *HostNetwork) SendReliable(hostID string, frame *meshpb.MeshFrame) error {
	n.mu.RLock()
	p, ok := n.peers[hostID]
	n.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no peer %q", hostID)
	}

	result := make(chan error, 1)
	deadline := time.NewTimer(peerSendDeadline)
	defer deadline.Stop()

	// Phase 1: enqueue.
	select {
	case p.outQ <- outboundFrame{frame: frame, result: result}:
	case <-deadline.C:
		return fmt.Errorf("peer %q queue backpressure", hostID)
	case <-n.ctx.Done():
		return n.ctx.Err()
	}

	// Phase 2: wait for the sender goroutine to Send and report the result.
	select {
	case err := <-result:
		return err
	case <-deadline.C:
		return fmt.Errorf("peer %q send deadline exceeded", hostID)
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
}

// findGatewayPeer returns the hostPeer entry for the given gateway ID, or
// nil if no gateway peer exists. Takes the read lock internally; callers
// must NOT hold n.mu when calling.
func (n *HostNetwork) findGatewayPeer(gatewayID string) *hostPeer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, p := range n.peers {
		if p.kind == peerKindGateway && p.hostID == gatewayID {
			return p
		}
	}
	return nil
}

// SendLossyToGateway finds the gateway peer registered under gatewayID and
// enqueues frame for fire-and-forget delivery. Returns false if no gateway
// peer with that ID exists or its outbound queue is full. O(N) scan — peers
// are expected to be few.
func (n *HostNetwork) SendLossyToGateway(gatewayID string, frame *meshpb.MeshFrame) bool {
	found := n.findGatewayPeer(gatewayID)
	if found == nil {
		return false
	}
	select {
	case found.outQ <- outboundFrame{frame: frame}:
		return true
	default:
		return false
	}
}

// SendReliableToGateway finds the gateway peer registered under gatewayID
// and enqueues frame, waiting for the sender goroutine to push it onto the
// stream. Returns an error if the peer is unknown, the queue is backlogged
// past peerSendDeadline, or the Send call itself fails. O(N) scan.
func (n *HostNetwork) SendReliableToGateway(gatewayID string, frame *meshpb.MeshFrame) error {
	found := n.findGatewayPeer(gatewayID)
	if found == nil {
		return fmt.Errorf("no gateway peer %q", gatewayID)
	}

	result := make(chan error, 1)
	deadline := time.NewTimer(peerSendDeadline)
	defer deadline.Stop()

	select {
	case found.outQ <- outboundFrame{frame: frame, result: result}:
	case <-deadline.C:
		return fmt.Errorf("gateway peer %q queue backpressure", gatewayID)
	case <-n.ctx.Done():
		return n.ctx.Err()
	}

	select {
	case err := <-result:
		return err
	case <-deadline.C:
		return fmt.Errorf("gateway peer %q send deadline exceeded", gatewayID)
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
}

// WaitPeersReady blocks until every hostID in expected has an entry in
// n.peers, or the timeout fires. Used by integration tests to avoid
// race conditions during startup.
func (n *HostNetwork) WaitPeersReady(expected []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		n.mu.RLock()
		ready := true
		for _, id := range expected {
			if _, ok := n.peers[id]; !ok {
				ready = false
				break
			}
		}
		n.mu.RUnlock()
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("peers not ready within %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Shutdown tears down the gRPC server and all peer streams. Works
// around grpc-go#2888 (GracefulStop hangs with active bidi streams)
// by cancelling outbound streams first, then racing GracefulStop
// against a 5s deadline and falling back to Stop().
func (n *HostNetwork) Shutdown() error {
	// 1. Cancel the root context — sender goroutines observe on next select.
	n.cancel()

	// 2. Cancel every outbound client stream + close its conn.
	n.mu.Lock()
	peers := n.peers
	n.peers = nil
	n.mu.Unlock()
	for _, p := range peers {
		p.cancel()
		_ = p.conn.Close()
	}

	// 3. Wait briefly for sender goroutines to exit.
	for _, p := range peers {
		select {
		case <-p.done:
		case <-time.After(500 * time.Millisecond):
			// wedged sender; server.Stop below will force it
		}
	}

	// 4. Race GracefulStop against a 5s deadline; hard-stop on timeout.
	stopped := make(chan struct{})
	go func() {
		n.server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		n.log.Log(CatMeshMsg, "[%s] mesh grpc hard-stop after GracefulStop timeout", n.hostID)
		n.server.Stop()
		<-stopped
	}
	return nil
}

// routeInboundFrame inspects a MeshFrame and dispatches it:
//
//   - ClientInput  → VCM.InjectInput (gateway→node I/O path)
//   - ClientFrame  → protocol error on a node (log and drop)
//   - ClientDisconnect → VCM.DropSession stub (T8 wires cell notification)
//   - PlayerAssignment with non-empty gateway_id → VCM.RegisterSession +
//     conn_id rewrite, then fall through to the cell inbox path
//   - all other variants → decodeMeshFrame + cell inbox delivery
//
// Dropped frames are logged; a full inbox logs and discards the message.
func (n *HostNetwork) routeInboundFrame(frame *meshpb.MeshFrame) error {
	// ── ClientInput: gateway forwarding client bytes to node ──────────────
	if ci := frame.GetClientInput(); ci != nil {
		if n.vcm == nil {
			n.log.Log(CatMeshMsg, "[%s] ClientInput received but no VCM configured, dropping", n.hostID)
			return nil
		}
		key := SessionKey{GatewayID: ci.GatewayId, ConnID: ci.ConnId}
		localID, ok := n.vcm.LookupByKey(key)
		if !ok {
			n.log.Log(CatMeshMsg, "[%s] ClientInput for unknown session gw=%s conn=%d, dropping", n.hostID, ci.GatewayId, ci.ConnId)
			return nil
		}
		n.vcm.InjectInput(localID, ci.Data)
		return nil
	}

	// ── ClientFrame: node→gateway direction — receiving on a node is wrong ─
	if frame.GetClientFrame() != nil {
		n.log.Log(CatMeshMsg, "[PROTO ERROR] [%s] unexpected ClientFrame received on node (protocol error), dropping", n.hostID)
		return nil
	}

	// ── ClientDisconnect: graceful disconnect notification from gateway ────
	// T8 will add cell-side MsgPlayerDisconnected. For now: drop VCM session.
	if cd := frame.GetClientDisconnect(); cd != nil {
		if n.vcm != nil {
			key := SessionKey{GatewayID: cd.GatewayId, ConnID: cd.ConnId}
			localID, ok := n.vcm.DropSession(key)
			if ok {
				n.log.Log(CatMeshMsg, "[%s] ClientDisconnect gw=%s conn=%d → localID=%d dropped (T8: cell notify pending)", n.hostID, cd.GatewayId, cd.ConnId, localID)
			} else {
				n.log.Log(CatMeshMsg, "[%s] ClientDisconnect for unknown session gw=%s conn=%d", n.hostID, cd.GatewayId, cd.ConnId)
			}
		}
		return nil
	}

	// ── PlayerAssignment with gateway_id: register VCM session, rewrite connID ─
	if pa := frame.GetPlayerAssignment(); pa != nil && pa.GatewayId != "" {
		if n.vcm != nil {
			key := SessionKey{GatewayID: pa.GatewayId, ConnID: pa.ConnId}
			// TODO(T7): pass real epoch from SessionAnnounce / PlayerAssignment once the handoff notification wiring lands.
			localID := n.vcm.RegisterSession(key, pa.Username, 1)
			// Construct a new PlayerAssignment rather than mutating the gRPC-owned
			// inbound proto. The gRPC runtime may retain the original buffer; logging
			// or retry paths would see corrupted values if we mutated in place.
			// Copy all fields explicitly — proto structs embed sync.Mutex so struct
			// assignment is unsafe.
			rewritten := &meshpb.PlayerAssignment{
				ConnId:       localID,
				GatewayId:    "", // cleared so downstream cell sees a plain assignment
				Username:     pa.Username,
				IsReconnect:  pa.IsReconnect,
				Data:         pa.Data,
				FromCellId:   pa.FromCellId,
				ToCellId:     pa.ToCellId,
			}
			frame.Msg = &meshpb.MeshFrame_PlayerAssignment{PlayerAssignment: rewritten}
			n.log.Log(CatMeshMsg, "[%s] PlayerAssignment gw=%s → localID=%d for %s", n.hostID, key.GatewayID, localID, pa.Username)
		}
		// Fall through to decodeMeshFrame below with the rewritten connID.
	}

	// ── All other variants: decode + deliver to destination cell ──────────
	msg, err := decodeMeshFrame(frame)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	destCellID := frame.DestCellId
	if destCellID == "" {
		return fmt.Errorf("missing dest_cell_id")
	}
	cell := n.host.CellByID(destCellID)
	if cell == nil {
		return fmt.Errorf("no cell %q on host %s", destCellID, n.hostID)
	}
	select {
	case cell.Inbox <- msg:
	default:
		n.log.Log(CatMeshMsg, "[%s] inbox full for %s, dropping %v", n.hostID, destCellID, msg.Type)
	}
	return nil
}
