package game

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// MiningSystem handles continuous mining beam extraction and jettison.
// Mining beams are activated/deactivated by the AbilitySystem; this system
// performs the per-tick resource extraction for active beams.
type MiningSystem struct {
	mmokit.SystemBase
	gw       *GameWorld
	entities mmokit.Query[struct {
		Input  *gamecomp.PlayerInput
		Laser  *gamecomp.MiningLaser
		Pos    *mmokit.Position
		Inv    *gamecomp.Inventory
		Active *gamecomp.ActiveMining
	}]
}

type pendingJettison struct {
	x, y  float32
	items map[uint32]int32
}

func (s *MiningSystem) Init() {
	s.gw = gwFromSystem(s.SystemBase)
	s.entities.Init(s)
}

func (s *MiningSystem) Update(dt float32) {
	gw := s.gw

	var jettisons []pendingJettison

	for e, b := range s.entities {
		input, laser, pos, inv := b.Input, b.Laser, b.Pos, b.Inv

		// Handle jettison — drop items into a loot crate
		if input.JettisonItemID > 0 {
			itemID := input.JettisonItemID
			if inv.Items != nil && inv.Items[itemID] > 0 {
				playerNetID := gw.C.NetworkID.Get(e).ID
				qty := inv.Items[itemID]
				gw.eng.Log.Log(CatEconomyMining, "player=%d jettisoned %d of item %d",
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
			if !gw.eng.ECS.Alive(laser.Target) || !gw.C.Minable.HasAll(laser.Target) {
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

			playerNetID := gw.C.NetworkID.Get(e).ID

			if gw.C.Replica.HasAll(laser.Target) {
				// Cross-cell mining: send action to authoritative node
				s.sendCrossNodeMining(playerNetID, laser.Target, float32(added))
				// Update local replica for immediate visual feedback
				minable.Remaining -= float32(added)
				gw.eng.Log.Log(CatEconomyMining, "player=%d cross-cell mining beam=%d amount=%d remaining=%.2f",
					playerNetID, i, added, minable.Remaining)
			} else {
				minable.Remaining -= float32(added)
				gw.eng.Log.Log(CatEconomyMining, "player=%d mining beam=%d amount=%d remaining=%.2f",
					playerNetID, i, added, minable.Remaining)

				// Mark depleted asteroid for removal
				if minable.Remaining <= 0 {
					gw.MarkForRemoval(laser.Target)
					gw.eng.Log.Log(CatEconomyMining, "asteroid depleted")
				}
			}
		}

		// Sync replicated active-mining state after beam updates.
		gw.syncActiveMining(e, laser)
	}

	// Spawn loot crates for jettisoned cargo (after query iteration)
	for _, j := range jettisons {
		gw.SpawnLootCrate(j.x, j.y, j.items)
	}
}

func (s *MiningSystem) sendCrossNodeMining(casterNetID uint32, target ecs.Entity, amount float32) {
	gw := s.gw
	rep := gw.C.Replica.Get(target)
	gw.Bridge().SendAction(rep.SourceCellID, &mmokit.CrossCellAction{
		Type:         ActionMining,
		TargetNetID:  rep.SourceNetID,
		SourceNetID:  casterNetID,
		SourceCellID: gw.CellID(),
		Payload:      MarshalMiningAction(&MiningAction{Amount: amount}),
	})
}
