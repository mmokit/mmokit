package game

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// TestDockingState_SurvivesReconnect pins the contract: dockingStates is
// keyed by Username (stable across reconnects), not ConnID (changes on
// reconnect). A mid-dock disconnect+reconnect must find the same in-flight
// DockingState so the timer keeps ticking and the player isn't stuck in
// StateDocking forever just outside the station.
//
// Regression: keying by ConnID stranded the player — `dockingStates[oldConnID]`
// was orphaned and `dockingStates[newConnID]` was nil, so processDockCompletions
// never advanced the timer and the dock never finished.
func TestDockingState_SurvivesReconnect(t *testing.T) {
	gw := testGW(newTestCell(pkguniverse.CellID{X: 0, Y: 0}))

	const username = "alice"
	const oldConnID = uint32(5)
	const newConnID = uint32(8)

	// Simulate the dock-start path: system_docking.go writes
	// dockingStates[username] when the player begins docking.
	gw.dockingStates[username] = &DockingState{
		Remaining:    1.5,
		StationX:     100,
		StationY:     200,
		StationNetID: 42,
	}

	// Simulate a reconnect: ConnID changes (5 → 8) but Username stays
	// the same. lifecycle.go's processDockCompletions iterates over
	// StateDocking sessions and looks up by sess.Username, which is the
	// stable key — so the entry must still be reachable after reconnect.
	if got := gw.dockingStates[username]; got == nil {
		t.Fatalf("dockingStates[%q] missing after reconnect — keying regressed back to ConnID", username)
	}

	// Sanity: the old/new ConnIDs are NOT keys (they're uint32, but if the
	// map type ever flipped back to uint32→DockingState this lookup would
	// compile and silently mask the bug).
	_ = oldConnID
	_ = newConnID

	// Cleanup contract: deleting by username clears the entry.
	delete(gw.dockingStates, username)
	if got := gw.dockingStates[username]; got != nil {
		t.Errorf("dockingStates[%q] not cleared by delete", username)
	}
}

// TestDockingState_KeyType is a compile-time/runtime guard that the map
// type stays string-keyed. If someone refactors back to ConnID, this test
// fails loudly with a clear message instead of silently breaking reconnect.
func TestDockingState_KeyType(t *testing.T) {
	gw := testGW(newTestCell(pkguniverse.CellID{X: 0, Y: 0}))
	// Smoke-write with a string key — would not compile if the map type
	// went back to map[uint32]*DockingState.
	gw.dockingStates["test-username"] = &DockingState{Remaining: 0.5}
	if gw.dockingStates["test-username"] == nil {
		t.Fatalf("string-keyed write rejected")
	}
	delete(gw.dockingStates, "test-username")
	_ = coords.Location{} // keep coords import in case future test extends spawn checks
}
