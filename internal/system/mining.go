package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/logger"
)

// MiningSystem handles continuous mining beam extraction and jettison.
// Mining beams are activated/deactivated by the AbilitySystem; this system
// performs the per-tick resource extraction for active beams.
type MiningSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[component.PlayerInput, component.MiningLaser, component.Position, component.Inventory]
}

func NewMiningSystem(gw *game.GameWorld) *MiningSystem {
	return &MiningSystem{gw: gw}
}

type pendingJettison struct {
	x, y  float32
	items map[uint32]float32
}

func (s *MiningSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[component.PlayerInput, component.MiningLaser, component.Position, component.Inventory](gw.ECS)
	}

	var jettisons []pendingJettison

	query := s.filter.Query()
	for query.Next() {
		input, laser, pos, inv := query.Get()
		entity := query.Entity()

		// Handle jettison — drop items into a loot crate
		if input.JettisonItemID > 0 {
			itemID := input.JettisonItemID
			if inv.Items != nil && inv.Items[itemID] > 0 {
				playerNetID := gw.NetworkIDMap.Get(entity).ID
				qty := inv.Items[itemID]
				gw.Log.Log(logger.CatMining, "player=%d jettisoned %.1f of item %d",
					playerNetID, qty, itemID)
				inv.RemoveItem(itemID, qty)
				jettisons = append(jettisons, pendingJettison{
					x:     pos.X,
					y:     pos.Y,
					items: map[uint32]float32{itemID: qty},
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
			if !gw.ECS.Alive(laser.Target) || !gw.MinableMap.HasAll(laser.Target) {
				beam.Active = false
				continue
			}

			// Range check
			if !gw.PositionMap.HasAll(laser.Target) {
				beam.Active = false
				continue
			}
			targetPos := gw.PositionMap.Get(laser.Target)
			dx := targetPos.X - pos.X
			dy := targetPos.Y - pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if dist > beam.Range {
				beam.Active = false
				continue
			}

			minable := gw.MinableMap.Get(laser.Target)
			if minable.Remaining <= 0 {
				beam.Active = false
				continue
			}

			// Check cargo space
			if inv.RemainingMass() <= 0 {
				continue
			}

			// Extract resources
			amount := beam.Rate * dt
			if amount > minable.Remaining {
				amount = minable.Remaining
			}

			itemID := item.ResourceItemID(minable.ResourceType)
			added := inv.AddItem(itemID, amount)
			minable.Remaining -= added

			if added > 0 {
				playerNetID := gw.NetworkIDMap.Get(entity).ID
				gw.Log.Log(logger.CatMining, "player=%d mining beam=%d amount=%.2f remaining=%.2f",
					playerNetID, i, added, minable.Remaining)
			}

			// Mark depleted asteroid for removal
			if minable.Remaining <= 0 {
				gw.MarkForRemoval(laser.Target)
				gw.Log.Log(logger.CatMining, "asteroid depleted")
			}
		}
	}

	// Spawn loot crates for jettisoned cargo (after query iteration)
	for _, j := range jettisons {
		gw.SpawnLootCrate(j.x, j.y, j.items)
	}
}
