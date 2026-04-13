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
	"github.com/zenion/mmoserver/pkg/logger"
)

// meshControlClient is the node-side long-lived connection to the
// coordinator's MeshControl service. Dials on Start, opens a bidi
// Control stream, sends RegisterHost as the first message, and runs
// a background receive loop that dispatches CoordMessage variants
// to handlers. Heartbeat sending (Task 8) and CellAssign/Release
// handling (Task 7) are stubbed in this task.
//
// One instance per node process; stored on Coordinator when
// Mode == "node".
type meshControlClient struct {
	coord     *Coordinator
	log       *logger.Logger
	hostID    string
	coordAddr string

	conn   *grpc.ClientConn
	stream meshpb.MeshControl_ControlClient
	cancel context.CancelFunc

	sendMu sync.Mutex // protects stream.Send — grpc-go ClientStream is not safe for concurrent Send

	epochMu      sync.RWMutex
	highestEpoch uint64 // monotonic fencing token — reject CoordMessages with lower epochs
}

// newMeshControlClient constructs the client without dialing. Start
// opens the connection and kicks off the recv loop.
func newMeshControlClient(coord *Coordinator, hostID, coordAddr string) *meshControlClient {
	return &meshControlClient{
		coord:     coord,
		log:       coord.Log,
		hostID:    hostID,
		coordAddr: coordAddr,
	}
}

// Start dials the coordinator, opens the Control bidi stream, sends
// the initial RegisterHost message, and spawns the recv loop goroutine.
// Returns an error if dial, stream open, or the first Send fails;
// caller should propagate the error up (Build() panics on control
// plane setup failures in S4).
func (c *meshControlClient) Start(ctx context.Context) error {
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
		return fmt.Errorf("mesh control: dial %s: %w", c.coordAddr, err)
	}
	c.conn = conn

	streamCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	client := meshpb.NewMeshControlClient(conn)
	stream, err := client.Control(streamCtx)
	if err != nil {
		cancel()
		_ = conn.Close()
		return fmt.Errorf("mesh control: open stream: %w", err)
	}
	c.stream = stream

	// Resolve the local Host's grpc listen address so we can include
	// it in RegisterHost. The coordinator will distribute this to
	// peer hosts via PeerList (deferred to S4.5+) so they can dial
	// us for MeshData traffic.
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
		cancel()
		_ = conn.Close()
		return fmt.Errorf("mesh control: send RegisterHost: %w", err)
	}

	c.log.Log(CatMeshCell, "node: registering as %q to coordinator %s", c.hostID, c.coordAddr)

	go c.runRecvLoop()
	return nil
}

// send pushes a HostMessage onto the control stream. Uses sendMu
// because grpc-go client streams are not safe for concurrent Send.
func (c *meshControlClient) send(msg *meshpb.HostMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.stream == nil {
		return fmt.Errorf("mesh control: stream not ready")
	}
	return c.stream.Send(msg)
}

// Shutdown cancels the stream context and closes the underlying
// connection. Idempotent.
func (c *meshControlClient) Shutdown() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// runRecvLoop reads CoordMessages from the control stream and
// dispatches them. Exits on EOF or error. Enforces monotonic
// coord_epoch: any message with a strictly-smaller epoch than the
// highest seen is dropped with a warning.
func (c *meshControlClient) runRecvLoop() {
	for {
		msg, err := c.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.log.Log(CatMeshCell, "node: control stream closed (EOF)")
				return
			}
			c.log.Log(CatMeshCell, "node: control recv error: %v", err)
			return
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

// dispatch routes a received CoordMessage to the appropriate handler.
// CellAssign / CellRelease / NetIDRangeGrant are wired to real handlers
// that spawn/destroy cells. Heartbeat (Task 8) and GracefulLeave (Task 9)
// remain stubbed.
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
	default:
		c.log.Log(CatMeshMsg, "node: received %T (handler not wired)", v)
	}
}
