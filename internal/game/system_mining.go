package game

import (
	"math"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
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
	s.gw = mmokit.State[GameWorld](s.Stage())
}

func (s *MiningSystem) Update(dt float32) {
	gw := s.gw

	var jettisons []pendingJettison

	for e, b := range s.entities.Iter {
		input, laser, pos, inv := b.Input, b.Laser, b.Pos, b.Inv

		entity := mmokit.EntityFromECS(gw.stage, e)

		// Handle jettison — drop items into a loot crate
		if input.JettisonItemID > 0 {
			itemID := input.JettisonItemID
			if inv.Items != nil && inv.Items[itemID] > 0 {
				playerNetID := entity.NetID()
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
			targetE := mmokit.EntityFromECS(gw.stage, laser.Target)
			minable := mmokit.Get[gamecomp.Minable](targetE)
			if !targetE.Alive() || minable == nil {
				beam.Active = false
				continue
			}

			// Range check
			targetPos := mmokit.Get[mmokit.Position](targetE)
			if targetPos == nil {
				beam.Active = false
				continue
			}
			dx := targetPos.X - pos.X
			dy := targetPos.Y - pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if dist > beam.Range {
				beam.Active = false
				continue
			}

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

			playerNetID := entity.NetID()

			// Resolve target as an mmokit.Entity. When local, target.Send
			// dispatches synchronously and the handler decrements
			// Minable.Remaining + marks for removal. When replica, Send
			// routes the action to the authoritative cell; we still
			// decrement the local replica copy for visual feedback.
			asteroid := mmokit.EntityByNetID(gw.stage, targetE.NetID())

			if !asteroid.Local() {
				minable.Remaining -= float32(added)
			}

			gw.MineExtract(entity, asteroid, uint8(i), float32(added))

			gw.eng.Log.Log(CatEconomyMining, "player=%d mining beam=%d amount=%d remaining=%.2f",
				playerNetID, i, added, minable.Remaining)
		}

		// Sync replicated active-mining state after beam updates.
		gw.syncActiveMining(entity, laser)
	}

	// Spawn loot crates for jettisoned cargo (after query iteration)
	for _, j := range jettisons {
		gw.SpawnLootCrate(j.x, j.y, j.items)
	}
}
