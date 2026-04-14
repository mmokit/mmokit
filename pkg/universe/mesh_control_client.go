package universe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// meshControlClient is the node-side long-lived connection to the
// coordinator's MeshControl service. Runs a persistent reconnect loop
// so the node can start before the coordinator is up, survives
// transient coordinator restarts, and keeps the control plane alive
// across network hiccups.
//
// Lifecycle:
//  1. Start(ctx) spawns runConnectLoop and returns immediately.
//  2. runConnectLoop dials the coordinator with exponential backoff.
//     On each successful connection it opens a Control bidi stream,
//     sends RegisterHost, re-announces any currently-owned cells via
//     CellReady (for coordinator restart recovery), and runs the recv
//     + heartbeat goroutines. When either loop returns (due to error,
//     EOF, or context cancel), the connection is torn down and the
//     outer loop retries.
//  3. Shutdown cancels the root context; the connect loop exits after
//     the current attempt finishes.
//
// Backoff: starts at 200ms, doubles up to a cap of 30s, with ±20%
// jitter. Reset to the minimum after every successful stream open.
//
// One instance per node process; stored on Coordinator when
// Mode == "node".
type meshControlClient struct {
	coord     *Coordinator
	log       *logger.Logger
	hostID    string
	coordAddr string

	rootCtx    context.Context
	rootCancel context.CancelFunc

	// connMu guards the per-connection state (conn, stream, streamCancel).
	// These are replaced on every successful reconnect.
	connMu       sync.Mutex
	conn         *grpc.ClientConn
	stream       meshpb.MeshControl_ControlClient
	streamCancel context.CancelFunc

	sendMu sync.Mutex // protects stream.Send — grpc-go ClientStream is not safe for concurrent Send

	epochMu      sync.RWMutex
	highestEpoch uint64 // monotonic fencing token — reject CoordMessages with lower epochs

	// done is closed when runConnectLoop exits. Shutdown waits for this
	// so the caller knows all goroutines have finished before returning.
	done chan struct{}
}

// Backoff tunables for the reconnect loop. Exported as package
// constants so tests can read them (but not override; they're
// time.Duration values and the test fixture doesn't need tunability).
const (
	connectBackoffMin = 200 * time.Millisecond
	connectBackoffMax = 30 * time.Second
	connectBackoffFactor = 2.0
)

// newMeshControlClient constructs the client without dialing. Start
// kicks off the reconnect loop which dials on its first iteration.
func newMeshControlClient(coord *Coordinator, hostID, coordAddr string) *meshControlClient {
	return &meshControlClient{
		coord:     coord,
		log:       coord.Log,
		hostID:    hostID,
		coordAddr: coordAddr,
		done:      make(chan struct{}),
	}
}

// Start spawns the reconnect loop and returns immediately. Never
// returns an error — connection failures are retried in the background
// with exponential backoff. The node process will appear "ready" from
// Build()'s perspective even if the coordinator isn't yet reachable;
// the control plane will come online asynchronously once the dial
// succeeds.
func (c *meshControlClient) Start(ctx context.Context) error {
	c.rootCtx, c.rootCancel = context.WithCancel(ctx)
	go c.runConnectLoop()
	return nil
}

// runConnectLoop is the outer reconnect loop. Each iteration attempts
// one full connection lifetime (dial → register → run → teardown).
// Exits when rootCtx is cancelled.
func (c *meshControlClient) runConnectLoop() {
	defer close(c.done)

	backoff := connectBackoffMin
	firstAttempt := true

	for {
		if c.rootCtx.Err() != nil {
			return
		}

		if firstAttempt {
			c.log.Log(CatMeshCell, "node: dialing coordinator %s", c.coordAddr)
			firstAttempt = false
		}

		err := c.runConnection()
		if c.rootCtx.Err() != nil {
			return
		}

		if err != nil {
			sleep := jitter(backoff)
			c.log.Log(CatMeshCell, "node: control connection failed (%v), retrying in %s", err, sleep.Round(time.Millisecond))
			if !sleepCtx(c.rootCtx, sleep) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// runConnection returned nil — clean EOF from the coordinator.
		// This happens on coordinator graceful shutdown; retry quickly
		// so we reconnect as soon as it comes back.
		c.log.Log(CatMeshCell, "node: control stream ended cleanly, reconnecting")
		backoff = connectBackoffMin
		if !sleepCtx(c.rootCtx, connectBackoffMin) {
			return
		}
	}
}

// runConnection attempts one full connection lifetime. Returns nil on
// clean EOF from the coordinator; returns an error on dial failure,
// stream open failure, or recv error. Never blocks after rootCtx is
// cancelled.
func (c *meshControlClient) runConnection() error {
	conn, err := grpc.NewClient(c.coordAddr,
		// TODO(mTLS): S4+ replaces insecure with mutual TLS once the
		// cert management layer lands. Acceptable here because S4 only
		// runs on localhost loopback for testing.
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

	// Store per-connection state for send() to use.
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

	// Send RegisterHost as the first message.
	grpcAddr := ""
	if host := c.coord.localHost(); host != nil && host.Network != nil {
		grpcAddr = host.Network.Addr()
	}
	reg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_Register{
			Register: &meshpb.RegisterHost{
				HostId:   c.hostID,
				GrpcAddr: grpcAddr,
			},
		},
	}
	if err := c.send(reg); err != nil {
		return fmt.Errorf("send RegisterHost: %w", err)
	}
	c.log.Log(CatMeshCell, "node: registered as %q to coordinator %s", c.hostID, c.coordAddr)

	// Re-announce any cells currently running locally so a restarted
	// coordinator can rebuild its view of our owned cells without
	// destroying and recreating them. On the first connection this
	// is a no-op (no local cells yet); on reconnect after coordinator
	// restart it's critical for continuity.
	c.reannounceOwnedCells()

	// Spawn heartbeat goroutine for this connection. It exits when
	// streamCtx is cancelled (which happens in the deferred cleanup
	// above, or from Shutdown via rootCtx cancellation).
	go c.runHeartbeatLoop(streamCtx)

	// Recv loop runs inline so the outer reconnect loop can observe
	// the error and decide whether to retry.
	return c.runRecvLoop()
}

// reannounceOwnedCells sends a CellReady for every cell currently
// running on the local host. Called immediately after RegisterHost
// so a restarted coordinator can rebuild its HostRegistry.OwnedCells
// view without destroying and recreating the cells.
func (c *meshControlClient) reannounceOwnedCells() {
	host := c.coord.localHost()
	if host == nil || len(host.Cells) == 0 {
		return
	}
	for _, cell := range host.Cells {
		msg := &meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellReady{
				CellReady: &meshpb.CellReady{
					HostId: c.hostID,
					CellId: cell.ID,
				},
			},
		}
		if err := c.send(msg); err != nil {
			c.log.Log(CatMeshCell, "node: re-announce CellReady for %s failed: %v", cell.ID, err)
			return
		}
		c.log.Log(CatMeshCell, "node: re-announced cell %s after reconnect", cell.ID)
	}
}

const heartbeatInterval = 1 * time.Second

// runHeartbeatLoop sends a Heartbeat every heartbeatInterval until
// ctx is cancelled. Started by runConnection with the stream context
// so a connection teardown cancels it cleanly.
func (c *meshControlClient) runHeartbeatLoop(ctx context.Context) {
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
						HostId: c.hostID,
						Tick:   0,
					},
				},
			}
			if err := c.send(hb); err != nil {
				// Heartbeat send failed — connection is broken.
				// The recv loop will observe the same error on its
				// next Recv and return, which drives the outer
				// reconnect loop. Nothing for us to do here.
				return
			}
		}
	}
}

// send pushes a HostMessage onto the current control stream. Uses
// sendMu because grpc-go client streams are not safe for concurrent
// Send. Returns an error if no stream is currently connected.
func (c *meshControlClient) send(msg *meshpb.HostMessage) error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		return fmt.Errorf("mesh control: stream not ready")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

// Shutdown cancels the reconnect loop and waits for it to exit.
// Idempotent.
func (c *meshControlClient) Shutdown() {
	if c.rootCancel != nil {
		c.rootCancel()
	}
	// Wait for runConnectLoop to exit so callers know the goroutine
	// tree is fully torn down. Bounded by the current connection
	// attempt's teardown path which is fast (connMu + Close).
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		c.log.Log(CatMeshCell, "node: control client shutdown timed out")
	}
}

// runRecvLoop reads CoordMessages from the current stream and
// dispatches them. Returns nil on clean EOF; returns an error on any
// other Recv failure. Enforces monotonic coord_epoch: any message
// with a strictly-smaller epoch than the highest seen is dropped
// with a warning.
func (c *meshControlClient) runRecvLoop() error {
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
				c.log.Log(CatMeshCell, "node: control stream closed (EOF)")
				return nil
			}
			if c.rootCtx.Err() != nil {
				// Shutdown triggered this; don't treat as error.
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}

		// Epoch enforcement. An epoch of 0 means "not carried" and we
		// let it pass (legacy / tests that don't set it).
		if msg.CoordEpoch > 0 {
			c.epochMu.Lock()
			if msg.CoordEpoch < c.highestEpoch {
				prev := c.highestEpoch
				c.epochMu.Unlock()
				c.log.Log(CatMeshCell, "node: dropping stale CoordMessage epoch=%d (highest=%d)", msg.CoordEpoch, prev)
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

// applyPeerList updates the node's local view of peer hosts and cell
// ownership from a coordinator broadcast. Called from the recv loop
// goroutine via dispatch.
//
// Peer reconciliation: connect to any host in the list we don't
// already have a stream to; disconnect from any peer we have that's
// not in the list. Skip our own host ID — we never connect to
// ourselves (same policy as the coordinator's cross-connect loop
// from S3 Task 7).
//
// Cell ownership: atomically replace the coordinator's cellToHostMap
// so the node's grpcBridge.resolveDest has a single consistent
// snapshot for routing decisions.
func (c *meshControlClient) applyPeerList(pl *meshpb.PeerList) {
	if pl == nil {
		return
	}

	host := c.coord.localHost()
	if host == nil || host.Network == nil {
		return
	}

	// Reconcile peer connections.
	wanted := make(map[string]string, len(pl.Hosts))
	for _, hr := range pl.Hosts {
		if hr.HostId == c.hostID {
			continue // skip self
		}
		wanted[hr.HostId] = hr.GrpcAddr
	}

	// Connect to new peers. ConnectPeer is idempotent per its S3
	// contract — it replaces any existing stream for the same host ID.
	for hid, addr := range wanted {
		if err := host.Network.ConnectPeer(hid, addr, peerKindNode); err != nil {
			c.log.Log(CatMeshCell, "node: ConnectPeer %s (%s) failed: %v", hid, addr, err)
		} else {
			c.log.Log(CatMeshCell, "node: connected to peer %s (%s)", hid, addr)
		}
	}

	// Disconnect peers not in the wanted set. Snapshot the existing
	// peer IDs under the network's lock, then call DisconnectPeer
	// outside the lock for the ones we want to drop.
	host.Network.mu.RLock()
	existing := make([]string, 0, len(host.Network.peers))
	for hid := range host.Network.peers {
		existing = append(existing, hid)
	}
	host.Network.mu.RUnlock()
	for _, hid := range existing {
		if _, keep := wanted[hid]; !keep {
			host.Network.DisconnectPeer(hid)
			c.log.Log(CatMeshCell, "node: disconnected from peer %s", hid)
		}
	}

	// Connect MeshData streams to any gateways in the peer list so outbound
	// ClientFrame delivery works. Gateways don't own cells, so skip them in
	// the cellToHostMap update below.
	wantedGateways := make(map[string]string, len(pl.Gateways))
	for _, gr := range pl.Gateways {
		if gr.GrpcAddr == "" {
			continue
		}
		wantedGateways[gr.GatewayId] = gr.GrpcAddr
	}
	for gwID, addr := range wantedGateways {
		if err := host.Network.ConnectPeer(gwID, addr, peerKindGateway); err != nil {
			c.log.Log(CatMeshCell, "node: ConnectPeer gateway %s (%s) failed: %v", gwID, addr, err)
		} else {
			c.log.Log(CatMeshCell, "node: connected to gateway %s (%s)", gwID, addr)
		}
	}

	// Atomically replace cellToHostMap. Guarded by the coordinator's
	// main mu RWMutex since grpcBridge.resolveDest reads from it on
	// the hot path.
	newMap := make(map[string]string, len(pl.Cells))
	for _, co := range pl.Cells {
		newMap[co.CellId] = co.HostId
	}
	c.coord.mu.Lock()
	c.coord.cellToHostMap = newMap
	c.coord.mu.Unlock()
	c.log.Log(CatMeshCell, "node: applied PeerList (%d peers, %d cells, %d gateways)", len(wanted), len(newMap), len(wantedGateways))
}

// dispatch routes a received CoordMessage to the appropriate handler.
// CellAssign / CellRelease / NetIDRangeGrant are wired to real handlers
// that spawn/destroy cells.
func (c *meshControlClient) dispatch(msg *meshpb.CoordMessage) {
	switch v := msg.Msg.(type) {
	case *meshpb.CoordMessage_RegisterAck:
		ack := v.RegisterAck
		if ack != nil && ack.Ok {
			c.log.Log(CatMeshCell, "node: registered successfully (epoch=%d)", msg.CoordEpoch)
		} else {
			reason := ""
			if ack != nil {
				reason = ack.Reason
			}
			c.log.Log(CatMeshCell, "node: registration rejected: %s", reason)
		}
	case *meshpb.CoordMessage_NetidRange:
		g := v.NetidRange
		// Apply the grant before the next CellAssign arrives. Node mode
		// creates cells lazily, so setting the base now ensures the
		// freshly-assigned cell uses this range. If multiple grants
		// arrive back-to-back, only the latest one takes effect — that's
		// intentional; coordinator issues one grant per host for S4.
		if g != nil && c.coord.netIDAlloc != nil {
			c.coord.netIDAlloc.SetBase(g.Start)
		}
		c.log.Log(CatMeshCell, "node: NetIDRangeGrant [%d..%d]", g.GetStart(), g.GetStart()+g.GetCount())

	case *meshpb.CoordMessage_CellAssign:
		assign := v.CellAssign
		if assign == nil {
			return
		}
		c.log.Log(CatMeshCell, "node: CellAssign %s", assign.CellId)
		go c.coord.assignCellOnNode(assign.CellId)

	case *meshpb.CoordMessage_CellRelease:
		rel := v.CellRelease
		if rel == nil {
			return
		}
		c.log.Log(CatMeshCell, "node: CellRelease %s", rel.CellId)
		go c.coord.releaseCellOnNode(rel.CellId)
	case *meshpb.CoordMessage_PeerList:
		c.applyPeerList(v.PeerList)
	case *meshpb.CoordMessage_UpstreamSwitch:
		// T7: log stub. In standalone gateway mode (T9) this will call
		// gateway.OnUpstreamSwitch to flip the session's upstream host.
		// In node mode (this client runs on a node, not a gateway) this
		// message should never arrive — log as unexpected.
		us := v.UpstreamSwitch
		if us != nil {
			c.log.Log(CatMeshMsg, "node: received UpstreamSwitch conn=%d -> host=%s epoch=%d (standalone gateway wiring is T9)",
				us.ConnId, us.NewHostId, us.NewEpoch)
		}
	default:
		c.log.Log(CatMeshMsg, "node: received %T (handler not wired)", v)
	}
}

// nextBackoff returns the next backoff duration, doubling up to the
// configured maximum.
func nextBackoff(current time.Duration) time.Duration {
	next := time.Duration(float64(current) * connectBackoffFactor)
	if next > connectBackoffMax {
		next = connectBackoffMax
	}
	return next
}

// jitter returns d ± 20% random variation. Prevents thundering-herd
// reconnects when many nodes retry against the same coordinator.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	spread := float64(d) * 0.4 // ±20%
	delta := (rand.Float64() - 0.5) * spread
	return d + time.Duration(delta)
}

// sleepCtx sleeps for the given duration or until ctx is cancelled.
// Returns false if the sleep was interrupted.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
