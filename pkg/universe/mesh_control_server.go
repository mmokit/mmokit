package universe

import (
	"errors"
	"fmt"
	"io"
	"sync"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/logger"
)

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
	coord *Coordinator
	log   *logger.Logger

	mu       sync.RWMutex
	streams  map[string]meshpb.MeshControl_ControlServer // hostID -> stream
	streamMu map[string]*sync.Mutex                       // per-stream send mutex
}

// Control is the bidi streaming RPC entry point. The first message
// MUST be RegisterHost; anything else is rejected with an error.
// Subsequent messages are logged but not dispatched — Task 5 adds
// the real handlers.
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
	s.mu.Lock()
	if old, ok := s.streams[hostID]; ok {
		_ = old // stale stream; new one replaces it
		s.log.Log(CatMeshCell, "coordinator: host %s replacing stale control stream", hostID)
	}
	s.streams[hostID] = stream
	s.streamMu[hostID] = &sync.Mutex{}
	s.mu.Unlock()
	s.log.Log(CatMeshCell, "coordinator: host %s registered from %s", hostID, reg.GrpcAddr)

	defer func() {
		s.mu.Lock()
		delete(s.streams, hostID)
		delete(s.streamMu, hostID)
		s.mu.Unlock()
	}()

	// Drain subsequent messages until EOF or error. Real dispatch
	// happens in later tasks; for now we just log.
	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.log.Log(CatMeshCell, "coordinator: host %s stream closed", hostID)
				return nil
			}
			s.log.Log(CatMeshCell, "coordinator: host %s recv error: %v", hostID, err)
			return err
		}
		s.log.Log(CatMeshMsg, "coordinator: host %s sent %T", hostID, msg.Msg)
	}
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
