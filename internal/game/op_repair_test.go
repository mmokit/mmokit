package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// setupRepairTest builds a player ship, wires sess.Entity (newTestPlayerShip
// skips this), transitions to StateDocked via the legal Active→Docking→Docked
// chain, and seeds the credit balance. Returns the entity + connID so the
// caller can mutate HP and read post-repair state.
func setupRepairTest(t *testing.T, gw *GameWorld, username string, netID uint32, credits int64) (mmokit.Entity, uint32, *PlayerData) {
	t.Helper()
	newTestPlayerShip(t, gw, netID, username)
	playerE := mmokit.EntityByNetID(gw.stage, netID)
	connID := netID

	sess := gw.Engine().Players.ByConnID(connID)
	sess.Entity = playerE.Handle()
	pdata := gw.PlayerDB.Get(username)
	pdata.AddCurrency(item.CreditsItemID, credits)

	_ = gw.Engine().Players.Transition(sess, mmokit.StateActive)
	_ = gw.Engine().Players.Transition(sess, StateDocking)
	_ = gw.Engine().Players.Transition(sess, StateDocked)

	return playerE, connID, pdata
}

// TestRepair_RestoresHPAndChargesCredits is the happy-path pin: docked
// player with missing HP + enough credits gets fully healed; credits drop
// by (missing × cost).
func TestRepair_RestoresHPAndChargesCredits(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	playerE, connID, pdata := setupRepairTest(t, gw, "carol", 501, 100)
	mmokit.Get[gamecomp.Health](playerE).Current = 60 // 40 missing

	res := gw.doRepair(connID)
	if !res.Success {
		t.Fatalf("repair failed: %s", res.Error)
	}
	if res.Cost != 40 {
		t.Errorf("Cost = %d, want 40 (40 missing × 1 cr/HP)", res.Cost)
	}
	if got := mmokit.Get[gamecomp.Health](playerE).Current; got != 100 {
		t.Errorf("Health.Current = %.0f, want 100", got)
	}
	if got := pdata.GetCurrency(item.CreditsItemID); got != 60 {
		t.Errorf("credits = %d, want 60 (100 − 40)", got)
	}
}

// TestRepair_RejectsWhenNotDocked guards the docked gate. Repair must only
// work at a station — otherwise players could heal mid-fight.
func TestRepair_RejectsWhenNotDocked(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	netID := newTestPlayerShip(t, gw, 502, "dave")
	playerE := mmokit.EntityByNetID(gw.stage, netID)
	connID := uint32(502)

	mmokit.Get[gamecomp.Health](playerE).Current = 60
	pdata := gw.PlayerDB.Get("dave")
	pdata.AddCurrency(item.CreditsItemID, 100)

	sess := gw.Engine().Players.ByConnID(connID)
	sess.Entity = playerE.Handle()
	_ = gw.Engine().Players.Transition(sess, mmokit.StateActive)
	if state := mmokit.PlayerStateOf(playerE); state != engine.StateActive {
		t.Fatalf("expected StateActive precondition; got %d", state)
	}

	res := gw.doRepair(connID)
	if res.Success {
		t.Errorf("repair succeeded while not docked; expected rejection")
	}
	if got := mmokit.Get[gamecomp.Health](playerE).Current; got != 60 {
		t.Errorf("Health.Current = %.0f, want 60 (no repair while undocked)", got)
	}
	if got := pdata.GetCurrency(item.CreditsItemID); got != 100 {
		t.Errorf("credits = %d, want 100 (no charge on rejected repair)", got)
	}
}

// TestRepair_RejectsInsufficientCredits — player credit balance < cost
// gets a clean error, no partial deduction, no HP restored.
func TestRepair_RejectsInsufficientCredits(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	playerE, connID, pdata := setupRepairTest(t, gw, "eve", 503, 5)
	mmokit.Get[gamecomp.Health](playerE).Current = 10 // missing 90

	res := gw.doRepair(connID)
	if res.Success {
		t.Errorf("repair succeeded with insufficient credits; expected rejection")
	}
	if got := mmokit.Get[gamecomp.Health](playerE).Current; got != 10 {
		t.Errorf("Health.Current = %.0f, want 10 (no HP change on rejection)", got)
	}
	if got := pdata.GetCurrency(item.CreditsItemID); got != 5 {
		t.Errorf("credits = %d, want 5 (no charge on rejected repair)", got)
	}
}

// TestRepair_NoOpWhenFullHP — calling repair at max HP is a clean no-op
// success (cost 0). The UI hides the button in this state but the server
// still needs to handle a redundant click gracefully.
func TestRepair_NoOpWhenFullHP(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	playerE, connID, pdata := setupRepairTest(t, gw, "frank", 504, 100)
	// Health already at max from newTestShip.

	res := gw.doRepair(connID)
	if !res.Success {
		t.Errorf("expected no-op success at full HP; got error %q", res.Error)
	}
	if res.Cost != 0 {
		t.Errorf("Cost = %d, want 0 (no missing HP)", res.Cost)
	}
	if got := mmokit.Get[gamecomp.Health](playerE).Current; got != 100 {
		t.Errorf("Health.Current = %.0f, want 100", got)
	}
	if got := pdata.GetCurrency(item.CreditsItemID); got != 100 {
		t.Errorf("credits = %d, want 100 (no charge for no-op)", got)
	}
}
