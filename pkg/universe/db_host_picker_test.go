package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/logger"
)

func TestPickDBHost_PrefersLexFirstWithDB(t *testing.T) {
	c := &Process{
		Cells:        map[string]*Cell{},
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
		Cells:        map[string]*Cell{},
		hostRegistry: NewHostRegistry(logger.New()),
	}
	c.registerLiveHost("host_a", false)
	if got := c.PickDBHost(); got != "" {
		t.Fatalf("PickDBHost() with no DB host = %q, want empty", got)
	}
}
