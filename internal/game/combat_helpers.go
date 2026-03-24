package game

import (
	"encoding/binary"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/internal/netutil"
	"github.com/zenion/mmoserver/pkg/engine"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// ApplyDamage applies hitscan damage to a target entity.
// Handles Fortified damage reduction, shield absorption, and death.
// Returns the actual damage dealt.
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attackerNetID uint32) float32 {
	if !gw.ECS.Alive(target) || !gw.C.Health.HasAll(target) {
		return 0
	}

	// Check Fortified buff for damage reduction
	if gw.C.StatusEffects.HasAll(target) {
		se := gw.C.StatusEffects.Get(target)
		if eff := se.Get(component.StatusFortified); eff != nil {
			damage *= (1.0 - eff.Value)
		}
	}

	health := gw.C.Health.Get(target)
	totalDamage := damage
	shieldAbsorbed := float32(0)

	// Shield absorbs damage first
	if gw.C.Shield.HasAll(target) {
		shield := gw.C.Shield.Get(target)
		shield.DamageCooldown = shield.RegenDelay
		if shield.Current > 0 {
			shieldAbsorbed = min(shield.Current, damage)
			shield.Current -= shieldAbsorbed
			damage -= shieldAbsorbed
		}
	}

	health.Current -= damage

	targetNetID := uint32(0)
	if gw.C.NetworkID.HasAll(target) {
		targetNetID = gw.C.NetworkID.Get(target).ID
	}
	gw.Log.Log(CatCombat, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
		attackerNetID, targetNetID, totalDamage, shieldAbsorbed, health.Current, health.Max)

	if health.Current <= 0 {
		gw.Log.Log(CatKill, "killed: target=%d by attacker=%d", targetNetID, attackerNetID)
		if gw.C.PlayerConn.HasAll(target) {
			gw.MarkPlayerDeath(target, attackerNetID)
		} else {
			gw.MarkNPCDeath(target, attackerNetID)
		}
	}

	return totalDamage
}

// MarkNPCDeath handles NPC death: credits Flux to attacker, drops non-Flux loot, and removes entity.
func (gw *GameWorld) MarkNPCDeath(entity ecs.Entity, attackerNetID uint32) {
	if gw.C.Position.HasAll(entity) && gw.C.EntityKind.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		kind := gw.C.EntityKind.Get(entity)

		if table, ok := NPCDropTables[kind.Type]; ok {
			items := RollDrops(table)
			if len(items) > 0 {
				// Credit Flux directly to the attacker's account
				if fluxQty, hasFlux := items[item.FluxItemID]; hasFlux {
					delete(items, item.FluxItemID)
					gw.rewardFlux(attackerNetID, int64(fluxQty))
				}

				// Drop remaining non-Flux items as a loot crate
				if len(items) > 0 {
					engine.Enqueue(gw.Queue, PendingLootDrop{
						X:     pos.X,
						Y:     pos.Y,
						Items: items,
					})
					gw.Log.Log(CatLoot, "npc drop: attacker=%d pos=(%.0f,%.0f) items=%v",
						attackerNetID, pos.X, pos.Y, items)
				}
			}
		}
	}

	gw.MarkForRemoval(entity)
}

// RewardFluxToLocal credits Flux to a player on this node by network ID.
// Used by the adapter to deliver cross-node kill rewards.
func (gw *GameWorld) RewardFluxToLocal(netID uint32, amount int64) {
	attackerEntity, ok := gw.NetIDToEntity[netID]
	if !ok || !gw.ECS.Alive(attackerEntity) {
		return
	}
	if !gw.C.PlayerConn.HasAll(attackerEntity) {
		return
	}
	connID := gw.C.PlayerConn.Get(attackerEntity).ConnID
	username, ok := gw.Players.Usernames[connID]
	if !ok {
		return
	}

	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.AddFlux(amount)
	gw.PlayerDB.MarkDirty(username)

	gw.Log.Log(CatLoot, "flux reward (cross-node): player=%s amount=%d balance=%d", username, amount, pdata.Flux)

	data := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_FLUX_UPDATE), &gamepb.FluxUpdateMsg{
		FluxBalance: pdata.Flux,
		FluxEarned:  amount,
	})
	if data != nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}

// SideEffectFlux is the side effect type for cross-node flux rewards.
const SideEffectFlux pkguniverse.SideEffectType = 1

// MarshalFluxReward encodes a flux amount for cross-node delivery.
func MarshalFluxReward(amount int64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(amount))
	return buf
}

// UnmarshalFluxReward decodes a flux amount from cross-node delivery.
func UnmarshalFluxReward(data []byte) int64 {
	if len(data) < 8 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(data))
}

// rewardFlux credits Flux to a player identified by their network ID and sends a balance update.
// If the attacker is not on this node (cross-node kill), emits a side effect for delivery.
func (gw *GameWorld) rewardFlux(netID uint32, amount int64) {
	attackerEntity, ok := gw.NetIDToEntity[netID]
	if !ok || !gw.ECS.Alive(attackerEntity) {
		// Attacker is on another node — emit side effect for cross-node delivery
		gw.SideEffects.Emit(SideEffectFlux, MarshalFluxReward(amount))
		gw.Log.Log(CatLoot, "flux reward (side-effect): attacker=%d amount=%d", netID, amount)
		return
	}
	if !gw.C.PlayerConn.HasAll(attackerEntity) {
		return
	}
	connID := gw.C.PlayerConn.Get(attackerEntity).ConnID
	username, ok := gw.Players.Usernames[connID]
	if !ok {
		return
	}

	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.AddFlux(amount)
	gw.PlayerDB.MarkDirty(username)

	gw.Log.Log(CatLoot, "flux reward: player=%s amount=%d balance=%d", username, amount, pdata.Flux)

	data := netutil.MakeEvent(uint32(gamepb.GameServerEventCode_GSE_FLUX_UPDATE), &gamepb.FluxUpdateMsg{
		FluxBalance: pdata.Flux,
		FluxEarned:  amount,
	})
	if data != nil {
		gw.ConnMgr.SendReliable(connID, data)
	}
}
