package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestKilled_NPC_NoDropsIsSafe verifies the Killed handler short-circuits
// cleanly when the dying NPC has no drop table entry. The kindType 9999
// is unmapped in NPCDropTables, so handleNPCKilled hits the table-miss
// path and returns without scheduling a SpawnLootCrate closure.
func TestKilled_NPC_NoDropsIsSafe(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, killedHandler)
	mmokit.Handle(gw.stage, killCreditHandler)

	target := newTestNPC(t, gw, 101, 99) // unmapped in NPCDropTables
	killer := newTestShip(t, gw, 202, 100, 0)

	targetE := mmokit.EntityByNetID(gw.stage, target)
	killerE := mmokit.EntityByNetID(gw.stage, killer)

	targetE.Send(&Killed{Killer: killerE})

	// Flush any deferred Commands closures. With no drop table the
	// handler never enqueues SpawnLootCrate, so the world stays free
	// of LootCrate entities.
	gw.stage.Commands().Flush()
	if mmokit.Any[gamecomp.LootCrate](gw.stage) {
		t.Fatal("no-drop-table NPC produced a loot crate")
	}
}

// TestKillCredit_SameCell_CreditsCurrency verifies the KillCredit handler
// credits the killer's player account. The killer is a player ship on the
// local cell, so the Send path runs the handler synchronously.
func TestKillCredit_SameCell_CreditsCurrency(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, killCreditHandler)

	killer := newTestPlayerShip(t, gw, 303, "alice")
	killerE := mmokit.EntityByNetID(gw.stage, killer)

	killerE.Send(&KillCredit{Currency: 1, Amount: 50})

	pdata := gw.PlayerDB.Get("alice")
	if pdata == nil {
		t.Fatal("PlayerDB.Get(alice) returned nil; newTestPlayerShip should have bound the session")
	}
	if got := pdata.GetCurrency(1); got != 50 {
		t.Fatalf("currency balance: got %d, want 50", got)
	}
}
