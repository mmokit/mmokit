package game

import (
	"reflect"
	"testing"

	"github.com/mmokit/mmokit"
)

// schemaFor derives the protocol schema from a process that has run register
// and nothing else — deliberately WITHOUT Build().
//
// Not calling Build is the point twice over. It is what a gateway effectively
// is for schema purposes (no cells), so the derivation is exercised in exactly
// the configuration that used to return nothing. And GameSetup boots systems
// whose Init resolves mmokit.State[GameWorld], which needs a state factory and
// a database this test has no business standing up.
func schemaFor(t *testing.T, register func(*mmokit.Process)) mmokit.ProtocolSchema {
	t.Helper()
	p := mmokit.New(mmokit.Config{CellsX: 1, CellsY: 1, TickRate: 20, Headless: true})
	proto := mmokit.NewProtocol(p, "space")
	register(p)
	proto.AssembleFromProcess(p)
	return proto.Schema()
}

// The client-visible protocol must not depend on which roles a process runs.
//
// examples/space used to gate game.GameSetup behind needsGameState, which is
// roles.Has(RoleHost) — and space's justfile launches the client-terminating
// process as --mode=gateway,service, which has no RoleHost. Measured before
// the split, a gateway reported 0 of 12 entities, 0 of 6 broadcast types,
// 2 of 15 client inputs, 7 of 19 server events and 5 of 12 operations. The
// gateway is what clients connect to, so the contract they were validated
// against was the emptiest one in the cluster.
//
// GameProtocol is the half that must run everywhere; GameSystems is the half
// that needs cells. This asserts the split is on the right line: registering
// only the protocol half yields the same schema as registering both.
func TestGameProtocolAloneYieldsTheFullSchema(t *testing.T) {
	protocolOnly := schemaFor(t, GameProtocol)
	everything := schemaFor(t, GameSetup)

	if !reflect.DeepEqual(protocolOnly, everything) {
		for _, c := range []struct {
			name        string
			gotN, wantN int
		}{
			{"entities", len(protocolOnly.Entities), len(everything.Entities)},
			{"broadcast_types", len(protocolOnly.BroadcastTypes), len(everything.BroadcastTypes)},
			{"client_input_types", len(protocolOnly.ClientInputTypes), len(everything.ClientInputTypes)},
			{"server_event_types", len(protocolOnly.ServerEventTypes), len(everything.ServerEventTypes)},
			{"operations", len(protocolOnly.Operations), len(everything.Operations)},
		} {
			if c.gotN != c.wantN {
				t.Errorf("%s: GameProtocol alone has %d, GameSetup has %d — a registration is on the wrong side of the split",
					c.name, c.gotN, c.wantN)
			}
		}
		t.Fatal("GameProtocol alone does not yield the same schema as GameSetup")
	}

	// Guard against the test passing because both are empty.
	if len(everything.Entities) == 0 || len(everything.Operations) == 0 {
		t.Fatal("schema is empty — this test would pass for the wrong reason")
	}
}
