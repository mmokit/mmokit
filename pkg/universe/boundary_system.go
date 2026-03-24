package universe

import (
	"log"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-node transfers. WorldBase implements this automatically.
type BoundaryWorld interface {
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	Bridge() NodeBridge
	NodeID() string
	Sector() coords.SectorCoord
	GhostMap() *ecs.Map1[component.Ghost]
}

// BoundarySystem normalizes entity positions into [0, SectorSize) and
// initiates cross-node transfers when entities cross sector boundaries.
type BoundarySystem struct {
	world  *ecs.World
	bw     BoundaryWorld
	filter *ecs.Filter2[component.Position, component.SectorCoord]
}

// NewBoundarySystem creates a boundary system that detects sector crossings.
func NewBoundarySystem(world *ecs.World, bw BoundaryWorld) *BoundarySystem {
	return &BoundarySystem{world: world, bw: bw}
}

func (s *BoundarySystem) Name() string { return "SectorBoundary" }

func (s *BoundarySystem) Update(dt float32) {
	if s.filter == nil {
		s.filter = ecs.NewFilter2[component.Position, component.SectorCoord](s.world).
			Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.TransferCooldown]())
	}

	type pendingTransfer struct {
		entity     ecs.Entity
		destNodeID string
	}
	var transfers []pendingTransfer

	query := s.filter.Query()
	for query.Next() {
		pos, sec := query.Get()

		oldSX, oldSY := sec.SX, sec.SY
		newSX, newSY := sec.SX, sec.SY
		newX, newY := pos.X, pos.Y

		for newX >= coords.SectorSize {
			newX -= coords.SectorSize
			newSX++
		}
		for newX < 0 {
			newX += coords.SectorSize
			newSX--
		}
		for newY >= coords.SectorSize {
			newY -= coords.SectorSize
			newSY++
		}
		for newY < 0 {
			newY += coords.SectorSize
			newSY--
		}

		if newSX == oldSX && newSY == oldSY {
			continue
		}

		destSector := coords.SectorCoord{SX: newSX, SY: newSY}
		destNodeID := s.bw.Bridge().SectorOwner(destSector)
		if destNodeID != "" && destNodeID != s.bw.NodeID() {
			transfers = append(transfers, pendingTransfer{
				entity:     query.Entity(),
				destNodeID: destNodeID,
			})
			continue
		}

		// Same-node sector change: just normalize
		pos.X = newX
		pos.Y = newY
		sec.SX = newSX
		sec.SY = newSY
	}

	ghostMap := s.bw.GhostMap()
	posMap := ecs.NewMap1[component.Position](s.world)
	netIDMap := ecs.NewMap1[component.NetworkID](s.world)
	sectorMap := ecs.NewMap1[component.SectorCoord](s.world)

	for _, t := range transfers {
		if !s.world.Alive(t.entity) {
			continue
		}

		// Read position and sector (pointers valid until archetype change)
		pos := posMap.Get(t.entity)
		sec := sectorMap.Get(t.entity)
		origX, origY := pos.X, pos.Y
		origSX, origSY := sec.SX, sec.SY

		// Compute normalized position for destination
		newX, newY := pos.X, pos.Y
		newSX, newSY := s.bw.Sector().SX, s.bw.Sector().SY
		for newX >= coords.SectorSize {
			newX -= coords.SectorSize
			newSX++
		}
		for newX < 0 {
			newX += coords.SectorSize
			newSX--
		}
		for newY >= coords.SectorSize {
			newY -= coords.SectorSize
			newSY++
		}
		for newY < 0 {
			newY += coords.SectorSize
			newSY--
		}

		// Temporarily set normalized position for serialization
		pos.X, pos.Y = newX, newY
		sec.SX, sec.SY = newSX, newSY

		data, err := s.bw.SerializeEntity(t.entity)

		// Restore original position for ghost visual continuity
		pos.X, pos.Y = origX, origY
		sec.SX, sec.SY = origSX, origSY

		if err != nil {
			continue
		}

		// Read netID before archetype change
		var netID uint32
		if netIDMap.HasAll(t.entity) {
			netID = netIDMap.Get(t.entity).ID
		}

		// Add Ghost (invalidates component pointers — must be last read)
		ghostMap.Add(t.entity, &component.Ghost{TTL: 10, DestNodeID: t.destNodeID})

		log.Printf("[%s] transfer: netID=%d -> %s", s.bw.NodeID(), netID, t.destNodeID)
		s.bw.Bridge().SendTransfer(t.destNodeID, data, netID)
	}
}
