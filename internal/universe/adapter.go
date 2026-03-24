package universe

import (
	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// gameWorldAdapter wraps *game.GameWorld to implement mmokit.GameWorld.
// Embeds WorldBase for all lifecycle defaults (replica/ghost/cooldown management,
// border scanning, etc.) and only overrides game-specific methods.
type gameWorldAdapter struct {
	mmokit.WorldBase
	gw                 *game.GameWorld
	sideEffectRegistry *mmokit.SideEffectRegistry
}

func newGameWorldAdapter(base *mmokit.WorldBase, gw *game.GameWorld, seRegistry *mmokit.SideEffectRegistry) *gameWorldAdapter {
	return &gameWorldAdapter{
		WorldBase:          *base,
		gw:                 gw,
		sideEffectRegistry: seRegistry,
	}
}

// GW returns the underlying *game.GameWorld for direct access (e.g., console commands).
func (a *gameWorldAdapter) GW() *game.GameWorld {
	return a.gw
}

// --- Game-specific overrides ---

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

func (a *gameWorldAdapter) SetBridge(bridge mmokit.NodeBridge) {
	a.WorldBase.SetBridge(bridge)
	a.gw.Bridge = bridge
}

func (a *gameWorldAdapter) DispatchChat(username, text string) {
	a.gw.Log.Log(game.CatChat, "inbox: relayed chat <%s> %s", username, text)
	mmokit.Enqueue(a.gw.Queue, &enginepb.ChatMsg{
		Username: username,
		Text:     text,
	})
}

func (a *gameWorldAdapter) RegisterPendingLogin(connID uint32, username string) {
	a.gw.Log.Log(game.CatConnect, "inbox: respawn transfer conn=%d username=%s", connID, username)
	a.gw.Players.Usernames[connID] = username
	a.gw.Players.PendingLogins[connID] = username
}

func (a *gameWorldAdapter) HandleCrossNodeAction(action *mmokit.CrossNodeAction) *mmokit.ActionResult {
	gw := a.gw
	var result *mmokit.ActionResult

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

		result = &mmokit.ActionResult{
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
			})
			gw.Log.Log(game.CatCombat, "cross-node status effect: src=%d -> target=%d type=%d dur=%.1f val=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, se.EffectType, se.Duration, se.Value, action.SourceNodeID)
		}

		result = &mmokit.ActionResult{
			Type:        game.ActionStatusEffect,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
		}

	default:
		gw.Log.Log(game.CatCombat, "cross-node action: unknown type=%d from node=%s", action.Type, action.SourceNodeID)
		return nil
	}

	if result != nil {
		if sideEffects := gw.SideEffects.Drain(); len(sideEffects) > 0 {
			result.SideEffects = mmokit.MarshalSideEffects(sideEffects)
		}
	}

	return result
}

func (a *gameWorldAdapter) HandleActionResult(result *mmokit.ActionResult) {
	gw := a.gw

	if len(result.SideEffects) > 0 {
		effects, err := mmokit.UnmarshalSideEffects(result.SideEffects)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node side effects: bad data: %v", err)
		} else {
			a.sideEffectRegistry.Dispatch(result.SourceNetID, effects)
		}
	}

	switch result.Type {
	case game.ActionDamage:
		dmgResult, err := game.UnmarshalDamageResult(result.Payload)
		if err != nil {
			gw.Log.Log(game.CatCombat, "cross-node damage result: bad payload: %v", err)
			return
		}

		mmokit.Enqueue(gw.Queue, &gamepb.AbilityCastResultMsg{
			Slot:        uint32(dmgResult.Slot),
			CasterId:    result.SourceNetID,
			TargetId:    result.TargetNetID,
			DamageDealt: dmgResult.DamageDealt,
			Success:     true,
			AbilityType: uint32(dmgResult.AbilityType),
		})

		if dmgResult.TargetDead {
			replicaNetIDs := a.ReplicaNetIDs()
			if replicaEntity, ok := replicaNetIDs[result.TargetNetID]; ok {
				if gw.ECS.Alive(replicaEntity) {
					gw.ECS.RemoveEntity(replicaEntity)
				}
				delete(replicaNetIDs, result.TargetNetID)
			}
			gw.RemovedNetIDs = append(gw.RemovedNetIDs, result.TargetNetID)
		}

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

func (a *gameWorldAdapter) Hooks() mmokit.Hooks {
	return a.gw.Hooks()
}

// Ensure gameWorldAdapter implements mmokit.GameWorld at compile time.
var _ mmokit.GameWorld = (*gameWorldAdapter)(nil)

// UnwrapGameWorld extracts the underlying *game.GameWorld from a mmokit.GameWorld.
func UnwrapGameWorld(w mmokit.GameWorld) *game.GameWorld {
	return w.(*gameWorldAdapter).gw
}
