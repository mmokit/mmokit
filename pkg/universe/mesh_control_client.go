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
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// meshControlClient is the remote-host-side long-lived connection to the
// coordinator's MeshControl service. Runs a persistent reconnect loop
// so the host can start before the coordinator is up, survives
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
// One instance per node process; stored on Process when
// Mode == "node".
type meshControlClient struct {
	coord     *Process
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

	// streamReady is closed (idempotently via streamReadyOnce) the FIRST
	// time runConnection successfully opens a stream. send() uses this as
	// a one-shot gate to block briefly if a caller fires before the dial
	// loop has connected — closes the subscribe-flush-during-dial race
	// where service.Subscribe in Service.Init could fire its 50ms-debounced
	// flush before controlClient.Start finished its first dial.
	//
	// After first-open the channel stays closed forever; subsequent
	// reconnect cycles do NOT re-arm it. This is intentional — once the
	// Bus has flushed its full subscription set once, every later flush
	// re-ships the whole set, so a transient disconnect that drops a
	// flush is recovered by the next debounce window. Callers during
	// reconnect see the existing "stream not ready" error and rely on
	// debounce-resend.
	streamReady     chan struct{}
	streamReadyOnce sync.Once

	epochMu      sync.RWMutex
	highestEpoch uint64 // monotonic fencing token — reject CoordMessages with lower epochs

	// drainMu guards drainWaiter. Shutdown calls armDrainWaiter() before
	// sending GracefulLeave; the recv loop's dispatch path closes the
	// channel on CellsDrained.
	drainMu     sync.Mutex
	drainWaiter chan struct{}

	// done is closed when runConnectLoop exits. Shutdown waits for this
	// so the caller knows all goroutines have finished before returning.
	done chan struct{}

	// clusterClock is the shared cluster clock this host observes via
	// incoming CoordTimeSync broadcasts from the coordinator. Threaded
	// from Process.ClusterClock at construction time. Nil is tolerated
	// (dispatch path guards); tests may construct a client with only
	// clusterClock set to exercise the dispatch arm directly.
	clusterClock *ClusterClock
}

// Backoff tunables for the reconnect loop. Exported as package
// constants so tests can read them (but not override; they're
// time.Duration values and the test fixture doesn't need tunability).
const (
	connectBackoffMin    = 200 * time.Millisecond
	connectBackoffMax    = 30 * time.Second
	connectBackoffFactor = 2.0
)

// newMeshControlClient constructs the client without dialing. Start
// kicks off the reconnect loop which dials on its first iteration.
func newMeshControlClient(coord *Process, hostID, coordAddr string) *meshControlClient {
	return &meshControlClient{
		coord:        coord,
		log:          coord.Log,
		hostID:       hostID,
		coordAddr:    coordAddr,
		done:         make(chan struct{}),
		streamReady:  make(chan struct{}),
		clusterClock: coord.ClusterClock,
	}
}

// streamReadyTimeout is the upper bound send() will wait on the
// first-stream-open signal before giving up and returning "stream not
// ready". Picked to absorb a sluggish dial (gRPC with backoff jitter
// can take a few hundred ms to land) without blocking long enough to
// stall startServices on a coordinator that's genuinely unreachable.
const streamReadyTimeout = 5 * time.Second

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
			c.log.Log(CatMeshCell, "host: dialing coordinator %s", c.coordAddr)
			firstAttempt = false
		}

		err := c.runConnection()
		if c.rootCtx.Err() != nil {
			return
		}

		if err != nil {
			sleep := jitter(backoff)
			c.log.Log(CatMeshCell, "host: control connection failed (%v), retrying in %s", err, sleep.Round(time.Millisecond))
			if !sleepCtx(c.rootCtx, sleep) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// runConnection returned nil — clean EOF from the coordinator.
		// This happens on coordinator graceful shutdown; retry quickly
		// so we reconnect as soon as it comes back.
		c.log.Log(CatMeshCell, "host: control stream ended cleanly, reconnecting")
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
		grpc.WithTransportCredentials(credentials.NewTLS(meshClientTLSConfig())),
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
	stream, err := client.Control(outgoingMeshMD(streamCtx, c.coord.cfg.ClusterSecret, c.hostID))
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

	// First-open signal: unblock any sender that's waiting on a
	// not-yet-ready stream. Idempotent across reconnects via Once.
	c.streamReadyOnce.Do(func() { close(c.streamReady) })

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
	// Service-only hosts (--mode=service alone) are dialable for
	// ServiceEvent peer-mesh but have no executor / systemDefs / VCM —
	// the assignment engine MUST skip them when distributing cells.
	serviceOnly := c.coord.roles.Has(RoleService) &&
		!c.coord.roles.Has(RoleHost) &&
		!c.coord.roles.Has(RoleGateway) &&
		!c.coord.roles.Has(RoleCoordinator)
	reg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_Register{
			Register: &meshpb.RegisterHost{
				HostId:      c.hostID,
				GrpcAddr:    grpcAddr,
				HasPlayerDb: c.coord.hasPlayerDB.Load(),
				ServiceOnly: serviceOnly,
			},
		},
	}
	if err := c.send(reg); err != nil {
		return fmt.Errorf("send RegisterHost: %w", err)
	}
	c.log.Log(CatMeshCell, "host: registered as %q to coordinator %s", c.hostID, c.coordAddr)

	// Re-announce any cells currently running locally so a restarted
	// coordinator can rebuild its view of our owned cells without
	// destroying and recreating them. On the first connection this
	// is a no-op (no local cells yet); on reconnect after coordinator
	// restart it's critical for continuity.
	c.reannounceOwnedCells()
	c.reannounceServices()

	// Spawn heartbeat goroutine for this connection. It exits when
	// streamCtx is cancelled (which happens in the deferred cleanup
	// above, or from Shutdown via rootCtx cancellation).
	go c.runHeartbeatLoop(streamCtx)

	// Recv loop runs inline so the outer reconnect loop can observe
	// the error and decide whether to retry.
	return c.runRecvLoop()
}

// reannounceServices replays this process's service registrations and its
// bus subscriptions after (re)registering with the coordinator. Both helpers
// already exist and are re-callable; they were only ever invoked once, from
// startServices, so a coordinator restart or a stream blip left the
// coordinator without either.
//
// One known wart: service.CoordRegistry.Register rejects a duplicate
// InstanceID, so re-announcing to a coordinator that still holds the entry
// logs a rejection. Harmless — the entry it is rejecting is the correct one.
func (c *meshControlClient) reannounceServices() {
	if c.coord == nil {
		return
	}
	if err := c.coord.announceServices(); err != nil {
		c.log.Log(CatServicesBus, "host: service re-announce failed: %v", err)
	}
	if c.coord.bus != nil {
		c.coord.sendServiceEventSubscribe(c.coord.bus.SubscribedTypeNames())
	}
}

// reannounceOwnedCells sends a CellReady for every cell currently
// running on the local host. Called immediately after RegisterHost
// so a restarted coordinator can rebuild its HostRegistry.OwnedCells
// view without destroying and recreating the cells.
func (c *meshControlClient) reannounceOwnedCells() {
	host := c.coord.localHost()
	if host == nil {
		return
	}
	// Snapshot under Host.mu so we don't race the executor's AddCell/RemoveCell.
	cellIDs := host.SnapshotCellIDs()
	if len(cellIDs) == 0 {
		return
	}
	for _, cellCellID := range cellIDs {
		cell := host.CellByCellID(cellCellID)
		if cell == nil {
			continue
		}
		msg := &meshpb.HostMessage{
			Msg: &meshpb.HostMessage_CellReady{
				CellReady: &meshpb.CellReady{
					HostId: c.hostID,
					CellId: string(cell.MeshID()),
				},
			},
		}
		if err := c.send(msg); err != nil {
			c.log.Log(CatMeshCell, "host: re-announce CellReady for %s failed: %v", cell.MeshID(), err)
			return
		}
		c.log.Log(CatMeshCell, "host: re-announced cell %s after reconnect", cell.MeshID())
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
						HostId:  c.hostID,
						Tick:    0,
						Metrics: c.buildCellMetricsReports(),
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

// buildCellMetricsReports snapshots every locally-owned cell's load metrics
// for inclusion in the next heartbeat. The coordinator caches these so the
// admin dashboard can display real entity counts / load / tick stats from
// remote hosts in distributed mode.
func (c *meshControlClient) buildCellMetricsReports() []*meshpb.CellMetricsReport {
	if c.coord == nil {
		return nil
	}
	c.coord.mu.RLock()
	defer c.coord.mu.RUnlock()
	if len(c.coord.Cells) == 0 {
		return nil
	}
	out := make([]*meshpb.CellMetricsReport, 0, len(c.coord.Cells))
	for id, node := range c.coord.Cells {
		if node == nil || node.Metrics == nil {
			continue
		}
		s := node.Metrics.Snapshot()
		out = append(out, &meshpb.CellMetricsReport{
			CellId:            string(id),
			CompositeLoad:     s.CompositeLoad,
			TickP99Us:         s.Tick.P99Duration.Microseconds(),
			TickP95Us:         s.Tick.P95Duration.Microseconds(),
			EntitiesReal:      int32(s.Entities.Real),
			EntitiesReplica:   int32(s.Entities.Replica),
			EntitiesGhost:     int32(s.Entities.Ghost),
			EntitiesConnected: int32(s.Entities.Connected),
			BytesSent:         s.Network.BytesSent,
			BytesRecv:         s.Network.BytesRecv,
		})
	}
	return out
}

// send pushes a HostMessage onto the current control stream. Uses
// sendMu because grpc-go client streams are not safe for concurrent
// Send. Returns an error if no stream is currently connected.
//
// First-open slow path: if the stream is nil AND streamReady has not
// yet been closed (the dial loop is still working on its first
// connect), block up to streamReadyTimeout for the signal. This closes
// the subscribe-flush-during-dial race — service.Subscribe in
// Service.Init can fire its 50ms-debounced flush before the dial loop
// has finished, and we want that flush to ride the first stream
// instead of being silently dropped.
//
// After the first stream opens (streamReady closed), this select falls
// through immediately on every later call, so reconnect cycles keep
// the existing fast-fail behavior — the Bus's flush is full-set and
// re-ships on the next debounce window if the current one fails.
func (c *meshControlClient) send(msg *meshpb.HostMessage) error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		// Slow path — wait for first-open signal. Once
		// streamReadyOnce fires, this select is "instant" forever.
		select {
		case <-c.streamReady:
		case <-c.rootCtxDone():
			return c.rootCtxErr()
		case <-time.After(streamReadyTimeout):
			return fmt.Errorf("mesh control: stream not ready (timeout after %s)", streamReadyTimeout)
		}
		// Re-load: the connection may have come up and gone down
		// again between the signal and this re-read on a flaky link.
		c.connMu.Lock()
		stream = c.stream
		c.connMu.Unlock()
		if stream == nil {
			return fmt.Errorf("mesh control: stream not ready")
		}
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

// sendIfReady sends msg only when the control stream is currently up,
// returning an error immediately (no reconnect wait) when it is down. For
// best-effort telemetry callers (admin topic events): unlike send, it does not
// wait up to streamReadyTimeout for a reconnect. Send may still block briefly
// on HTTP/2 flow control under sendMu, but never parks waiting for a stream to
// appear. Same sendMu serialization, so ordering is preserved relative to other
// senders.
func (c *meshControlClient) sendIfReady(msg *meshpb.HostMessage) error {
	c.connMu.Lock()
	stream := c.stream
	c.connMu.Unlock()
	if stream == nil {
		return fmt.Errorf("mesh control: stream down, message dropped")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return stream.Send(msg)
}

// rootCtxDone returns the rootCtx's Done channel, or a never-closed
// channel if rootCtx has not been initialized yet (Start not called).
// Defensive — in practice send is only invoked after Start because
// service.Subscribe runs in Process.Start.
func (c *meshControlClient) rootCtxDone() <-chan struct{} {
	if c.rootCtx == nil {
		return make(chan struct{}) // never closes
	}
	return c.rootCtx.Done()
}

// rootCtxErr mirrors rootCtxDone — returns ctx error or a generic
// "shutting down" error if rootCtx is nil.
func (c *meshControlClient) rootCtxErr() error {
	if c.rootCtx == nil {
		return fmt.Errorf("mesh control: not started")
	}
	return c.rootCtx.Err()
}

// armDrainWaiter installs a fresh channel that the dispatch loop closes
// when the next CellsDrained message arrives. Callers (Process.Shutdown
// in remote-host mode) use the returned channel to block until the coordinator
// reports every owned cell migrated, or a local timeout fires. If a prior
// waiter was never fulfilled (re-arm without intervening signalDrained),
// the stale channel is closed so any goroutine still parked on it can
// unblock — preventing a leak on repeated Shutdown attempts.
func (c *meshControlClient) armDrainWaiter() <-chan struct{} {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	if c.drainWaiter != nil {
		close(c.drainWaiter)
	}
	ch := make(chan struct{})
	c.drainWaiter = ch
	return ch
}

// signalDrained closes the currently armed drain waiter, if any. Safe to
// call with no waiter installed — the call is a no-op in that case.
// Called from the recv loop's dispatch path when CellsDrained arrives.
func (c *meshControlClient) signalDrained() {
	c.drainMu.Lock()
	defer c.drainMu.Unlock()
	if c.drainWaiter != nil {
		close(c.drainWaiter)
		c.drainWaiter = nil
	}
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
		c.log.Log(CatMeshCell, "host: control client shutdown timed out")
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
				c.log.Log(CatMeshCell, "host: control stream closed (EOF)")
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
				c.log.Log(CatMeshCell, "host: dropping stale CoordMessage epoch=%d (highest=%d)", msg.CoordEpoch, prev)
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

	// Build the combined set of peers we want to hold open: nodes AND
	// gateways. Previously only node peers were enumerated before the
	// disconnect loop below, so every PeerList treated the gateway peer
	// as "unwanted" and tore it down — then the gateway reconnect loop
	// (further down) immediately recreated it. The rip-reconnect happened
	// on EVERY broadcast (initial registration, cell assign, rebalance),
	// dropping any replication frames in the old outQ and causing
	// client-visible blackouts for every affected viewer.
	type wantedPeer struct {
		addr string
		kind peerKind
	}
	wantedAll := make(map[string]wantedPeer, len(pl.Hosts)+len(pl.Gateways))
	for _, hr := range pl.Hosts {
		if hr.HostId == c.hostID {
			continue // skip self
		}
		if hr.GrpcAddr == "" {
			continue
		}
		wantedAll[hr.HostId] = wantedPeer{addr: hr.GrpcAddr, kind: peerKindNode}
	}
	for _, gr := range pl.Gateways {
		if gr.GrpcAddr == "" {
			continue
		}
		wantedAll[gr.GatewayId] = wantedPeer{addr: gr.GrpcAddr, kind: peerKindGateway}
	}

	// Open MeshData streams to everything in wantedAll. ConnectPeer is
	// idempotent: if a peer with the same {hostID, grpcAddr, kind} is
	// already live, it's a no-op and the existing stream is preserved.
	for id, wp := range wantedAll {
		if err := host.Network.ConnectPeer(id, wp.addr, wp.kind); err != nil {
			c.log.Log(CatMeshCell, "host: ConnectPeer %s (%s kind=%d) failed: %v", id, wp.addr, wp.kind, err)
		}
	}

	// Disconnect peers that are NOT in wantedAll. Snapshot the existing
	// IDs under the network lock, then call DisconnectPeer outside it.
	host.Network.mu.RLock()
	existing := make([]string, 0, len(host.Network.peers))
	for hid := range host.Network.peers {
		existing = append(existing, hid)
	}
	host.Network.mu.RUnlock()
	for _, hid := range existing {
		if _, keep := wantedAll[hid]; !keep {
			host.Network.DisconnectPeer(hid)
			c.log.Log(CatMeshCell, "host: disconnected from peer %s", hid)
		}
	}

	// Atomically replace cellToHostMap. Guarded by Control.mu since
	// grpcBridge.resolveDest / OwnerOf / AllOwnedCells read from it.
	newMap := make(map[MeshCellID]string, len(pl.Cells))
	for _, co := range pl.Cells {
		// Wire boundary: pl.Cells[].CellId is the proto string carrying
		// mesh form. Cast to MeshCellID for the typed map key.
		newMap[MeshCellID(co.CellId)] = co.HostId
	}
	c.coord.Control.mu.Lock()
	c.coord.Control.cellToHostMap = newMap
	c.coord.Control.mu.Unlock()

	// Service framework: refresh the local routing index from the
	// announced services so this host's gateway-style routing decisions
	// stay current. No-op when serviceRouting is nil.
	c.coord.applyServicesToRoutingIndex(pl.Services)

	// Apply event-bus routing table from PeerList. Whole-map replace —
	// coord's snapshot is authoritative. The cache is read on every
	// service.Publish to resolve remote subscribers.
	if c.coord != nil && c.coord.bus != nil {
		table := make(map[string][]string, len(pl.GetEventRouting()))
		for typeName, procs := range pl.GetEventRouting() {
			table[typeName] = append([]string(nil), procs.GetProcessIds()...)
		}
		c.coord.bus.SetRoutingCache(table)
	}
	c.coord.mu.Lock()
	// Snapshot the local cell set under the same lock so we can reconcile
	// neighbors after release. Can't call reconcileCellNeighbors here
	// because it takes the same lock.
	localCells := make([]*Cell, 0, len(c.coord.Cells))
	for _, cc := range c.coord.Cells {
		localCells = append(localCells, cc)
	}
	c.coord.mu.Unlock()

	// Rebuild neighbor relationships on every local cell so newly-joined
	// remote cells become visible to the local border dispatcher as stub
	// neighbors, and removed ones are pruned. reconcileCellNeighbors also
	// invalidates each cell's cached borderDispatcher so the next
	// PostSystems tick rebuilds the CellViewer set.
	for _, cc := range localCells {
		c.coord.reconcileCellNeighbors(cc)
	}
	nodeCount, gwCount := 0, 0
	for _, wp := range wantedAll {
		if wp.kind == peerKindGateway {
			gwCount++
		} else {
			nodeCount++
		}
	}
	c.log.Log(CatMeshCell, "host: applied PeerList (%d peers, %d cells, %d gateways)", nodeCount, len(newMap), gwCount)
}

// dispatch routes a received CoordMessage to the appropriate handler.
// CellAssign / CellRelease / NetIDRangeGrant are wired to real handlers
// that spawn/destroy cells.
func (c *meshControlClient) dispatch(msg *meshpb.CoordMessage) {
	switch v := msg.Msg.(type) {
	case *meshpb.CoordMessage_RegisterAck:
		ack := v.RegisterAck
		if ack != nil && ack.Ok {
			c.log.Log(CatMeshCell, "host: registered successfully (epoch=%d)", msg.CoordEpoch)
		} else {
			reason := ""
			if ack != nil {
				reason = ack.Reason
			}
			c.log.Log(CatMeshCell, "host: registration rejected: %s", reason)
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
		c.log.Log(CatMeshCell, "host: NetIDRangeGrant [%d..%d]", g.GetStart(), g.GetStart()+g.GetCount())

	case *meshpb.CoordMessage_CellAssign:
		c.onCellAssign(v.CellAssign)

	case *meshpb.CoordMessage_CellRelease:
		rel := v.CellRelease
		if rel == nil {
			return
		}
		c.log.Log(CatMeshCell, "host: CellRelease %s (req=%d)", rel.CellId, rel.ReqId)
		go func(cellID string, reqID uint64) {
			c.coord.releaseCellOnNode(MeshCellID(cellID))
			if reqID != 0 {
				ack := &meshpb.HostMessage{
					Msg: &meshpb.HostMessage_HostOpAck{
						HostOpAck: &meshpb.HostOpAck{
							ReqId: reqID,
							Ok:    true,
						},
					},
				}
				if err := c.send(ack); err != nil {
					c.log.Log(CatMeshCell, "host: failed to send HostOpAck req=%d: %v", reqID, err)
				}
			}
		}(rel.CellId, rel.ReqId)
	case *meshpb.CoordMessage_CellRename:
		req := v.CellRename
		if req == nil {
			return
		}
		c.log.Log(CatMeshCell, "host: CellRename %s -> %s (req=%d)", req.FromCellId, req.ToCellId, req.ReqId)
		go func(from, to string, reqID uint64) {
			err := c.coord.renameCellOnNode(MeshCellID(from), MeshCellID(to))
			ok := err == nil
			errStr := ""
			if !ok {
				errStr = err.Error()
			}
			if reqID != 0 {
				ack := &meshpb.HostMessage{
					Msg: &meshpb.HostMessage_HostOpAck{
						HostOpAck: &meshpb.HostOpAck{
							ReqId: reqID,
							Ok:    ok,
							Error: errStr,
						},
					},
				}
				if err := c.send(ack); err != nil {
					c.log.Log(CatMeshCell, "host: failed to send HostOpAck req=%d: %v", reqID, err)
				}
			}
		}(req.FromCellId, req.ToCellId, req.ReqId)
	case *meshpb.CoordMessage_PeerList:
		c.applyPeerList(v.PeerList)
	case *meshpb.CoordMessage_UpstreamSwitch:
		// T7: log stub. In standalone gateway mode (T9) this will call
		// gateway.OnUpstreamSwitch to flip the session's upstream host.
		// In remote-host mode (this client runs on a host, not a gateway)
		// this message should never arrive — log as unexpected.
		us := v.UpstreamSwitch
		if us != nil {
			c.log.Log(CatMeshMsg, "host: received UpstreamSwitch conn=%d -> host=%s epoch=%d (standalone gateway wiring is T9)",
				us.ConnId, us.NewHostId, us.NewEpoch)
		}

	case *meshpb.CoordMessage_CellTransfer:
		// S7: coord → source host command forwarding. Look up the local
		// executor and run Execute with the reconstructed Go command.
		ct := v.CellTransfer
		if ct == nil {
			return
		}
		host := c.coord.localHost()
		if host == nil {
			c.log.Log(CatMeshCell, "host: CellTransfer req=%d but no local host", ct.RequestId)
			return
		}
		exec := c.coord.localHostExecutor(host.ID)
		if exec == nil {
			c.log.Log(CatMeshCell, "host: CellTransfer req=%d but no executor on host %s", ct.RequestId, host.ID)
			return
		}
		cmd := commandFromProto(ct, host.ID)
		// Execute serializes on the cell's game loop and then ships the
		// populated CellTransfer to the destination host; do not block the
		// recv loop.
		go func() {
			if err := exec.Execute(cmd); err != nil {
				c.log.Log(CatMeshCell, "host: CellTransfer req=%d execute: %v", ct.RequestId, err)
				// Report failure upstream so the orchestrator rolls back
				// instead of waiting for timeout. Uses the destination cell
				// from the command — the orchestrator keys Ready on host+cell.
				_ = c.send(&meshpb.HostMessage{
					Msg: &meshpb.HostMessage_CellTransferReady{
						CellTransferReady: &meshpb.CellTransferReady{
							RequestId:  ct.RequestId,
							DestCellId: ct.DestCellId,
							HostId:     host.ID,
							Ok:         false,
							Error:      err.Error(),
						},
					},
				})
			}
		}()

	case *meshpb.CoordMessage_CellsDrained:
		// S7-T7: coordinator has finished draining every cell previously
		// owned by this host. Wake the Shutdown caller blocking in
		// armDrainWaiter so it can proceed with teardown.
		cd := v.CellsDrained
		if cd != nil {
			c.log.Log(CatMeshCell, "host: received CellsDrained for host %s", cd.HostId)
		}
		c.signalDrained()

	case *meshpb.CoordMessage_CellTransferAbort:
		cta := v.CellTransferAbort
		if cta == nil {
			return
		}
		host := c.coord.localHost()
		if host == nil {
			return
		}
		if exec := c.coord.localHostExecutor(host.ID); exec != nil {
			exec.Abort(cta)
		}

	case *meshpb.CoordMessage_CellTransferCommit:
		ctc := v.CellTransferCommit
		if ctc == nil {
			return
		}
		host := c.coord.localHost()
		if host == nil {
			return
		}
		if exec := c.coord.localHostExecutor(host.ID); exec != nil {
			exec.Commit(ctc.RequestId)
		}

	case *meshpb.CoordMessage_CommandRequest:
		// C3: coordinator dispatched a command to this host. Execute locally
		// and reply via HostMessage_CommandResponse.
		req := v.CommandRequest
		if req != nil && c.coord.dispatcher != nil {
			go c.handleInboundCommandRequest(req)
		}

	case *meshpb.CoordMessage_CommandResponse:
		// C3: response to a CommandRequest this host sent to the coordinator.
		resp := v.CommandResponse
		if resp != nil && c.coord.transport != nil {
			c.coord.transport.orch.OnResponse(resp)
		}

	case *meshpb.CoordMessage_CommandCancel:
		// C3: coordinator wants to cancel an in-flight handler on this host.
		cc := v.CommandCancel
		if cc != nil && c.coord.transport != nil {
			c.coord.transport.orch.OnCancel(cc.RequestId)
		}

	case *meshpb.CoordMessage_CoordTimeSync:
		// Replication Timeline Redesign: feed the cluster clock.
		// Nil-guard both sides: clusterClock may be nil on test
		// harnesses that construct a bare dispatcher, and the payload
		// wrapper may carry a nil inner message on malformed frames.
		if c.clusterClock != nil && v.CoordTimeSync != nil {
			c.clusterClock.Observe(v.CoordTimeSync.CoordTimeMs, v.CoordTimeSync.Seq)
		}

	case *meshpb.CoordMessage_SessionRegister:
		sr := v.SessionRegister
		if sr != nil && c.coord.vcm != nil {
			key := SessionKey{GatewayID: sr.GatewayId, ConnID: sr.ConnId}
			localID := c.coord.vcm.RegisterSession(key, sr.Username, sr.Epoch, MeshCellID(sr.CellId))
			c.log.Log(CatMeshCell, "host: SessionRegister gw=%s conn=%d → localID=%d epoch=%d cell=%s",
				sr.GatewayId, sr.ConnId, localID, sr.Epoch, sr.CellId)
		}

	default:
		c.log.Log(CatMeshMsg, "host: received %T (handler not wired)", v)
	}
}

// onCellAssign applies a CellAssign from the coordinator. Extracted
// from the dispatch switch so it can be unit-tested without spinning
// up the full gRPC stack.
//
// Replication Timeline Redesign gate: refuse the assignment if the
// host's ClusterClock has not yet observed any CoordTimeSync. Without
// this, a race between host registration and cell dispatch could land
// a cell on this host before its clock is cluster-coherent, and the
// cell would emit replication samples stamped with local OS wall-time
// rather than cluster-coordinated wall-time. The coordinator's
// RegisterAck path sends the initial CoordTimeSync before any
// CellAssign can be dispatched (see mesh_control_server.go's
// handleHostControl), so this guard should never fire in production —
// it exists to catch orchestration regressions.
func (c *meshControlClient) onCellAssign(assign *meshpb.CellAssign) {
	if assign == nil {
		return
	}
	if c.clusterClock != nil && !c.clusterClock.Observed() {
		c.log.Log(CatMeshCell,
			"host: CellAssign %s rejected — ClusterClock not yet observed",
			assign.CellId)
		return
	}
	c.log.Log(CatMeshCell, "host: CellAssign %s", assign.CellId)
	if c.coord != nil {
		go c.coord.assignCellOnNode(MeshCellID(assign.CellId))
	}
}

// handleInboundCommandRequest executes an inbound CommandRequest that the
// coordinator dispatched to this node, then replies with a HostMessage
// containing the CommandResponse. Runs in a goroutine so the recv loop stays live.
func (c *meshControlClient) handleInboundCommandRequest(req *meshpb.CommandRequest) {
	ctx, cancel := context.WithDeadline(
		context.Background(),
		clampRemoteDeadline(req.DeadlineUnixNanos),
	)
	defer cancel()

	host := c.coord.localHost()
	hostID := c.hostID
	if host != nil {
		hostID = host.ID
	}

	resp := executeCommandRequest(ctx, c.coord.dispatcher, hostID, peerCoordinator, req)
	msg := &meshpb.HostMessage{
		Msg: &meshpb.HostMessage_CommandResponse{CommandResponse: resp},
	}
	if err := c.send(msg); err != nil {
		c.log.Log(CatMeshMsg, "host: CommandResponse send failed: %v", err)
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
