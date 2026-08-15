package tunectl_test

import (
	"testing"

	"github.com/mmokit/mmokit"
	"github.com/mmokit/mmokit/pkg/cmdsys"
)

func TestTuneVerbsRegistered(t *testing.T) {
	proc := mmokit.New(mmokit.Config{Name: "tunetest", Headless: true})
	for _, verb := range []string{"tune.list", "tune.get", "tune.set", "tune.reset"} {
		if _, ok := proc.CmdRegistry().Lookup(verb); !ok {
			t.Fatalf("verb %q not registered", verb)
		}
	}
}

// Every tune verb must fan out to the hosts that own cells: the per-process
// tunable registry is populated only by SyncCellTunables on cell boot, so a
// pure-coordinator process (distributed mode) has an empty registry. A
// RouteLocal read there returns nothing — the bug that made `tune list` come
// back empty on the coordinator console and the admin /tunables page.
func TestTuneVerbsRouteAllHosts(t *testing.T) {
	proc := mmokit.New(mmokit.Config{Name: "tunetest-route", Headless: true})
	for _, verb := range []string{"tune.list", "tune.get", "tune.set", "tune.reset"} {
		cmd, ok := proc.CmdRegistry().Lookup(verb)
		if !ok {
			t.Fatalf("verb %q not registered", verb)
		}
		if cmd.Route != cmdsys.RouteAllHosts {
			t.Errorf("verb %q route = %v, want RouteAllHosts", verb, cmd.Route)
		}
	}
}
