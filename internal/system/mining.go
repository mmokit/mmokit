package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MiningSystem handles continuous mining beam extraction and jettison.
// Mining beams are activated/deactivated by the AbilitySystem; this system
// performs the per-tick resource extraction for active beams.
type MiningSystem struct {
	mmokit.SystemBase
	gw     *game.GameWorld
	filter *ecs.Filter4[gamecomp.PlayerInput, gamecomp.MiningLaser, mmokit.Position, gamecomp.Inventory]
}

type pendingJettison struct {
	x, y  float32
	items map[uint32]int32
}

func (s *MiningSystem) Init() {
	s.gw = unwrapGW(s.GameWorld())
	s.filter = ecs.NewFilter4[gamecomp.PlayerInput, gamecomp.MiningLaser, mmokit.Position, gamecomp.Inventory](s.ECSWorld()).Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
}

func (s *MiningSystem) Update(dt float32) {
	gw := s.gw

	var jettisons []pendingJettison

	query := s.filter.Query()
	for query.Next() {
		input, laser, pos, inv := query.Get()
		entity := query.Entity()

		// Handle jettison — drop items into a loot crate
		if input.JettisonItemID > 0 {
			itemID := input.JettisonItemID
			if inv.Items != nil && inv.Items[itemID] > 0 {
				playerNetID := gw.C.NetworkID.Get(entity).ID
				qty := inv.Items[itemID]
				gw.Log.Log(game.CatEconomyMining, "player=%d jettisoned %d of item %d",
					playerNetID, qty, itemID)
				inv.RemoveItem(itemID, qty)
				jettisons = append(jettisons, pendingJettison{
					x:     pos.X,
					y:     pos.Y,
					items: map[uint32]int32{itemID: qty},
				})
			}
			input.JettisonItemID = 0
		}

		// Process each mining beam
		for i := range laser.Beams {
			beam := &laser.Beams[i]
			if !beam.Active || beam.Rate <= 0 {
				continue
			}

			// Validate target
			if !gw.ECS.Alive(laser.Target) || !gw.C.Minable.HasAll(laser.Target) {
				beam.Active = false
				continue
			}

			// Range check
			if !gw.C.Position.HasAll(laser.Target) {
				beam.Active = false
				continue
			}
			targetPos := gw.C.Position.Get(laser.Target)
			dx := targetPos.X - pos.X
			dy := targetPos.Y - pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if dist > beam.Range {
				beam.Active = false
				continue
			}

			minable := gw.C.Minable.Get(laser.Target)
			if minable.Remaining <= 0 {
				beam.Active = false
				continue
			}

			// Check cargo space
			if inv.RemainingMass() <= 0 {
				continue
			}

			// Extract resources (accumulator pattern: fractional extraction builds up to whole units)
			beam.Accumulator += beam.Rate * dt
			if beam.Accumulator < 1.0 {
				continue
			}
			// Cap accumulator by minable remaining
			if beam.Accumulator > minable.Remaining {
				beam.Accumulator = minable.Remaining
			}
			whole := int32(beam.Accumulator)
			// Extract the last fractional unit so asteroids don't get stuck near zero
			if whole == 0 && minable.Remaining > 0 && minable.Remaining < 1.0 {
				whole = 1
				beam.Accumulator = 0
			} else {
				beam.Accumulator -= float32(whole)
			}

			itemID := minable.ItemID
			added := inv.AddItem(itemID, whole)
			beam.Accumulator += float32(whole - added) // return unadded back to accumulator

			if added <= 0 {
				continue
			}

			playerNetID := gw.C.NetworkID.Get(entity).ID

			if gw.C.Replica.HasAll(laser.Target) {
				// Cross-node mining: send action to authoritative node
				s.sendCrossNodeMining(playerNetID, laser.Target, float32(added))
				// Update local replica for immediate visual feedback
				minable.Remaining -= float32(added)
				gw.Log.Log(game.CatEconomyMining, "player=%d cross-node mining beam=%d amount=%d remaining=%.2f",
					playerNetID, i, added, minable.Remaining)
			} else {
				minable.Remaining -= float32(added)
				gw.Log.Log(game.CatEconomyMining, "player=%d mining beam=%d amount=%d remaining=%.2f",
					playerNetID, i, added, minable.Remaining)

				// Mark depleted asteroid for removal
				if minable.Remaining <= 0 {
					gw.MarkForRemoval(laser.Target)
					gw.Log.Log(game.CatEconomyMining, "asteroid depleted")
				}
			}
		}
	}

	// Spawn loot crates for jettisoned cargo (after query iteration)
	for _, j := range jettisons {
		gw.SpawnLootCrate(j.x, j.y, j.items)
	}
}

func (s *MiningSystem) sendCrossNodeMining(casterNetID uint32, target ecs.Entity, amount float32) {
	gw := s.gw
	rep := gw.C.Replica.Get(target)
	gw.Bridge.SendAction(rep.SourceNodeID, &mmokit.CrossNodeAction{
		Type:         game.ActionMining,
		TargetNetID:  rep.SourceNetID,
		SourceNetID:  casterNetID,
		SourceNodeID: gw.NodeID,
		Payload:      game.MarshalMiningAction(&game.MiningAction{Amount: amount}),
	})
}
