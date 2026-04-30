package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/logger"
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
		Cells:        map[string]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.setActiveUserHost("alice", "host_a")
	c.registerLiveHost("host_a", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "alice"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_a" {
		t.Fatalf("Resolve(online) = %+v, want one target host_a", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_FallsBackToDBHost(t *testing.T) {
	c := &Process{
		Cells:        map[string]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	c.registerLiveHost("host_b", true)
	r := newMeshRouteResolver(c)
	got, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].ID != "host_b" {
		t.Fatalf("Resolve(offline) = %+v, want one target host_b", got)
	}
}

func TestResolve_PlayerHomeOrOwner_Offline_NoDBHost(t *testing.T) {
	c := &Process{
		Cells:        map[string]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	r := newMeshRouteResolver(c)
	_, err := r.Resolve(cmdsys.RoutePlayerHomeOrOwner, "player.tp", playerArgs{Username: "bob"})
	if err == nil {
		t.Fatalf("Resolve(no-db) should have returned ErrRouteNoOwner")
	}
}
