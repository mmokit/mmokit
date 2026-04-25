package universe

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	"github.com/zenion/mmoserver/pkg/query"
)

// BoundaryWorld is the interface needed by BoundarySystem to detect entities
// crossing cell boundaries and queue CrossingEvents for the HandoffDriver.
// WorldBase implements this automatically.
type BoundaryWorld interface {
	Bridge() Bridge
	CellID() string
	Cell() CellID
	CellSize() float32
	Engine() *engine.Engine
	QueueCrossing(evt CrossingEvent)
}

// edgeMargin is the minimum distance from the cell edge when clamping
// entities at world boundaries.
const edgeMargin float32 = 5.0

// BoundarySystem normalizes entity positions into [0, CellSize) and
// initiates cross-cell transfers when entities cross cell boundaries.
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
		destCellID string
	}
	var transfers []pendingTransfer

	for e, b := range s.entities {
		pos := b.Pos

		// Check if entity is within this node's subcell bounds.
		if pos.X >= bMinX && pos.X < bMaxX && pos.Y >= bMinY && pos.Y < bMaxY {
			continue
		}

		// Compute world-space position for the ownership lookup.
		worldX := float32(rootCell.X)*cellSize + pos.X
		worldY := float32(rootCell.Y)*cellSize + pos.Y
		destCellID := s.bw.Bridge().CellOwnerAtPos(worldX, worldY)

		if destCellID == "" {
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

		if destCellID == s.bw.CellID() {
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
			destCellID: destCellID,
		})
	}

	netIDMap := ecs.NewMap1[component.NetworkID](s.ECSWorld())
	playerMap := ecs.NewMap1[component.PlayerConn](s.ECSWorld())

	for _, t := range transfers {
		if !s.ECSWorld().Alive(t.entity) {
			continue
		}

		var netID uint32
		if netIDMap.HasAll(t.entity) {
			netID = netIDMap.Get(t.entity).ID
		}

		var connID uint32
		var username string
		if playerMap.HasAll(t.entity) {
			connID = playerMap.Get(t.entity).ConnID
			if eng := s.bw.Engine(); eng != nil {
				if sess := eng.Players.ByConnID(connID); sess != nil {
					username = sess.Username
				}
			}
		}

		s.bw.QueueCrossing(CrossingEvent{
			Entity:     t.entity,
			NetID:      netID,
			ConnID:     connID,
			Username:   username,
			DestCellID: t.destCellID,
		})
	}
}
