package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
)

// registerLiveHost is a test-only helper that injects a host record
// directly into the registry without going through RegisterHost. Used by
// PickDBHost / route-resolver unit tests to seed cluster state.
func (c *Process) registerLiveHost(id string, hasDB bool) {
	c.hostRegistry.RegisterLocal(id, "", nil, hasDB)
}

// setActiveUserHost is a test-only helper that injects an entry into
// the active-user → host map without going through notifySessionActive.
// Production code uses notifySessionActive (coordinator.go:779).
func (c *Process) setActiveUserHost(username, hostID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.players == nil {
		c.players = make(map[string]*PlayerLocation)
	}
	c.players[username] = &PlayerLocation{HostID: hostID, Active: true}
}

func TestPickDBHost_PrefersLexFirstWithDB(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_b", true)
	c.registerLiveHost("host_a", false)
	if got := c.PickDBHost(); got != "host_b" {
		t.Fatalf("PickDBHost() = %q, want host_b", got)
	}
	c.registerLiveHost("host_c", true)
	if got := c.PickDBHost(); got != "host_b" {
		t.Fatalf("after host_c registered, PickDBHost() = %q, want host_b (lex first DB-bearing)", got)
	}
}

func TestPickDBHost_NoneAvailable(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	if got := c.PickDBHost(); got != "" {
		t.Fatalf("PickDBHost() with no DB host = %q, want empty", got)
	}
}

func TestPickDBHost_LexicalOrderAcrossDBHosts(t *testing.T) {
	c := &Process{
		Cells:        map[MeshCellID]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	// Insert in non-sorted order to prove sort.Strings is doing the work.
	c.registerLiveHost("host_z", true)
	c.registerLiveHost("host_m", true)
	c.registerLiveHost("host_a", true)
	if got := c.PickDBHost(); got != "host_a" {
		t.Fatalf("PickDBHost() = %q, want host_a (lex-first across all DB hosts)", got)
	}
}
