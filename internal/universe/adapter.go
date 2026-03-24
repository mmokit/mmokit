package universe

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	comp "github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// gameWorldAdapter wraps *game.GameWorld to implement pkguniverse.GameWorld.
type gameWorldAdapter struct {
	gw                 *game.GameWorld
	replicaNetIDs      map[uint32]ecs.Entity
	replRegistry       *pkguniverse.ReplicationRegistry
	sideEffectRegistry *pkguniverse.SideEffectRegistry
}

// newGameWorldAdapter creates a new adapter for the given game world.
func newGameWorldAdapter(gw *game.GameWorld, replRegistry *pkguniverse.ReplicationRegistry, seRegistry *pkguniverse.SideEffectRegistry) *gameWorldAdapter {
	return &gameWorldAdapter{
		gw:                 gw,
		replicaNetIDs:      make(map[uint32]ecs.Entity),
		replRegistry:       replRegistry,
		sideEffectRegistry: seRegistry,
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
	result := pkguniverse.ScanBorderWithRegistry(
		a.gw.ECS,
		a.replRegistry,
		coords.SectorCoord{SX: a.gw.Sector.SX, SY: a.gw.Sector.SY},
		coords.SectorSize,
		a.gw.Config.AoIRadius,
		neighbors,
	)

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
	pkguniverse.ApplyReplicasWithRegistry(
		snapshots,
		sourceNodeID,
		coords.SectorCoord{SX: a.gw.Sector.SX, SY: a.gw.Sector.SY},
		coords.SectorSize,
		a.replRegistry,
		a,
	)
}

// --- ReplicaApplyContext implementation ---

func (a *gameWorldAdapter) FindReplica(netID uint32) (ecs.Entity, bool) {
	if existing, ok := a.replicaNetIDs[netID]; ok && a.gw.ECS.Alive(existing) {
		return existing, true
	}
	return ecs.Entity{}, false
}

func (a *gameWorldAdapter) CreateReplica(frame *pkguniverse.ReplicaFrame, localX, localY float32, sourceNodeID string) ecs.Entity {
	// Decode collider from frame (component ID 0)
	collider := comp.Collider{}
	for _, cs := range frame.Components {
		if cs.ID == 0 {
			collider = pkguniverse.UnmarshalCollider(cs.Data)
			break
		}
	}

	entity := a.gw.C.ReplicaMapper.NewEntity(
		&comp.Position{X: localX, Y: localY},
		&comp.Velocity{},
		&comp.Rotation{},
		&collider,
		&comp.NetworkID{ID: frame.NetworkID},
		&comp.EntityKind{Type: frame.EntityType},
	)

	a.gw.C.SectorCoord.Add(entity, &comp.SectorCoord{SX: a.gw.Sector.SX, SY: a.gw.Sector.SY})
	a.gw.C.Replica.Add(entity, &comp.Replica{
		SourceNodeID: sourceNodeID,
		SourceNetID:  frame.NetworkID,
		TTL:          30,
	})

	a.replicaNetIDs[frame.NetworkID] = entity
	a.gw.Log.Log(game.CatReplica, "created replica netID=%d type=%d from=%s at (%.0f,%.0f)",
		frame.NetworkID, frame.EntityType, sourceNodeID, localX, localY)

	return entity
}

func (a *gameWorldAdapter) UpdateReplicaBase(entity ecs.Entity, localX, localY float32) {
	if a.gw.C.Position.HasAll(entity) {
		pos := a.gw.C.Position.Get(entity)
		pos.X = localX
		pos.Y = localY
	}
	if a.gw.C.Replica.HasAll(entity) {
		rep := a.gw.C.Replica.Get(entity)
		rep.TTL = 30
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

func (a *gameWorldAdapter) HandleCrossNodeAction(action *pkguniverse.CrossNodeAction) *pkguniverse.ActionResult {
	gw := a.gw
	var result *pkguniverse.ActionResult

	switch action.Type {
	case game.ActionDamage:
		dmg, err := game.UnmarshalDamageAction(action.Payload)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node damage: bad payload from node=%s: %v", action.SourceNodeID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.ECS.Alive(target) {
			gw.Log.Log(game.CatCombat, "cross-node damage: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceNodeID)
			return nil
		}

		damage := dmg.Damage
		if dmg.BonusDamage > 0 && gw.C.Shield.HasAll(target) {
			shield := gw.C.Shield.Get(target)
			if shield.Current <= 0 {
				damage += dmg.BonusDamage
			}
		}

		dealt := gw.ApplyDamage(target, damage, action.SourceNetID)
		gw.Log.Log(game.CatCombat, "cross-node damage: src=%d -> target=%d dmg=%.1f dealt=%.1f (from node=%s)",
			action.SourceNetID, action.TargetNetID, damage, dealt, action.SourceNodeID)

		dead := false
		if gw.C.Health.HasAll(target) {
			dead = gw.C.Health.Get(target).Current <= 0
		}

		result = &pkguniverse.ActionResult{
			Type:        game.ActionDamage,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
			Payload: game.MarshalDamageResult(&game.DamageResult{
				DamageDealt: dealt,
				TargetDead:  dead,
				Slot:        dmg.Slot,
				AbilityType: dmg.AbilityType,
			}),
		}

	case game.ActionStatusEffect:
		se, err := game.UnmarshalStatusEffectAction(action.Payload)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node status effect: bad payload from node=%s: %v", action.SourceNodeID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.ECS.Alive(target) {
			gw.Log.Log(game.CatCombat, "cross-node status effect: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceNodeID)
			return nil
		}

		if gw.C.StatusEffects.HasAll(target) {
			effects := gw.C.StatusEffects.Get(target)
			effects.Add(gamecomp.StatusEffect{
				Type:     gamecomp.StatusType(se.EffectType),
				Duration: se.Duration,
				Value:    se.Value,
				// Source is zero — cross-node DoT has no local entity reference
			})
			gw.Log.Log(game.CatCombat, "cross-node status effect: src=%d -> target=%d type=%d dur=%.1f val=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, se.EffectType, se.Duration, se.Value, action.SourceNodeID)
		}

		result = &pkguniverse.ActionResult{
			Type:        game.ActionStatusEffect,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
		}

	default:
		gw.Log.Log(game.CatCombat, "cross-node action: unknown type=%d from node=%s", action.Type, action.SourceNodeID)
		return nil
	}

	// Attach any side effects that accumulated during action handling
	if result != nil {
		if sideEffects := gw.SideEffects.Drain(); len(sideEffects) > 0 {
			result.SideEffects = pkguniverse.MarshalSideEffects(sideEffects)
		}
	}

	return result
}

func (a *gameWorldAdapter) HandleActionResult(result *pkguniverse.ActionResult) {
	gw := a.gw

	// Dispatch side effects generically via registry
	if len(result.SideEffects) > 0 {
		effects, err := pkguniverse.UnmarshalSideEffects(result.SideEffects)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node side effects: bad data: %v", err)
		} else {
			a.sideEffectRegistry.Dispatch(result.SourceNetID, effects)
		}
	}

	// Handle action-specific result payload
	switch result.Type {
	case game.ActionDamage:
		dmgResult, err := game.UnmarshalDamageResult(result.Payload)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node damage result: bad payload: %v", err)
			return
		}

		// Broadcast ability result to clients for VFX
		engine.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(dmgResult.Slot),
			CasterId:    result.SourceNetID,
			TargetId:    result.TargetNetID,
			DamageDealt: dmgResult.DamageDealt,
			Success:     true,
			AbilityType: uint32(dmgResult.AbilityType),
		})

		gw.Log.Log(game.CatCombat, "cross-node damage result: src=%d -> target=%d dealt=%.1f dead=%v",
			result.SourceNetID, result.TargetNetID, dmgResult.DamageDealt, dmgResult.TargetDead)

	case game.ActionStatusEffect:
		gw.Log.Log(game.CatCombat, "cross-node status effect result: src=%d -> target=%d success=%v",
			result.SourceNetID, result.TargetNetID, result.Success)
	}
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
