package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// BoundaryWorld is the interface needed by BoundarySystem to serialize entities
// and initiate cross-node transfers. WorldBase implements this automatically.
type BoundaryWorld interface {
	SerializeEntity(entity ecs.Entity) ([]byte, error)
	Bridge() Bridge
	NodeID() string
	Cell() CellID
	CellSize() float32
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
	bw       BoundaryWorld
	entities query.Query[struct {
		Pos    *component.Position
		CC     *component.CellCoord
		Player *component.PlayerConn `ecs:"optional"`
		Vel    *component.Velocity   `ecs:"optional"`
	}]
}

func (s *BoundarySystem) Init() {
	if s.bw == nil {
		if gw, ok := s.GameWorld().(BoundaryWorld); ok {
			s.bw = gw
		}
	}
	s.entities.Init(s,
		query.IncludeAll(),
		query.Without[component.Ghost](),
		query.Without[component.Replica](),
		query.Without[component.Dormant](),
		query.Without[component.TransferCooldown](),
	)
}

func (s *BoundarySystem) Update(dt float32) {
	cellSize := coords.CellSize
	cell := s.bw.Cell()

	// Compute this node's bounds in base-cell-local coordinates.
	// For depth-0 cells this is [0, baseCellSize). For sub-cells after a
	// split the range is narrower (e.g. [4096, 8192) for the right half).
	bMinX, bMinY, bMaxX, bMaxY := cell.LocalBounds(cellSize)

	rootCell := cell
	for rootCell.Depth > 0 {
		rootCell = rootCell.Parent()
	}

	type pendingTransfer struct {
		entity     ecs.Entity
		destNodeID string
	}
	var transfers []pendingTransfer

	for e, b := range s.entities.All() {
		pos := b.Pos

		// Check if entity is within this node's subcell bounds.
		if pos.X >= bMinX && pos.X < bMaxX && pos.Y >= bMinY && pos.Y < bMaxY {
			continue
		}

		// Compute world-space position for the ownership lookup.
		worldX := float32(rootCell.X)*cellSize + pos.X
		worldY := float32(rootCell.Y)*cellSize + pos.Y
		destNodeID := s.bw.Bridge().NodeOwnerAtPos(worldX, worldY)

		if destNodeID == "" {
			// World edge — clamp position back into this node's bounds
			if pos.X < bMinX {
				pos.X = bMinX + edgeMargin
			} else if pos.X >= bMaxX {
				pos.X = bMaxX - edgeMargin
			}
			if pos.Y < bMinY {
				pos.Y = bMinY + edgeMargin
			} else if pos.Y >= bMaxY {
				pos.Y = bMaxY - edgeMargin
			}
			if b.Vel != nil {
				vel := b.Vel
				if pos.X <= bMinX+edgeMargin || pos.X >= bMaxX-edgeMargin {
					vel.X = 0
				}
				if pos.Y <= bMinY+edgeMargin || pos.Y >= bMaxY-edgeMargin {
					vel.Y = 0
				}
			}
			continue
		}

		if destNodeID == s.bw.NodeID() {
			// Same node — clamp into bounds (shouldn't happen with 1:1 cell mapping)
			if pos.X >= bMaxX {
				pos.X = bMaxX - edgeMargin
			} else if pos.X < bMinX {
				pos.X = bMinX + edgeMargin
			}
			if pos.Y >= bMaxY {
				pos.Y = bMaxY - edgeMargin
			} else if pos.Y < bMinY {
				pos.Y = bMinY + edgeMargin
			}
			continue
		}

		transfers = append(transfers, pendingTransfer{
			entity:     e,
			destNodeID: destNodeID,
		})
	}

	ghostMap := s.bw.GhostMap()
	posMap := ecs.NewMap1[component.Position](s.ECSWorld())
	netIDMap := ecs.NewMap1[component.NetworkID](s.ECSWorld())
	cellMap := ecs.NewMap1[component.CellCoord](s.ECSWorld())
	playerMap := ecs.NewMap1[component.PlayerConn](s.ECSWorld())

	for _, t := range transfers {
		if !s.ECSWorld().Alive(t.entity) {
			continue
		}

		pos := posMap.Get(t.entity)
		sec := cellMap.Get(t.entity)
		origX, origY := pos.X, pos.Y
		origSX, origSY := sec.CellX, sec.CellY

		// Compute normalized position for serialization: wrap into [0, cellSize)
		// Use root cell coords since entities are in base-cell coordinate space.
		rootCell := cell
		for rootCell.Depth > 0 {
			rootCell = rootCell.Parent()
		}
		newX, newY := pos.X, pos.Y
		newSX, newSY := rootCell.X, rootCell.Y
		for newX >= cellSize {
			newX -= cellSize
			newSX++
		}
		for newX < 0 {
			newX += cellSize
			newSX--
		}
		for newY >= cellSize {
			newY -= cellSize
			newSY++
		}
		for newY < 0 {
			newY += cellSize
			newSY--
		}

		// Temporarily set normalized position for serialization
		pos.X, pos.Y = newX, newY
		sec.CellX, sec.CellY = newSX, newSY

		dx, dy := newX-origX, newY-origY

		if th, ok := s.bw.(transferHooker); ok {
			th.PreSerialize(t.entity, dx, dy)
		}

		data, err := s.bw.SerializeEntity(t.entity)

		// Restore original position for ghost visual continuity
		pos.X, pos.Y = origX, origY
		sec.CellX, sec.CellY = origSX, origSY

		if th, ok := s.bw.(transferHooker); ok {
			th.PostSerialize(t.entity, -dx, -dy)
		}

		if err != nil {
			continue
		}

		var netID uint32
		if netIDMap.HasAll(t.entity) {
			netID = netIDMap.Get(t.entity).ID
		}

		ghostMap.Add(t.entity, &component.Ghost{TTL: 10, DestNodeID: t.destNodeID})

		s.bw.Engine().Log.Log(CatMeshTransfer, "[%s] transfer: netID=%d -> %s", s.bw.NodeID(), netID, t.destNodeID)
		s.bw.Bridge().SendTransfer(t.destNodeID, data, netID)

		if playerMap.HasAll(t.entity) {
			playerConnID := playerMap.Get(t.entity).ConnID
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
