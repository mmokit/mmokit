package game

import (
	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// gameWorldAdapter wraps *GameWorld to implement mmokit.GameWorld.
// Embeds WorldBase for all lifecycle defaults (replica/ghost/cooldown management,
// border scanning, etc.) and only overrides game-specific methods.
type gameWorldAdapter struct {
	*mmokit.WorldBase
	gw                 *GameWorld
	sideEffectRegistry *mmokit.SideEffectRegistry
}

func newGameWorldAdapter(base *mmokit.WorldBase, gw *GameWorld, seRegistry *mmokit.SideEffectRegistry) *gameWorldAdapter {
	return &gameWorldAdapter{
		WorldBase:          base,
		gw:                 gw,
		sideEffectRegistry: seRegistry,
	}
}

func (a *gameWorldAdapter) Init() {
	replRegistry := buildReplicationRegistry(a.gw)
	a.SetReplicationRegistry(replRegistry)

	a.SetOnTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		a.gw.FinishTransferSpawn(entity, frame)
	})

	a.SetOnPlayerTransferReceived(func(entity mmokit.Entity, frame *mmokit.TransferFrame) {
		if s := a.Engine().Players.ByConnID(frame.ConnID); s != nil {
			a.gw.WireTransferPlayer(entity, s)
		}
		if a.gw.PlayerSessions != nil {
			a.gw.PlayerSessions.Set(frame.ConnID, frame.Username)
		}

		secFrame := mmokit.MakeEvent(uint32(enginepb.ServerEventCode_SE_CELL_CHANGE), &enginepb.CellChangeMsg{
			CellX: frame.CellX,
			CellY: frame.CellY,
		})
		if secFrame != nil {
			a.gw.ConnMgr.SendReliable(frame.ConnID, secFrame)
		}
		mapFrame := mmokit.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_MAP_DATA), &gamepb.MapDataMsg{
			Stations: a.gw.CollectStationMapData(),
		})
		if mapFrame != nil {
			a.gw.ConnMgr.SendReliable(frame.ConnID, mapFrame)
		}
	})
}

// GW returns the underlying *GameWorld for direct access (e.g., console commands).
func (a *gameWorldAdapter) GW() *GameWorld {
	return a.gw
}

// --- Game-specific overrides ---

func (a *gameWorldAdapter) SetBridge(bridge mmokit.NodeBridge) {
	a.WorldBase.SetBridge(bridge)
	a.gw.Bridge = bridge
}

func (a *gameWorldAdapter) DispatchChat(username, text string) {
	a.gw.Log.Log(CatPlayerChat, "inbox: relayed chat <%s> %s", username, text)
	mmokit.Enqueue(a.gw.Queue, &enginepb.ChatMsg{
		Username: username,
		Text:     text,
	})
}

func (a *gameWorldAdapter) HandleCrossNodeAction(action *mmokit.CrossNodeAction) *mmokit.ActionResult {
	gw := a.gw
	var result *mmokit.ActionResult

	switch action.Type {
	case ActionDamage:
		dmg, err := UnmarshalDamageAction(action.Payload)
		if err != nil {
			gw.Log.Log(CatCombatAbility, "cross-node damage: bad payload from node=%s: %v", action.SourceNodeID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.ECS.Alive(target) {
			gw.Log.Log(CatCombatAbility, "cross-node damage: target netID=%d not found (from node=%s)",
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
		gw.Log.Log(CatCombatAbility, "cross-node damage: src=%d -> target=%d dmg=%.1f dealt=%.1f (from node=%s)",
			action.SourceNetID, action.TargetNetID, damage, dealt, action.SourceNodeID)

		dead := false
		if gw.C.Health.HasAll(target) {
			dead = gw.C.Health.Get(target).Current <= 0
		}

		result = &mmokit.ActionResult{
			Type:        ActionDamage,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
			Payload: MarshalDamageResult(&DamageResult{
				DamageDealt: dealt,
				TargetDead:  dead,
				Slot:        dmg.Slot,
				AbilityType: dmg.AbilityType,
			}),
		}

	case ActionStatusEffect:
		se, err := UnmarshalStatusEffectAction(action.Payload)
		if err != nil {
			gw.Log.Log(CatCombatAbility, "cross-node status effect: bad payload from node=%s: %v", action.SourceNodeID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.ECS.Alive(target) {
			gw.Log.Log(CatCombatAbility, "cross-node status effect: target netID=%d not found (from node=%s)",
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
			gw.Log.Log(CatCombatAbility, "cross-node status effect: src=%d -> target=%d type=%d dur=%.1f val=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, se.EffectType, se.Duration, se.Value, action.SourceNodeID)
		}

		result = &mmokit.ActionResult{
			Type:        ActionStatusEffect,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
		}

	case ActionMining:
		mining, err := UnmarshalMiningAction(action.Payload)
		if err != nil {
			gw.Log.Log(CatEconomyMining, "cross-node mining: bad payload from node=%s: %v", action.SourceNodeID, err)
			return nil
		}

		target, ok := gw.NetIDToEntity[action.TargetNetID]
		if !ok || !gw.ECS.Alive(target) {
			gw.Log.Log(CatEconomyMining, "cross-node mining: target netID=%d not found (from node=%s)",
				action.TargetNetID, action.SourceNodeID)
			return nil
		}

		if !gw.C.Minable.HasAll(target) {
			gw.Log.Log(CatEconomyMining, "cross-node mining: target netID=%d not minable (from node=%s)",
				action.TargetNetID, action.SourceNodeID)
			return nil
		}

		minable := gw.C.Minable.Get(target)
		extracted := mining.Amount
		if extracted > minable.Remaining {
			extracted = minable.Remaining
		}
		minable.Remaining -= extracted

		depleted := minable.Remaining <= 0
		if depleted {
			gw.MarkForRemoval(target)
			gw.Log.Log(CatEconomyMining, "cross-node mining: asteroid netID=%d depleted (from node=%s)",
				action.TargetNetID, action.SourceNodeID)
		} else {
			gw.Log.Log(CatEconomyMining, "cross-node mining: src=%d -> target=%d extracted=%.1f remaining=%.1f (from node=%s)",
				action.SourceNetID, action.TargetNetID, extracted, minable.Remaining, action.SourceNodeID)
		}

		result = &mmokit.ActionResult{
			Type:        ActionMining,
			TargetNetID: action.TargetNetID,
			SourceNetID: action.SourceNetID,
			Success:     true,
			Payload:     MarshalMiningResult(&MiningResult{Extracted: extracted, Depleted: depleted}),
		}

	default:
		gw.Log.Log(CatCombatAbility, "cross-node action: unknown type=%d from node=%s", action.Type, action.SourceNodeID)
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
			gw.Log.Log(CatCombatAbility, "cross-node side effects: bad data: %v", err)
		} else {
			a.sideEffectRegistry.Dispatch(result.SourceNetID, effects)
		}
	}

	switch result.Type {
	case ActionDamage:
		dmgResult, err := UnmarshalDamageResult(result.Payload)
		if err != nil {
			gw.Log.Log(CatCombatAbility, "cross-node damage result: bad payload: %v", err)
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

		gw.Log.Log(CatCombatAbility, "cross-node damage result: src=%d -> target=%d dealt=%.1f dead=%v",
			result.SourceNetID, result.TargetNetID, dmgResult.DamageDealt, dmgResult.TargetDead)

	case ActionStatusEffect:
		gw.Log.Log(CatCombatAbility, "cross-node status effect result: src=%d -> target=%d success=%v",
			result.SourceNetID, result.TargetNetID, result.Success)

	case ActionMining:
		miningResult, err := UnmarshalMiningResult(result.Payload)
		if err != nil {
			gw.Log.Log(CatEconomyMining, "cross-node mining result: bad payload: %v", err)
			return
		}

		if miningResult.Depleted {
			replicaNetIDs := a.ReplicaNetIDs()
			if replicaEntity, ok := replicaNetIDs[result.TargetNetID]; ok {
				if gw.ECS.Alive(replicaEntity) {
					gw.ECS.RemoveEntity(replicaEntity)
				}
				delete(replicaNetIDs, result.TargetNetID)
			}
			gw.RemovedNetIDs = append(gw.RemovedNetIDs, result.TargetNetID)
		}

		gw.Log.Log(CatEconomyMining, "cross-node mining result: target=%d extracted=%.1f depleted=%v",
			result.TargetNetID, miningResult.Extracted, miningResult.Depleted)
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

// UnwrapGameWorld extracts the underlying *GameWorld from a mmokit.GameWorld.
func UnwrapGameWorld(w mmokit.GameWorld) *GameWorld {
	return w.(*gameWorldAdapter).gw
}
