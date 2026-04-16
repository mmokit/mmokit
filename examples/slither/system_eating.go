package main

import (
	"math"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// EatingSystem lets snakes eat nearby food pellets within a forward arc.
// The eating range scales with the snake's size, and food must be within
// a ~120° cone in front of the head to be eaten.
type EatingSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		Pos   *mmokit.Position
		Rot   *mmokit.Rotation
		State *SnakeState
		NetID *mmokit.NetworkID
	}]
	buf []mmokit.SpatialEntry
}

func (s *EatingSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.entities.Init(s)
}

func (s *EatingSystem) Update(dt float32) {
	gw := s.gw

	replicaMap := ecs.NewMap1[mmokit.Replica](gw.ECSWorld())
	baseRadius := gw.Cfg.EatingRadius

	// Collect eat events to process after iteration
	type eatEvent struct {
		food    ecs.Entity
		value   float32
		replica bool
	}
	var eats []eatEvent

	bridge := gw.Bridge()

	// Cosine of half the eating cone angle (120° cone = 60° half-angle)
	const cosHalfCone = 0.5 // cos(60°)

	for _, b := range s.entities.All() {
		// Eating radius scales with sqrt of mass ratio — bigger snakes vacuum further
		massRatio := b.State.Mass / gw.Cfg.StartingMass
		eatingRadius := baseRadius * float32(math.Sqrt(float64(massRatio)))
		if eatingRadius < baseRadius {
			eatingRadius = baseRadius
		}

		fwdX := float32(math.Cos(float64(b.Rot.Angle)))
		fwdY := float32(math.Sin(float64(b.Rot.Angle)))

		s.buf = gw.Spatial.QueryRadius(b.Pos.X, b.Pos.Y, eatingRadius, s.buf[:0])
		for _, entry := range s.buf {
			if entry.Layer != LayerFood {
				continue
			}
			if !gw.ECSWorld().Alive(entry.Entity) {
				continue
			}

			// Check if food is within the forward arc
			dx := entry.X - b.Pos.X
			dy := entry.Y - b.Pos.Y
			dist := float32(math.Sqrt(float64(dx*dx + dy*dy)))
			if dist < 0.01 {
				// Directly on top — always eat
			} else {
				// Dot product with forward direction, normalized
				dot := (dx*fwdX + dy*fwdY) / dist
				if dot < cosHalfCone {
					continue // outside the forward cone
				}
			}

			if replicaMap.HasAll(entry.Entity) {
				replica := replicaMap.Get(entry.Entity)
				action := &mmokit.CrossCellAction{
					Type:         mmokit.ActionType(ActionEat),
					TargetNetID:  replica.SourceNetID,
					SourceNetID:  b.NetID.ID,
					SourceCellID: gw.CellID(),
				}
				bridge.SendAction(replica.SourceCellID, action)

				if !gw.FoodMap.HasAll(entry.Entity) {
					continue
				}
				food := gw.FoodMap.Get(entry.Entity)
				value := food.Value
				b.State.Mass += value
				eats = append(eats, eatEvent{food: entry.Entity, value: value, replica: true})

				gw.Engine().Log.Log(CatSnakeEat, "snake=%d ate replica food netID=%d value=%.1f mass=%.1f (cross-cell)", b.NetID.ID, replica.SourceNetID, value, b.State.Mass)
				continue
			}
			if !gw.FoodMap.HasAll(entry.Entity) {
				continue
			}
			food := gw.FoodMap.Get(entry.Entity)
			value := food.Value
			b.State.Mass += value
			eats = append(eats, eatEvent{food: entry.Entity, value: value})

			gw.Engine().Log.Log(CatSnakeEat, "snake=%d ate food value=%.1f mass=%.1f", b.NetID.ID, value, b.State.Mass)
		}
	}

	// Process removals after query iteration
	for _, eat := range eats {
		if gw.ECSWorld().Alive(eat.food) {
			gw.MarkForRemoval(eat.food)
			if !eat.replica {
				gw.FoodCount--
			}
		}
	}
}
