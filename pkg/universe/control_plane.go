package universe

import (
	stdnet "net"

	"google.golang.org/grpc"

	"github.com/zenion/mmoserver/pkg/logger"
)

// ControlPlane holds the state belonging to the RoleCoordinator role.
// Always present on every Process. Fields migrate here from Coordinator
// across Phases 1-5 of the role-separation refactor.
type ControlPlane struct {
	log *logger.Logger

	hostRegistry    *HostRegistry
	gatewayRegistry *GatewayRegistry

	controlServer     *meshControlServer
	controlGrpcServer *grpc.Server
	controlListener   stdnet.Listener

	controlClient *meshControlClient

	assignmentEngine *assignmentEngine
}

func newControlPlane(log *logger.Logger) *ControlPlane {
	return &ControlPlane{log: log}
}
