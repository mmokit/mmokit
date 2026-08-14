package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
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

// TestPlayerKilled_SoulboundItemsStayOnPlayer pins the die-respawn-loot-fresh
// exploit fix: soulbound items (starter kit) must NOT appear in the death
// loot crate, and they must be persisted on the player's save so the
// respawn path restores them. The crate carries only the non-soulbound items.
func TestPlayerKilled_SoulboundItemsStayOnPlayer(t *testing.T) {
	gw, _ := newTestGameWorld()
	gw.stage.SetStateByName("game.GameWorld", gw)
	mmokit.Handle(gw.stage, killedHandler)

	netID := newTestPlayerShip(t, gw, 401, "bob")
	playerE := mmokit.EntityByNetID(gw.stage, netID)

	// Equip a soulbound weapon (Pulse Laser Array = 100) and a
	// non-soulbound weapon (Railgun System = 101 — not tagged).
	mmokit.Set(playerE, gamecomp.Equipment{
		Weapon1: 100, // soulbound starter
		Weapon2: 101, // non-soulbound, should drop
		Shield:  110, // soulbound starter
	})
	// Cargo: one soulbound (PlasmaCannon = 107) + one resource (Ore = 2).
	inv := gamecomp.Inventory{MaxMass: 100}
	inv.AddItem(107, 1) // soulbound
	inv.AddItem(2, 5)   // ore — should drop
	mmokit.Set(playerE, inv)

	// Drop HP to zero and fire Killed via the handler directly.
	mmokit.Get[gamecomp.Health](playerE).Current = 0
	playerE.Send(&Killed{Killer: playerE})
	gw.stage.Commands().Flush()

	// Inspect the loot crate (only non-soulbound items should be in it).
	var crateContents map[uint32]int32
	mmokit.ForEach2(gw.stage, func(_ mmokit.Entity, _ *gamecomp.LootCrate, crateInv *gamecomp.Inventory) {
		crateContents = make(map[uint32]int32, len(crateInv.Items))
		for id, q := range crateInv.Items {
			crateContents[id] = q
		}
	})
	if crateContents == nil {
		t.Fatal("no loot crate spawned; expected non-soulbound items to drop")
	}
	if _, found := crateContents[100]; found {
		t.Errorf("soulbound Pulse Laser (100) leaked into loot crate")
	}
	if _, found := crateContents[110]; found {
		t.Errorf("soulbound Shield Gen (110) leaked into loot crate")
	}
	if _, found := crateContents[107]; found {
		t.Errorf("soulbound Plasma Cannon (107) leaked into loot crate")
	}
	if crateContents[101] != 1 {
		t.Errorf("non-soulbound Railgun (101) expected qty=1, got %d", crateContents[101])
	}
	if crateContents[2] != 5 {
		t.Errorf("non-soulbound Ore (2) expected qty=5, got %d", crateContents[2])
	}

	// PlayerData should have HasSave=true with the soulbound items
	// restored — respawn will pick this up via the "has save" branch in
	// entity_ship and skip the fresh-starter seed.
	pdata := gw.PlayerDB.Get("bob")
	if pdata == nil {
		t.Fatal("PlayerDB.Get(bob) returned nil after death")
	}
	if !pdata.HasSave {
		t.Errorf("HasSave=false after death with kept items; respawn would mint a fresh starter kit")
	}
	if pdata.Equipment.Weapon1 != 100 {
		t.Errorf("Equipment.Weapon1 = %d, want 100 (soulbound preserved)", pdata.Equipment.Weapon1)
	}
	if pdata.Equipment.Weapon2 != 0 {
		t.Errorf("Equipment.Weapon2 = %d, want 0 (non-soulbound dropped)", pdata.Equipment.Weapon2)
	}
	if pdata.Equipment.Shield != 110 {
		t.Errorf("Equipment.Shield = %d, want 110 (soulbound preserved)", pdata.Equipment.Shield)
	}
	if pdata.Cargo[107] != 1 {
		t.Errorf("Cargo[107] (soulbound PlasmaCannon) = %d, want 1", pdata.Cargo[107])
	}
	if _, found := pdata.Cargo[2]; found {
		t.Errorf("Cargo[2] (non-soulbound Ore) leaked into save; expected dropped")
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
