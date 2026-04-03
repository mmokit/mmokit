package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
)

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-node transfers. WorldBase implements this automatically.
type BoundaryWorld interface {
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	Bridge() NodeBridge
	NodeID() string
	Cell() coords.CellCoord
	GhostMap() *ecs.Map1[component.Ghost]
	Engine() *engine.Engine
}

// edgeMargin is the minimum distance from the cell edge when clamping
// entities at world boundaries.
const edgeMargin float32 = 5.0

// transferHooker is optionally implemented by BoundaryWorld to adjust
// game-specific components during transfer serialization.
type transferHooker interface {
	PreSerialize(entity ecs.Entity, dx, dy float32)
	PostSerialize(entity ecs.Entity, dx, dy float32)
}

// BoundarySystem normalizes entity positions into [0, CellSize) and
// initiates cross-node transfers when entities cross cell boundaries.
type BoundarySystem struct {
	engine.SystemBase
	bw        BoundaryWorld
	filter    *ecs.Filter2[component.Position, component.CellCoord]
	playerMap *ecs.Map1[component.PlayerConn]
	velMap    *ecs.Map1[component.Velocity]
}

func (s *BoundarySystem) Init() {
	if s.bw == nil {
		if gw, ok := s.GameWorld().(BoundaryWorld); ok {
			s.bw = gw
		}
	}
	w := s.ECSWorld()
	s.filter = ecs.NewFilter2[component.Position, component.CellCoord](w).
		Without(ecs.C[component.Ghost](), ecs.C[component.Replica](), ecs.C[component.Proxy](), ecs.C[component.Dormant](), ecs.C[component.TransferCooldown]())
	s.playerMap = ecs.NewMap1[component.PlayerConn](w)
	s.velMap = ecs.NewMap1[component.Velocity](w)
}

func (s *BoundarySystem) Update(dt float32) {
	type pendingTransfer struct {
		entity     ecs.Entity
		destNodeID string
	}
	var transfers []pendingTransfer

	query := s.filter.Query()
	for query.Next() {
		pos, sec := query.Get()

		oldSX, oldSY := sec.CellX, sec.CellY
		newSX, newSY := sec.CellX, sec.CellY
		newX, newY := pos.X, pos.Y

		for newX >= coords.CellSize {
			newX -= coords.CellSize
			newSX++
		}
		for newX < 0 {
			newX += coords.CellSize
			newSX--
		}
		for newY >= coords.CellSize {
			newY -= coords.CellSize
			newSY++
		}
		for newY < 0 {
			newY += coords.CellSize
			newSY--
		}

		if newSX == oldSX && newSY == oldSY {
			continue
		}

		destCell := coords.CellCoord{CellX: newSX, CellY: newSY}
		destNodeID := s.bw.Bridge().NodeOwner(destCell)

		if destNodeID == "" {
			// World edge — clamp position back into current cell
			if pos.X < 0 {
				pos.X = edgeMargin
			} else if pos.X >= coords.CellSize {
				pos.X = coords.CellSize - edgeMargin
			}
			if pos.Y < 0 {
				pos.Y = edgeMargin
			} else if pos.Y >= coords.CellSize {
				pos.Y = coords.CellSize - edgeMargin
			}
			// Zero velocity in clamped directions
			if s.velMap.HasAll(query.Entity()) {
				vel := s.velMap.Get(query.Entity())
				if pos.X <= edgeMargin || pos.X >= coords.CellSize-edgeMargin {
					vel.X = 0
				}
				if pos.Y <= edgeMargin || pos.Y >= coords.CellSize-edgeMargin {
					vel.Y = 0
				}
			}
			continue
		}

		if destNodeID != s.bw.NodeID() {
			transfers = append(transfers, pendingTransfer{
				entity:     query.Entity(),
				destNodeID: destNodeID,
			})
			continue
		}

		// Same-node cell change: just normalize
		pos.X = newX
		pos.Y = newY
		sec.CellX = newSX
		sec.CellY = newSY
	}

	ghostMap := s.bw.GhostMap()
	posMap := ecs.NewMap1[component.Position](s.ECSWorld())
	netIDMap := ecs.NewMap1[component.NetworkID](s.ECSWorld())
	cellMap := ecs.NewMap1[component.CellCoord](s.ECSWorld())

	for _, t := range transfers {
		if !s.ECSWorld().Alive(t.entity) {
			continue
		}

		// Read position and cell (pointers valid until archetype change)
		pos := posMap.Get(t.entity)
		sec := cellMap.Get(t.entity)
		origX, origY := pos.X, pos.Y
		origSX, origSY := sec.CellX, sec.CellY

		// Compute normalized position for destination
		newX, newY := pos.X, pos.Y
		newSX, newSY := s.bw.Cell().CellX, s.bw.Cell().CellY
		for newX >= coords.CellSize {
			newX -= coords.CellSize
			newSX++
		}
		for newX < 0 {
			newX += coords.CellSize
			newSX--
		}
		for newY >= coords.CellSize {
			newY -= coords.CellSize
			newSY++
		}
		for newY < 0 {
			newY += coords.CellSize
			newSY--
		}

		// Temporarily set normalized position for serialization
		pos.X, pos.Y = newX, newY
		sec.CellX, sec.CellY = newSX, newSY

		dx, dy := newX-origX, newY-origY

		// Notify game to adjust components that store absolute positions
		if th, ok := s.bw.(transferHooker); ok {
			th.PreSerialize(t.entity, dx, dy)
		}

		data, err := s.bw.SerializeEntity(t.entity)

		// Restore original position for ghost visual continuity
		pos.X, pos.Y = origX, origY
		sec.CellX, sec.CellY = origSX, origSY

		// Restore game components
		if th, ok := s.bw.(transferHooker); ok {
			th.PostSerialize(t.entity, -dx, -dy)
		}

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

		s.bw.Engine().Log.Log(CatMeshTransfer, "[%s] transfer: netID=%d -> %s", s.bw.NodeID(), netID, t.destNodeID)
		s.bw.Bridge().SendTransfer(t.destNodeID, data, netID)

		// Player transfer: clean up session on source node and update coordinator routing.
		if s.playerMap.HasAll(t.entity) {
			playerConnID := s.playerMap.Get(t.entity).ConnID
			if playerConnID != 0 {
				if eng := s.bw.Engine(); eng != nil {
					if sess := eng.Players.ByConnID(playerConnID); sess != nil {
						eng.Players.Transition(sess, engine.StateTransferring)
						eng.Players.Remove(sess)
					}
				}
				s.bw.Bridge().OnPlayerTransfer(playerConnID, t.destNodeID)
			}
		}
	}
}
