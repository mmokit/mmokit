package game

import (
	"testing"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestKillCredit_FullChain exercises the death observer → Killed handler →
// KillCredit handler chain end-to-end on a single cell. Mirrors the
// cross-cell case (same-cell dispatches synchronously through the same
// handler code paths) without requiring two-cell scaffolding.
func TestKillCredit_FullChain(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)

	// Wire all three legs of the chain:
	//   observer fires Killed when Health <= 0
	//   killedHandler runs handleNPCKilled → currency drops dispatch KillCredit
	//   killCreditHandler credits the player's currency balance
	mmokit.Handle(gw.stage, killedHandler)
	mmokit.Handle(gw.stage, killCreditHandler)
	mmokit.OnTickEach(gw.stage, deathObserver)

	// Register a deterministic drop table for the test NPC kind. DropChance=1.0
	// + Min==Max ensures RollDrops returns exactly one currency entry, so the
	// chain is fully deterministic with no rand seeding required.
	const testKind uint8 = 200
	const dropAmount int32 = 17
	prev := NPCDropTables
	NPCDropTables = map[uint8]*DropTable{
		testKind: {
			Name: "test_kill_credit",
			Entries: []DropEntry{
				{
					ItemID:      item.CreditsItemID,
					DropChance:  1.0,
					MinQuantity: dropAmount,
					MaxQuantity: dropAmount,
				},
			},
		},
	}
	t.Cleanup(func() { NPCDropTables = prev })

	// Sanity-check the test fixture: CreditsItemID must classify as currency
	// in the item registry, otherwise handleNPCKilled would route the drop
	// through the loot-crate path instead of KillCredit.
	if !item.IsCurrency(item.CreditsItemID) {
		t.Fatal("test fixture broken: CreditsItemID must be a currency in item registry")
	}

	killer := newTestPlayerShip(t, gw, 303, "alice")
	target := newTestNPC(t, gw, 101, testKind)

	// Ensure the killer's authoritative cell can resolve Position/Stats/etc;
	// newTestPlayerShip builds the entity with PlayerConn, NetworkID, Health,
	// Shield, Position — sufficient for the killCreditHandler path.

	targetE := mmokit.EntityByNetID(gw.stage, target)
	killerE := mmokit.EntityByNetID(gw.stage, killer)
	if !killerE.Alive() {
		t.Fatal("killer entity not alive")
	}

	// Mark target as killed by killer. Death observer fires on next tick.
	h := mmokit.Get[component.Health](targetE)
	if h == nil {
		t.Fatal("target Health missing")
	}
	h.Current = 0
	h.LastDamagedByNetID = killer

	// Drive 1 tick: observer fires Killed; Killed handler runs Send
	// synchronously (same-cell); killCreditHandler runs synchronously and
	// credits the player's currency balance.
	runTickCallbacks(t, gw.stage, 1)

	pdata := gw.PlayerDB.Get("alice")
	if pdata == nil {
		t.Fatal("PlayerDB.Get(alice) returned nil; newTestPlayerShip should have bound the session")
	}
	if got := pdata.GetCurrency(item.CreditsItemID); got != int64(dropAmount) {
		t.Fatalf("KillCredit didn't credit currency: got=%d want=%d", got, dropAmount)
	}
}
