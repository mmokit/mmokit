package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// TestHostDrainRegistration confirms the verb registers with the right
// shape. Behavior coverage (actual drain) is exercised by
// s7_graceful_shutdown_test.go which drives drainHost via GracefulLeave.
func TestHostDrainRegistration(t *testing.T) {
	t.Parallel()
	coord := newTestCoordWithStage(t, "0_0", "host-a")
	withFreshRegistry(coord)

	if err := registerHostBuiltins(coord.registry, coord); err != nil {
		t.Fatalf("registerHostBuiltins: %v", err)
	}

	cmd, ok := coord.registry.Lookup("host.drain")
	if !ok {
		t.Fatalf("host.drain not registered")
	}
	if cmd.Route != cmdsys.RouteLocal {
		t.Fatalf("host.drain route = %v, want RouteLocal", cmd.Route)
	}
	if cmd.Capability != "host.drain" {
		t.Fatalf("host.drain capability = %q, want host.drain", string(cmd.Capability))
	}
	schema, err := cmdsys.SchemaOf(cmd.Args)
	if err != nil {
		t.Fatalf("SchemaOf: %v", err)
	}
	hasHostID := false
	for _, f := range schema.Fields {
		if f.Name == "HostID" {
			hasHostID = true
			if !f.Required {
				t.Fatalf("HostID should be required by default")
			}
			break
		}
	}
	if !hasHostID {
		t.Fatalf("host.drain args missing HostID field; got %+v", schema.Fields)
	}
}
