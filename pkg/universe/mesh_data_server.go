package universe

import (
	"errors"
	"io"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
)

// meshDataServer is the server-side implementation of the MeshData gRPC
// service. It receives bidi streams from peer hosts, calls
// HostNetwork.routeInboundFrame for each received frame, and never
// sends anything back on this stream (outbound sends use the peer
// client stream owned by HostNetwork.ConnectPeer).
type meshDataServer struct {
	meshpb.UnimplementedMeshDataServer // embed for forward compat
	net *HostNetwork
}

// Data is the bidi streaming RPC. Despite being a bidi stream, the
// server end only reads — each host's outbound path is always a
// *client* stream opened via ConnectPeer. Separating the directions
// keeps the topology a clean full-mesh of client-server pairs.
func (s *meshDataServer) Data(stream meshpb.MeshData_DataServer) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := s.net.routeInboundFrame(frame); err != nil {
			s.net.log.Log(CatMeshMsg, "[%s] mesh server recv route error: %v", s.net.hostID, err)
		}
	}
}
