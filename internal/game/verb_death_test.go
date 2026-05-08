package game

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestKilled_NPC_NoDropsIsSafe verifies the Killed handler short-circuits
// cleanly when the dying NPC has no drop table entry. The kindType 9999
// is unmapped in NPCDropTables, so handleNPCKilled hits the table-miss
// path and returns without enqueuing loot.
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

	// No panic, no drops, no loot crate enqueued for an unknown kind.
	drops := mmokit.Peek[PendingLootDrop](gw.Queue)
	if len(drops) != 0 {
		t.Fatalf("PendingLootDrop queue: got %d, want 0 (no drop table for kind)", len(drops))
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

	pdata := gw.PlayerDB.GetOrCreate("alice")
	if got := pdata.GetCurrency(1); got != 50 {
		t.Fatalf("currency balance: got %d, want 50", got)
	}
}
