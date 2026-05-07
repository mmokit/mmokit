package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/service"
)

func TestProcess_ImplementsLocalProcess(t *testing.T) {
	var lp cmdsys.LocalProcess = (*Process)(nil)
	_ = lp
}

type playerArgs struct {
	Username string
}

func TestResolve_PlayerHomeOrOwner_Online(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.setActiveUserHost("alice", "host_a")
	c.registerLiveHost("host_a", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "alice"}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_a" {
		t.Fatalf("Resolve(online) = %+v, want one target host_a", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_FallsBackToDBHost(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	c.registerLiveHost("host_b", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_b" {
		t.Fatalf("Resolve(offline) = %+v, want one target host_b", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_NoDBHost(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	r := newMeshRouteResolver(c)
	_, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"}, "")
	if err == nil {
		t.Fatalf("Resolve(no-db) should have returned ErrRouteNoOwner")
	}
}

func TestResolve_Service_NoneRegistered_ReturnsError(t *testing.T) {
	// When no instance of the requested service kind is registered anywhere
	// the resolver must surface an error instead of silently falling back to
	// local execution. The previous fall-back masked real misconfigurations
	// (e.g. ServiceAnnounce never reached the coordinator) because the local
	// stub handler then returned an opaque "service not initialized" error.
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	r := newMeshRouteResolver(c)
	if _, err := r.Resolve(cmdsys.RouteService, "chat.channel.list", struct{}{}, "chat"); err == nil {
		t.Fatalf("Resolve(no-instance) should return an error so missing-instance is visible")
	}
}

func TestResolve_Service_FromCoordRegistry(t *testing.T) {
	c := &Process{
		Cells:         map[MeshCellID]*Cell{},
		hostRegistry:  NewHostRegistry(logger.New()),
		coordServices: service.NewCoordRegistry(),
	}
	if err := c.coordServices.Register(service.CoordInstance{
		Kind:       "chat",
		InstanceID: "chat-1",
		HostID:     "host_b",
		OpCodes:    []uint32{4000},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RouteService, "chat.channel.list", struct{}{}, "chat")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Kind != cmdsys.RouteSpecificHost || got[0].ID != "host_b" {
		t.Fatalf("Resolve(coord) = %+v, want one RouteSpecificHost host_b", got)
	}
}

func TestResolve_Service_FromRoutingIndex(t *testing.T) {
	c := &Process{
		Cells:          map[MeshCellID]*Cell{},
		hostRegistry:   NewHostRegistry(logger.New()),
		serviceRouting: service.NewRoutingIndex(),
	}
	if err := c.serviceRouting.Apply([]service.ServiceRecord{{
		Kind:       "auth",
		InstanceID: "auth-1",
		HostID:     "host_a",
		OpCodes:    []uint32{4100},
	}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RouteService, "auth.user.info", struct{ Username string }{Username: "x"}, "auth")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Kind != cmdsys.RouteSpecificHost || got[0].ID != "host_a" {
		t.Fatalf("Resolve(router) = %+v, want one RouteSpecificHost host_a", got)
	}
}

func TestResolve_Service_MissingService_ReturnsError(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	r := newMeshRouteResolver(c)
	if _, err := r.Resolve(cmdsys.RouteService, "chat.do.thing", struct{}{}, ""); err == nil {
		t.Fatalf("Resolve(empty service) should have errored")
	}
}
