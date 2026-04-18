package universe

import (
	stdnet "net"
	"sync"

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

	// Phase 2 migration bridges: ControlPlane reads Coordinator's raw
	// maps through these pointers while the fields live on Coordinator.
	// Removed in Phase 6 after the raw maps are deleted.
	cellToHostMapRef *map[string]string
	coordMuRef       *sync.RWMutex
}

func newControlPlane(log *logger.Logger) *ControlPlane {
	return &ControlPlane{log: log}
}

// OwnerOf returns the host currently owning cellKey, or ("", false) if
// no host owns it. Unified view: consults hostRegistry first (the
// authoritative source in distributed deployments), falls back to the
// coord's cellToHostMap (populated by Build() for local hosts and by
// applyPeerList on remote hosts).
func (c *ControlPlane) OwnerOf(cellKey string) (string, bool) {
	if c.hostRegistry != nil {
		if h := c.hostRegistry.HostForCell(cellKey); h != "" {
			return h, true
		}
	}
	// cellToHostMap fallback — read with the parent coord's mu.
	// During Phase 2 migration this still lives on Coordinator, so we
	// pass through to the coord via a field the coord sets at init.
	if c.cellToHostMapRef != nil {
		c.coordMuRef.RLock()
		h, ok := (*c.cellToHostMapRef)[cellKey]
		c.coordMuRef.RUnlock()
		return h, ok
	}
	return "", false
}

// AllCells iterates every (cellKey, hostID) pair currently known. Union
// of hostRegistry ownership and cellToHostMap entries.
func (c *ControlPlane) AllCells(yield func(cellKey, hostID string) bool) {
	seen := make(map[string]struct{})
	if c.hostRegistry != nil {
		for _, h := range c.hostRegistry.LiveHosts() {
			for cellID := range h.OwnedCells {
				if _, dup := seen[cellID]; dup {
					continue
				}
				seen[cellID] = struct{}{}
				if !yield(cellID, h.ID) {
					return
				}
			}
		}
	}
	if c.cellToHostMapRef != nil {
		c.coordMuRef.RLock()
		defer c.coordMuRef.RUnlock()
		for k, v := range *c.cellToHostMapRef {
			if _, dup := seen[k]; dup {
				continue
			}
			if !yield(k, v) {
				return
			}
		}
	}
}

// CellsOwnedBy iterates cell keys owned by the named host. Empty if
// hostID is unknown.
func (c *ControlPlane) CellsOwnedBy(hostID string, yield func(cellKey string) bool) {
	c.AllCells(func(cellKey, owner string) bool {
		if owner != hostID {
			return true
		}
		return yield(cellKey)
	})
}
