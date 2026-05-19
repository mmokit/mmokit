package game

import (
	"testing"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

func TestSpawnNPC_StatMultipliersScaleHP(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseHP := gw.Config.BrawlerHP

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{
		HPMul: 2.5,
	})
	h := mmokit.Get[gamecomp.Health](npc)
	if h == nil {
		t.Fatal("no Health on spawned NPC")
	}
	if absf(h.Max-baseHP*2.5) > 0.1 {
		t.Fatalf("Health.Max = %v, want %v", h.Max, baseHP*2.5)
	}
}

func TestSpawnNPC_StatMultipliersZero_DefaultsToOne(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseHP := gw.Config.BrawlerHP

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](npc)
	if absf(h.Max-baseHP) > 0.1 {
		t.Fatalf("zero-modifier Health.Max = %v, want %v", h.Max, baseHP)
	}
}

func TestSpawnNPC_StatMultipliersStackWithElite(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseHP := gw.Config.BrawlerHP
	eliteMul := gw.Config.BossSoloHPMultiplier
	tierMul := float32(2.0)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{
		Elite: true,
		HPMul: tierMul,
	})
	h := mmokit.Get[gamecomp.Health](npc)
	expected := baseHP * eliteMul * tierMul
	if absf(h.Max-expected) > 0.1 {
		t.Fatalf("elite+tier HP = %v, want %v", h.Max, expected)
	}
}

func TestSpawnNPC_ShieldMultiplier(t *testing.T) {
	gw, _ := newTestGameWorld()
	baseShield := gw.Config.BrawlerShield

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{
		ShieldMul: 3.0,
	})
	s := mmokit.Get[gamecomp.Shield](npc)
	if s == nil {
		t.Fatal("no Shield on spawned NPC")
	}
	if absf(s.Max-baseShield*3.0) > 0.1 {
		t.Fatalf("Shield.Max = %v, want %v", s.Max, baseShield*3.0)
	}
}
