package game

import (
	"encoding/binary"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// ApplyDamage applies hitscan damage to a target entity.
// Handles Fortified damage reduction, shield absorption, and death.
// Returns the actual damage dealt.
func (gw *GameWorld) ApplyDamage(target ecs.Entity, damage float32, attackerNetID uint32) float32 {
	if !gw.eng.ECS.Alive(target) || !gw.C.Health.HasAll(target) {
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
	gw.eng.Log.Log(CatCombatHit, "hit: attacker=%d -> target=%d damage=%.1f (shield=%.1f) hp=%.1f/%.1f",
		attackerNetID, targetNetID, totalDamage, shieldAbsorbed, health.Current, health.Max)

	if health.Current <= 0 {
		gw.eng.Log.Log(CatCombatKill, "killed: target=%d by attacker=%d", targetNetID, attackerNetID)
		if gw.C.PlayerConn.HasAll(target) {
			gw.MarkPlayerDeath(target, attackerNetID)
		} else {
			gw.MarkNPCDeath(target, attackerNetID)
		}
	}

	return totalDamage
}

// MarkNPCDeath handles NPC death: credits currencies to attacker, drops non-currency loot, and removes entity.
func (gw *GameWorld) MarkNPCDeath(entity ecs.Entity, attackerNetID uint32) {
	if gw.C.Position.HasAll(entity) && gw.C.EntityKind.HasAll(entity) {
		pos := gw.C.Position.Get(entity)
		kind := gw.C.EntityKind.Get(entity)

		if table, ok := NPCDropTables[kind.Type]; ok {
			items := RollDrops(table)
			if len(items) > 0 {
				// Credit any currency drops directly to the attacker's account
				for itemID, qty := range items {
					if item.IsCurrency(itemID) {
						gw.rewardCurrency(itemID, attackerNetID, int64(qty))
						delete(items, itemID)
					}
				}

				// Drop remaining non-currency items as a loot crate
				if len(items) > 0 {
					mmokit.Enqueue(gw.Queue, PendingLootDrop{
						X:     pos.X,
						Y:     pos.Y,
						Items: items,
					})
					gw.eng.Log.Log(CatEconomyLoot, "npc drop: attacker=%d pos=(%.0f,%.0f) items=%v",
						attackerNetID, pos.X, pos.Y, items)
				}
			}
		}
	}

	gw.MarkForRemoval(entity)
}

// RewardCurrencyToLocal credits a currency to a player on this node by network ID.
// Used by the adapter to deliver cross-cell kill rewards.
func (gw *GameWorld) RewardCurrencyToLocal(netID uint32, currencyID uint32, amount int64) {
	attackerEntity, ok := gw.NetIDToEntity[netID]
	if !ok || !gw.eng.ECS.Alive(attackerEntity) {
		gw.eng.Log.Log(CatEconomyLoot, "currency reward (cross-cell): FAILED netID=%d not found in NetIDToEntity (ok=%v)", netID, ok)
		return
	}
	if !gw.C.PlayerConn.HasAll(attackerEntity) {
		gw.eng.Log.Log(CatEconomyLoot, "currency reward (cross-cell): FAILED netID=%d entity has no PlayerConn", netID)
		return
	}
	connID := gw.C.PlayerConn.Get(attackerEntity).ConnID
	s := gw.Players.ByConnID(connID)
	if s == nil || s.Username == "" {
		gw.eng.Log.Log(CatEconomyLoot, "currency reward (cross-cell): FAILED netID=%d conn=%d no username", netID, connID)
		return
	}
	username := s.Username

	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.AddCurrency(currencyID, amount)
	gw.PlayerDB.MarkDirty(username)

	gw.eng.Log.Log(CatEconomyLoot, "currency reward (cross-cell): player=%s currency=%d amount=%d balance=%d",
		username, currencyID, amount, pdata.GetCurrency(currencyID))

	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
		CurrencyId: currencyID,
		Balance:    pdata.GetCurrency(currencyID),
		Earned:     amount,
	})
}

// SideEffectCurrency is the side effect type for cross-cell currency rewards.
const SideEffectCurrency mmokit.SideEffectType = 1

// MarshalCurrencyReward encodes a currency reward (currencyID + amount) for cross-cell delivery.
func MarshalCurrencyReward(currencyID uint32, amount int64) []byte {
	buf := make([]byte, 12)
	binary.LittleEndian.PutUint32(buf[0:4], currencyID)
	binary.LittleEndian.PutUint64(buf[4:12], uint64(amount))
	return buf
}

// UnmarshalCurrencyReward decodes a currency reward from cross-cell delivery.
func UnmarshalCurrencyReward(data []byte) (currencyID uint32, amount int64) {
	if len(data) < 12 {
		return 0, 0
	}
	currencyID = binary.LittleEndian.Uint32(data[0:4])
	amount = int64(binary.LittleEndian.Uint64(data[4:12]))
	return
}

// rewardCurrency credits a currency to a player identified by their network ID and sends a balance update.
// If the attacker is not on this node (cross-cell kill), emits a side effect for delivery.
func (gw *GameWorld) rewardCurrency(currencyID uint32, netID uint32, amount int64) {
	attackerEntity, ok := gw.NetIDToEntity[netID]
	if !ok || !gw.eng.ECS.Alive(attackerEntity) || gw.C.Replica.HasAll(attackerEntity) {
		// Attacker is on another node (or only present as a replica) —
		// emit side effect for cross-cell delivery.
		gw.SideEffects.Emit(SideEffectCurrency, MarshalCurrencyReward(currencyID, amount))
		gw.eng.Log.Log(CatEconomyLoot, "currency reward (side-effect): attacker=%d currency=%d amount=%d", netID, currencyID, amount)
		return
	}
	if !gw.C.PlayerConn.HasAll(attackerEntity) {
		return
	}
	connID := gw.C.PlayerConn.Get(attackerEntity).ConnID
	s := gw.Players.ByConnID(connID)
	if s == nil || s.Username == "" {
		return
	}
	username := s.Username

	pdata := gw.PlayerDB.GetOrCreate(username)
	pdata.AddCurrency(currencyID, amount)
	gw.PlayerDB.MarkDirty(username)

	gw.eng.Log.Log(CatEconomyLoot, "currency reward: player=%s currency=%d amount=%d balance=%d",
		username, currencyID, amount, pdata.GetCurrency(currencyID))

	gw.ServerEvents().Send(gw.eng.ConnMgr, connID, uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE), &gamepb.CurrencyUpdateMsg{
		CurrencyId: currencyID,
		Balance:    pdata.GetCurrency(currencyID),
		Earned:     amount,
	})
}
