package system

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"google.golang.org/protobuf/proto"

	gamepb "github.com/zenion/mmoserver/gen/go"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/logger"
)

// EconomySystem handles selling cargo at stations and picking up loot crates.
type EconomySystem struct {
	gw            *game.GameWorld
	playerFilter  *ecs.Filter3[component.PlayerInput, component.Position, component.Inventory]
	crateFilter   *ecs.Filter3[component.LootCrate, component.Position, component.Inventory]
	stationFilter *ecs.Filter2[component.Station, component.Position]
}

func NewEconomySystem(gw *game.GameWorld) *EconomySystem {
	return &EconomySystem{gw: gw}
}

type crateInfo struct {
	entity       ecs.Entity
	x, y         float32
	inv          *component.Inventory
	dropperNetID uint32
	immune       bool
}

func (s *EconomySystem) Update(dt float32) {
	gw := s.gw
	if s.playerFilter == nil {
		s.playerFilter = ecs.NewFilter3[component.PlayerInput, component.Position, component.Inventory](gw.ECS)
		s.crateFilter = ecs.NewFilter3[component.LootCrate, component.Position, component.Inventory](gw.ECS)
		s.stationFilter = ecs.NewFilter2[component.Station, component.Position](gw.ECS)
	}

	// Collect station positions
	var stationPositions []component.Position
	stationQuery := s.stationFilter.Query()
	for stationQuery.Next() {
		_, pos := stationQuery.Get()
		stationPositions = append(stationPositions, *pos)
	}

	// Collect crate info and tick down pickup immunity
	var crates []crateInfo
	crateQuery := s.crateFilter.Query()
	for crateQuery.Next() {
		lc, pos, inv := crateQuery.Get()
		if lc.PickupImmunity > 0 {
			lc.PickupImmunity -= dt
		}
		crates = append(crates, crateInfo{
			entity:       crateQuery.Entity(),
			x:            pos.X,
			y:            pos.Y,
			inv:          inv,
			dropperNetID: lc.DropperNetID,
			immune:       lc.PickupImmunity > 0,
		})
	}

	sellRange2 := float64(gw.Config.SellRange) * float64(gw.Config.SellRange)
	pickupRange2 := float64(gw.Config.LootPickupRange) * float64(gw.Config.LootPickupRange)

	playerQuery := s.playerFilter.Query()
	for playerQuery.Next() {
		input, pos, inv := playerQuery.Get()
		entity := playerQuery.Entity()

		// Sell at station
		if input.Sell {
			input.Sell = false

			var totalCargo float32
			for _, r := range inv.Resources {
				totalCargo += r
			}
			if totalCargo > 0 {
				for _, sp := range stationPositions {
					dx := float64(pos.X - sp.X)
					dy := float64(pos.Y - sp.Y)
					dist2 := dx*dx + dy*dy
					if dist2 > sellRange2 {
						continue
					}

					// Calculate flux earned
					var fluxEarned float64
					for i, amount := range inv.Resources {
						fluxEarned += float64(amount) * gw.Config.SellPrices[i]
					}

					inv.Resources = [4]float32{}

					// Update FLUX balance and send result to client
					if gw.PlayerConnMap.HasAll(entity) {
						connID := gw.PlayerConnMap.Get(entity).ConnID
						username := gw.ConnToUsername[connID]
						pdata := gw.PlayerDB.GetOrCreate(username)
						pdata.Flux += fluxEarned
						gw.PlayerDB.MarkDirty(username)
						totalFlux := pdata.Flux
						msg := &gamepb.ServerMessage{
							Msg: &gamepb.ServerMessage_SellResult{
								SellResult: &gamepb.SellResultMsg{
									FluxEarned: float32(fluxEarned),
									TotalFlux:  float32(totalFlux),
								},
							},
						}
						if data, err := proto.Marshal(msg); err == nil {
							gw.ConnMgr.SendReliable(connID, data)
						}
						gw.Log.Log(logger.CatEconomy, "sell: flux_earned=%.1f total_flux=%.1f", fluxEarned, totalFlux)
					}
					break
				}
			}
		}

		// Loot crate pickup
		playerNetID := gw.NetworkIDMap.Get(entity).ID
		for i := range crates {
			c := &crates[i]
			if !gw.ECS.Alive(c.entity) {
				continue
			}

			// Skip if this player dropped the crate and immunity is still active
			if c.immune && c.dropperNetID == playerNetID {
				continue
			}

			dx := float64(pos.X - c.x)
			dy := float64(pos.Y - c.y)
			dist2 := dx*dx + dy*dy
			if dist2 > pickupRange2 {
				continue
			}

			// Calculate current cargo total
			var currentCargo float32
			for _, r := range inv.Resources {
				currentCargo += r
			}

			// Transfer cargo respecting MaxCargo
			var transferred bool
			for j := range c.inv.Resources {
				if c.inv.Resources[j] <= 0 {
					continue
				}
				room := gw.Config.MaxCargo - currentCargo
				if room <= 0 {
					break
				}
				take := float32(math.Min(float64(c.inv.Resources[j]), float64(room)))
				inv.Resources[j] += take
				c.inv.Resources[j] -= take
				currentCargo += take
				transferred = true
			}

			if transferred {
				// Check if crate is empty
				var remaining float32
				for _, r := range c.inv.Resources {
					remaining += r
				}
				if remaining <= 0 {
					gw.MarkForRemoval(c.entity)
				}
				gw.Log.Log(logger.CatEconomy, "loot pickup")
			}
		}
	}
}
