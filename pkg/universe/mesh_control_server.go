package universe

import (
	"errors"
	"fmt"
	"io"
	"sync"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

// recvResult holds the result of a single stream.Recv() call.
type recvResult struct {
	msg *meshpb.HostMessage
	err error
}

// meshControlServer implements meshpb.MeshControl. It accepts bidi
// streams from remote nodes, dispatches inbound HostMessage variants
// to the HostRegistry (Task 3), and drives assignment via the
// assignmentEngine (Task 5). One instance per coordinator process.
//
// In this task (S4 Task 2) it is a skeleton: it accepts a stream,
// requires RegisterHost as the first message, stores the stream,
// and drains subsequent messages without acting on them. Real
// handlers land in Tasks 5, 7, 8.
type meshControlServer struct {
	meshpb.UnimplementedMeshControlServer // forward-compat
	coord    *Coordinator
	log      *logger.Logger
	registry *HostRegistry
	engine   *assignmentEngine

	mu         sync.RWMutex
	streams    map[string]meshpb.MeshControl_ControlServer // hostID -> stream
	streamMu   map[string]*sync.Mutex                       // per-stream send mutex
	streamKill map[string]chan struct{}                      // hostID -> kill signal from `host kill` cmd
}

// Control is the bidi streaming RPC entry point. The first message
// MUST be RegisterHost; anything else is rejected with an error.
// Subsequent messages are dispatched: CellReady and CellStopped update
// the HostRegistry. Heartbeat (Task 8) and GracefulLeave (Task 9) are
// still handled by the default log-and-ignore case.
func (s *meshControlServer) Control(stream meshpb.MeshControl_ControlServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	reg := first.GetRegister()
	if reg == nil {
		return fmt.Errorf("mesh control: first message must be RegisterHost, got %T", first.Msg)
	}
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
	if err := s.sendCoordMessage(hostID, ack); err != nil {
		s.log.Log(CatMeshCell, "coordinator: RegisterAck to %s failed: %v", hostID, err)
		return err
	}

	if s.engine != nil {
		s.engine.onHostRegistered(host)
		// Send an initial PeerList so the new host knows about its peers
		// (and their cell ownership) before the settle window closes and
		// the first real rebalance runs. Targeted send to this one host.
		if initial := s.engine.buildPeerList(); initial != nil {
			if err := s.sendCoordMessage(hostID, initial); err != nil {
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
		// path (Task 8) pick up the orphaned cells on its next tick.
		// We could trigger the reassignment directly here, but going
		// through the watcher keeps one code path responsible for
		// crash recovery, which simplifies reasoning.
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

// sendCoordMessage pushes a CoordMessage onto the given host's control
// stream. Returns an error if there is no stream for the host or the
// Send call fails. Uses a per-stream mutex because grpc-go server
// streams are not safe for concurrent Send.
func (s *meshControlServer) sendCoordMessage(hostID string, msg *meshpb.CoordMessage) error {
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
