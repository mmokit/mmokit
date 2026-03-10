package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
)

// MiningSystem handles mining laser activation and resource extraction.
type MiningSystem struct {
	gw     *game.GameWorld
	filter *ecs.Filter4[component.PlayerInput, component.MiningLaser, component.Position, component.Inventory]
}

func NewMiningSystem(gw *game.GameWorld) *MiningSystem {
	return &MiningSystem{gw: gw}
}

func (s *MiningSystem) Update(dt float32) {
	gw := s.gw
	if s.filter == nil {
		s.filter = ecs.NewFilter4[component.PlayerInput, component.MiningLaser, component.Position, component.Inventory](gw.ECS)
	}

	query := s.filter.Query()
	for query.Next() {
		input, laser, pos, inv := query.Get()
		entity := query.Entity()

		// Handle jettison
		if input.JettisonResource >= 1 && input.JettisonResource <= 4 {
			idx := input.JettisonResource - 1
			if inv.Resources[idx] > 0 {
				playerNetID := gw.NetworkIDMap.Get(entity).ID
				gw.Log.Log(logger.CatMining, "player=%d jettisoned %.1f of resource %d",
					playerNetID, inv.Resources[idx], idx)
				inv.Resources[idx] = 0
			}
			input.JettisonResource = 0
		}

		// Default: laser off
		laser.Active = false

		if !input.Mine || input.TargetNetID == 0 {
			continue
		}

		// Resolve target
		targetEntity, ok := gw.NetIDToEntity[input.TargetNetID]
		if !ok || !gw.ECS.Alive(targetEntity) {
			continue
		}

		// Target must be minable
		if !gw.MinableMap.HasAll(targetEntity) {
			continue
		}

		// Check range
		if !gw.PositionMap.HasAll(targetEntity) {
			continue
		}
		targetPos := gw.PositionMap.Get(targetEntity)
		dx := targetPos.X - pos.X
		dy := targetPos.Y - pos.Y
		dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
		if dist > laser.Range {
			continue
		}

		minable := gw.MinableMap.Get(targetEntity)
		if minable.Remaining <= 0 {
			continue
		}

		// Calculate cargo space
		var totalCargo float32
		for _, r := range inv.Resources {
			totalCargo += r
		}
		cargoSpace := gw.Config.MaxCargo - totalCargo
		if cargoSpace <= 0 {
			continue
		}

		// Extract resources
		amount := laser.Rate * dt
		if amount > minable.Remaining {
			amount = minable.Remaining
		}
		if amount > cargoSpace {
			amount = cargoSpace
		}

		inv.Resources[minable.ResourceType] += amount
		minable.Remaining -= amount

		laser.Active = true
		laser.Target = targetEntity

		playerNetID := gw.NetworkIDMap.Get(entity).ID
		gw.Log.Log(logger.CatMining, "player=%d mining target=%d amount=%.2f remaining=%.2f",
			playerNetID, input.TargetNetID, amount, minable.Remaining)

		// Mark depleted asteroid for removal
		if minable.Remaining <= 0 {
			gw.MarkForRemoval(targetEntity)
			gw.Log.Log(logger.CatMining, "asteroid %d depleted", input.TargetNetID)
		}
	}
}
