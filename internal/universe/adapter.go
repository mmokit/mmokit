package universe

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// gameWorldAdapter wraps *game.GameWorld to implement pkguniverse.GameWorld.
type gameWorldAdapter struct {
	gw            *game.GameWorld
	replicaNetIDs map[uint32]ecs.Entity
}

// newGameWorldAdapter creates a new adapter for the given game world.
func newGameWorldAdapter(gw *game.GameWorld) *gameWorldAdapter {
	return &gameWorldAdapter{
		gw:            gw,
		replicaNetIDs: make(map[uint32]ecs.Entity),
	}
}

// GW returns the underlying *game.GameWorld for direct access (e.g., console commands).
func (a *gameWorldAdapter) GW() *game.GameWorld {
	return a.gw
}

func (a *gameWorldAdapter) SerializeEntity(entity ecs.Entity) ([]byte, error) {
	payload := a.gw.SerializeEntity(entity)
	return game.MarshalTransferPayload(payload)
}

func (a *gameWorldAdapter) SpawnFromTransfer(data []byte) (netID uint32, connID uint32, err error) {
	payload, err := game.UnmarshalTransferPayload(data)
	if err != nil {
		a.gw.Log.Log(game.CatTransfer, "failed to unmarshal transfer: %v", err)
		return 0, 0, err
	}

	a.gw.Log.Log(game.CatTransfer, "inbox: received transfer netID=%d type=%d conn=%d username=%s",
		payload.NetworkID, payload.EntityType, payload.ConnID, payload.Username)

	entity := a.gw.SpawnFromTransfer(payload)
	if entity == (ecs.Entity{}) {
		return 0, 0, nil
	}

	return payload.NetworkID, payload.ConnID, nil
}

func (a *gameWorldAdapter) ScanBorderEntities(neighbors map[string]pkguniverse.NeighborInfo) map[string][][]byte {
	margin := a.gw.Config.AoIRadius
	size := coords.SectorSize

	// Build direction -> nodeID lookup from neighbor info
	dirToNeighbor := make(map[[2]int32]string, len(neighbors))
	for nID, info := range neighbors {
		dirToNeighbor[[2]int32{info.DX, info.DY}] = nID
	}

	result := make(map[string][][]byte)

	filter := ecs.NewFilter4[comp.Position, comp.NetworkID, comp.EntityKind, comp.Collider](a.gw.ECS).
		Without(ecs.C[comp.Ghost](), ecs.C[comp.Replica]())
	query := filter.Query()
	for query.Next() {
		pos, netID, kind, collider := query.Get()

		nearLeft := pos.X < margin
		nearRight := pos.X > (size - margin)
		nearBottom := pos.Y < margin
		nearTop := pos.Y > (size - margin)

		if !nearLeft && !nearRight && !nearBottom && !nearTop {
			continue
		}

		snap := game.ReplicaSnapshot{
			NetworkID:  netID.ID,
			EntityType: kind.Type,
			Position:   *pos,
			Sector:     comp.SectorCoord{SX: a.gw.Sector.SX, SY: a.gw.Sector.SY},
			Velocity:   comp.Velocity{},
			Rotation:   comp.Rotation{},
			Collider:   *collider,
		}

		entity := query.Entity()

		if a.gw.C.Velocity.HasAll(entity) {
			v := a.gw.C.Velocity.Get(entity)
			snap.Velocity = *v
		}
		if a.gw.C.Rotation.HasAll(entity) {
			r := a.gw.C.Rotation.Get(entity)
			snap.Rotation = *r
		}
		if a.gw.C.Health.HasAll(entity) {
			h := a.gw.C.Health.Get(entity)
			hCopy := *h
			snap.Health = &hCopy
		}
		if a.gw.C.Shield.HasAll(entity) {
			s := a.gw.C.Shield.Get(entity)
			sCopy := *s
			snap.Shield = &sCopy
		}
		if a.gw.C.Minable.HasAll(entity) {
			m := a.gw.C.Minable.Get(entity)
			mCopy := *m
			snap.Minable = &mCopy
		}

		snapBytes, err := game.MarshalReplicaSnapshot(&snap)
		if err != nil {
			continue
		}

		// Send to cardinal neighbors
		if nearLeft {
			if nID, ok := dirToNeighbor[[2]int32{-1, 0}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearRight {
			if nID, ok := dirToNeighbor[[2]int32{1, 0}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{0, -1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearTop {
			if nID, ok := dirToNeighbor[[2]int32{0, 1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}

		// Diagonal neighbors for corner entities
		if nearLeft && nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{-1, -1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearLeft && nearTop {
			if nID, ok := dirToNeighbor[[2]int32{-1, 1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearRight && nearBottom {
			if nID, ok := dirToNeighbor[[2]int32{1, -1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
		if nearRight && nearTop {
			if nID, ok := dirToNeighbor[[2]int32{1, 1}]; ok {
				result[nID] = append(result[nID], snapBytes)
			}
		}
	}

	total := 0
	for _, snaps := range result {
		total += len(snaps)
	}
	if total > 0 {
		a.gw.Log.Log(game.CatReplica, "sent %d replica snapshots to %d neighbors",
			total, len(result))
	}

	return result
}

func (a *gameWorldAdapter) ApplyReplicas(snapshots [][]byte, sourceNodeID string) {
	for _, snapBytes := range snapshots {
		snap, err := game.UnmarshalReplicaSnapshot(snapBytes)
		if err != nil {
			continue
		}

		// Translate coordinates to receiver's local space
		offsetX := float32(snap.Sector.SX-a.gw.Sector.SX) * coords.SectorSize
		offsetY := float32(snap.Sector.SY-a.gw.Sector.SY) * coords.SectorSize
		localX := snap.Position.X + offsetX
		localY := snap.Position.Y + offsetY

		if existing, ok := a.replicaNetIDs[snap.NetworkID]; ok && a.gw.ECS.Alive(existing) {
			// Update existing replica
			if a.gw.C.Position.HasAll(existing) {
				pos := a.gw.C.Position.Get(existing)
				pos.X = localX
				pos.Y = localY
			}
			if a.gw.C.Velocity.HasAll(existing) {
				vel := a.gw.C.Velocity.Get(existing)
				*vel = snap.Velocity
			}
			if a.gw.C.Rotation.HasAll(existing) {
				rot := a.gw.C.Rotation.Get(existing)
				*rot = snap.Rotation
			}
			if a.gw.C.Replica.HasAll(existing) {
				rep := a.gw.C.Replica.Get(existing)
				rep.TTL = 30
			}
			if snap.Health != nil && a.gw.C.Health.HasAll(existing) {
				h := a.gw.C.Health.Get(existing)
				*h = *snap.Health
			}
			if snap.Shield != nil && a.gw.C.Shield.HasAll(existing) {
				s := a.gw.C.Shield.Get(existing)
				*s = *snap.Shield
			}
		} else {
			// Create new replica entity
			entity := a.gw.C.ReplicaMapper.NewEntity(
				&comp.Position{X: localX, Y: localY},
				&snap.Velocity,
				&snap.Rotation,
				&snap.Collider,
				&comp.NetworkID{ID: snap.NetworkID},
				&comp.EntityKind{Type: snap.EntityType},
			)

			a.gw.C.SectorCoord.Add(entity, &comp.SectorCoord{SX: a.gw.Sector.SX, SY: a.gw.Sector.SY})

			a.gw.C.Replica.Add(entity, &comp.Replica{
				SourceNodeID: sourceNodeID,
				SourceNetID:  snap.NetworkID,
				TTL:          30,
			})

			if snap.Health != nil {
				a.gw.C.Health.Add(entity, snap.Health)
			}
			if snap.Shield != nil {
				a.gw.C.Shield.Add(entity, snap.Shield)
			}
			if snap.Minable != nil {
				a.gw.C.Minable.Add(entity, snap.Minable)
			}

			a.replicaNetIDs[snap.NetworkID] = entity
			a.gw.Log.Log(game.CatReplica, "created replica netID=%d type=%d from=%s at (%.0f,%.0f)",
				snap.NetworkID, snap.EntityType, sourceNodeID, localX, localY)
		}
	}
}

func (a *gameWorldAdapter) ExpireReplicas() {
	filter := ecs.NewFilter1[comp.Replica](a.gw.ECS)
	var expired []ecs.Entity

	query := filter.Query()
	for query.Next() {
		rep := query.Get()
		rep.TTL--
		if rep.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
	}

	for _, e := range expired {
		if a.gw.ECS.Alive(e) {
			netID := uint32(0)
			if a.gw.C.Replica.HasAll(e) {
				rep := a.gw.C.Replica.Get(e)
				netID = rep.SourceNetID
				delete(a.replicaNetIDs, netID)
			}
			a.gw.MarkForRemoval(e)
			a.gw.Log.Log(game.CatReplica, "replica expired: netID=%d (TTL reached 0)", netID)
		}
	}
}

func (a *gameWorldAdapter) RemoveReplicaByNetID(netID uint32) {
	if replicaEntity, ok := a.replicaNetIDs[netID]; ok {
		if a.gw.ECS.Alive(replicaEntity) {
			a.gw.ECS.RemoveEntity(replicaEntity)
			a.gw.Log.Log(game.CatTransfer, "removed pre-existing replica netID=%d before transfer spawn", netID)
		}
		delete(a.replicaNetIDs, netID)
	}
}

func (a *gameWorldAdapter) MarkForRemoval(entity ecs.Entity) {
	a.gw.MarkForRemoval(entity)
}

func (a *gameWorldAdapter) ECSWorld() *ecs.World {
	return a.gw.ECS
}

func (a *gameWorldAdapter) GetAoIRadius() float32 {
	return a.gw.Config.AoIRadius
}

func (a *gameWorldAdapter) TickGhosts() {
	filter := ecs.NewFilter1[comp.Ghost](a.gw.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		ghost := query.Get()
		ghost.TTL--
		if ghost.TTL <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	for _, e := range expired {
		if a.gw.ECS.Alive(e) {
			netID := uint32(0)
			if a.gw.C.NetworkID.HasAll(e) {
				netID = a.gw.C.NetworkID.Get(e).ID
			}
			a.gw.MarkForRemoval(e)
			a.gw.Log.Log(game.CatTransfer, "ghost expired: netID=%d (TTL reached 0)", netID)
		}
	}
}

func (a *gameWorldAdapter) TickTransferCooldowns() {
	filter := ecs.NewFilter1[comp.TransferCooldown](a.gw.ECS)
	var expired []ecs.Entity
	query := filter.Query()
	for query.Next() {
		tc := query.Get()
		tc.Remaining--
		if tc.Remaining <= 0 {
			expired = append(expired, query.Entity())
		}
	}
	for _, e := range expired {
		if a.gw.ECS.Alive(e) {
			a.gw.C.TransferCooldown.Remove(e)
		}
	}
}

func (a *gameWorldAdapter) RemoveGhostByNetID(netID uint32) {
	filter := ecs.NewFilter2[comp.NetworkID, comp.Ghost](a.gw.ECS)
	query := filter.Query()
	for query.Next() {
		nid, _ := query.Get()
		if nid.ID == netID {
			entity := query.Entity()
			query.Close()
			a.gw.MarkForRemoval(entity)
			a.gw.Log.Log(game.CatTransfer, "ghost removed: netID=%d (arrival confirmed)", netID)
			return
		}
	}
}

func (a *gameWorldAdapter) DispatchChat(username, text string) {
	a.gw.Log.Log(game.CatChat, "inbox: relayed chat <%s> %s", username, text)
	engine.Enqueue(a.gw.Queue, &enginepb.ChatMsg{
		Username: username,
		Text:     text,
	})
}

func (a *gameWorldAdapter) RegisterPendingLogin(connID uint32, username string) {
	a.gw.Log.Log(game.CatConnect, "inbox: respawn transfer conn=%d username=%s", connID, username)
	a.gw.Players.Usernames[connID] = username
	a.gw.Players.PendingLogins[connID] = username
}

func (a *gameWorldAdapter) SetBridge(bridge pkguniverse.NodeBridge) {
	a.gw.Bridge = bridge
}

func (a *gameWorldAdapter) Shutdown() {
	a.gw.Shutdown()
}

// Ensure gameWorldAdapter implements pkguniverse.GameWorld at compile time.
var _ pkguniverse.GameWorld = (*gameWorldAdapter)(nil)

// UnwrapGameWorld extracts the underlying *game.GameWorld from a pkguniverse.GameWorld.
// Panics if the interface is not backed by a gameWorldAdapter.
func UnwrapGameWorld(w pkguniverse.GameWorld) *game.GameWorld {
	return w.(*gameWorldAdapter).gw
}
