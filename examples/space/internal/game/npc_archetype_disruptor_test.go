package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/examples/space/internal/item"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestDisruptor_DebuffAppliesSlowAndSilence — a Disruptor NPC in Engage,
// with a player target inside DisruptorAttackRange and the StatusEffects
// component attached, must apply BOTH Slow and Silence to the player
// once SpecialCooldown elapses.
//
// The cast is instant-apply at cooldown (v1 simplification — the spec
// envisions a telegraphed skillshot projectile, deferred to a follow-up).
func TestDisruptor_DebuffAppliesSlowAndSilence(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()
	// newTestGameWorld bypasses GameSetup so the status verb isn't wired —
	// register it directly on the test stage (mirrors the healHandler
	// pattern in npc_archetype_support_test.go).
	mmokit.Handle(gw.stage, statusHandler)

	// Player must be inside DisruptorAttackRange — half-range keeps us
	// well inside both LockRange and the cast-range gate.
	playerX := gw.Config.DisruptorAttackRange / 2
	player := spawnAbilityTestPlayer(t, gw, 9001, 1, playerX, 0,
		item.PlasmaCannon, 0)
	// spawnAbilityTestPlayer doesn't attach StatusEffects — required so
	// statusHandler has somewhere to write.
	mmokit.Set(player, gamecomp.StatusEffects{})

	disruptor := gw.SpawnNPC(0, 0, ArchetypeDisruptor, 0, NPCSpawnModifiers{})

	// Pre-aim the NPC at the player so we don't have to spin the spatial
	// grid and acquire→approach state machine for the test.
	ai := mmokit.Get[gamecomp.NPCAI](disruptor)
	if ai == nil {
		t.Fatal("disruptor missing NPCAI")
	}
	ai.TargetNetID = player.NetID()
	ai.State = AIStateEngage
	ai.SpecialCooldown = 0 // fire on next tick

	sys, _ := newWiredNPCAISystem(t, gw)

	// Tick enough to clear cooldown and apply the debuff. 200 ticks at
	// 5% of cooldown each ≈ 10× cooldown — plenty of headroom.
	dt := gw.Config.DisruptorDebuffCooldown / 20
	for i := 0; i < 200; i++ {
		gw.stage.TickOne(sys, dt)
		se := mmokit.Get[gamecomp.StatusEffects](player)
		if se != nil && se.Has(gamecomp.StatusSlow) && se.Has(gamecomp.StatusSilence) {
			return // success — both applied
		}
	}

	se := mmokit.Get[gamecomp.StatusEffects](player)
	if se == nil {
		t.Fatal("no StatusEffects on player")
	}
	if !se.Has(gamecomp.StatusSlow) {
		t.Fatal("Slow not applied")
	}
	if !se.Has(gamecomp.StatusSilence) {
		t.Fatal("Silence not applied")
	}
}
