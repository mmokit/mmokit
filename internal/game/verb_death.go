package game

import (
	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// Killed is dispatched by the death observer (factory.GameSetup, see Phase 3)
// when an entity's Health.Current drops to zero. The handler runs on the
// dying entity's authoritative cell and is responsible for cleanup (loot,
// client notification, removal) and for forwarding currency rewards back to
// the killer via KillCredit.
//
// Killer may be the zero Entity if the entity died of unattributed damage
// (environmental, /kill admin command). The handler treats that as a no-op
// for kill-credit purposes.
type Killed struct {
	Killer mmokit.Entity
}

// KillCredit awards a currency drop to the killer. Server-internal — no AoI
// broadcast. Cross-cell aware: when the killer is a replica on the dying
// entity's cell, the message routes via mmokit.Send to the killer's
// authoritative cell. This is the typed-message replacement for the legacy
// gw.SideEffects.Emit + ActionResult-drain path.
type KillCredit struct {
	Currency uint32
	Amount   int64
}

// serverOnly marks KillCredit as a server-internal message — skips AoI
// broadcast. The framework currently routes typed messages point-to-point
// only (no automatic broadcast), so this method is a documentation /
// safety marker that pins KillCredit's intent for future broadcast work.
func (KillCredit) serverOnly() {}

// killedHandler runs on the dying entity's authoritative cell. Branches on
// kind (PlayerConn presence), routes per-currency KillCredit to the killer,
// queues non-currency loot, marks the entity for removal.
func killedHandler(target mmokit.Entity, msg *Killed) {
	gw := gameWorldOfEntity(target)
	if gw == nil {
		return
	}
	gw.eng.Log.Log(CatCombatKill, "killed: target=%d killer=%d",
		target.NetID(), msg.Killer.NetID())

	if mmokit.Has[mmokit.PlayerConn](target) {
		gw.handlePlayerKilled(target, msg.Killer)
	} else {
		gw.handleNPCKilled(target, msg.Killer)
	}

	gw.MarkForRemoval(target.Handle())
}

// killCreditHandler runs on the killer's authoritative cell. Credits the
// currency to the player's bank and pushes a CurrencyUpdate event to their
// client. Replaces the legacy rewardCurrency / RewardCurrencyToLocal pair.
func killCreditHandler(killer mmokit.Entity, msg *KillCredit) {
	gw := gameWorldOfEntity(killer)
	if gw == nil {
		return
	}
	conn := mmokit.Get[mmokit.PlayerConn](killer)
	if conn == nil {
		return
	}
	s := gw.Players.ByConnID(conn.ConnID)
	if s == nil || s.Username == "" {
		return
	}
	pdata := gw.PlayerDB.GetOrCreate(s.Username)
	pdata.AddCurrency(msg.Currency, msg.Amount)
	gw.PlayerDB.MarkDirty(s.Username)

	gw.eng.Log.Log(CatEconomyLoot, "kill credit: player=%s currency=%d amount=%d balance=%d",
		s.Username, msg.Currency, msg.Amount, pdata.GetCurrency(msg.Currency))

	// ServerEvents may be nil in unit tests where the stage isn't bound to
	// a Process. Production binds Process at coordinator-create time.
	if events := gw.ServerEvents(); events != nil {
		events.Send(gw.eng.ConnMgr, conn.ConnID,
			uint32(gamepb.GameServerEventCode_GSE_CURRENCY_UPDATE),
			&gamepb.CurrencyUpdateMsg{
				CurrencyId: msg.Currency,
				Balance:    pdata.GetCurrency(msg.Currency),
				Earned:     msg.Amount,
			})
	}
}

// RegisterDeathVerbs wires killedHandler and killCreditHandler onto every
// Stage owned by p. Call once at startup from GameSetup.
func RegisterDeathVerbs(p *mmokit.Process) {
	mmokit.HandleAll(p, killedHandler)
	mmokit.HandleAll(p, killCreditHandler)
}

// handlePlayerKilled is the per-kind body for player deaths. Sends the death
// cue to the player's client, captures inventory + equipment as a loot drop,
// and transitions the player session to StateDead. Absorbs the body of the
// legacy gw.MarkPlayerDeath method.
func (gw *GameWorld) handlePlayerKilled(target mmokit.Entity, killer mmokit.Entity) {
	// Caller (killedHandler) verified PlayerConn presence via mmokit.Has[PlayerConn].
	connID := mmokit.Get[mmokit.PlayerConn](target).ConnID

	// ServerEvents may be nil in unit tests where the stage isn't bound to
	// a Process. Production binds Process at coordinator-create time.
	if events := gw.ServerEvents(); events != nil {
		events.Send(gw.eng.ConnMgr, connID,
			uint32(gamepb.GameServerEventCode_GSE_PLAYER_DIED),
			&gamepb.PlayerDiedMsg{KillerId: killer.NetID()})
	}

	if s := gw.Players.ByConnID(connID); s != nil {
		if s.Username != "" {
			// Clear saved state so respawn places them near the station.
			pdata := gw.PlayerDB.GetOrCreate(s.Username)
			pdata.Cargo = nil
			pdata.Equipment = EquipmentSave{}
			pdata.HasSave = false
			gw.PlayerDB.MarkDirty(s.Username)
		}
		gw.Players.Transition(s, StateDead)
	}

	// Capture inventory + equipment for loot crate drop.
	pos := mmokit.Get[mmokit.Position](target)
	if pos == nil {
		return
	}
	var items map[uint32]int32

	if inv := mmokit.Get[gamecomp.Inventory](target); inv != nil && !inv.IsEmpty() {
		items = inv.Clear()
	}
	if eq := mmokit.Get[gamecomp.Equipment](target); eq != nil {
		for _, eqID := range []uint32{eq.Weapon1, eq.Weapon2, eq.Shield, eq.Thruster} {
			if eqID != 0 {
				if items == nil {
					items = make(map[uint32]int32)
				}
				items[eqID] += 1
			}
		}
		eq.Weapon1 = 0
		eq.Weapon2 = 0
		eq.Shield = 0
		eq.Thruster = 0
	}
	if len(items) > 0 {
		// Deferred — observer fires from OnTickEach iteration, where spawning new
		// entities is unsafe. PendingLootDrop is drained in postFlush.
		mmokit.Enqueue(gw.Queue, PendingLootDrop{X: pos.X, Y: pos.Y, Items: items})
	}
}

// handleNPCKilled is the per-kind body for NPC deaths. Rolls drops; routes
// each currency item via KillCredit (cross-cell aware); queues the
// non-currency remainder as a loot crate. Currency-only kills produce no
// loot crate. Absorbs the body of the legacy gw.MarkNPCDeath method.
func (gw *GameWorld) handleNPCKilled(target mmokit.Entity, killer mmokit.Entity) {
	pos := mmokit.Get[mmokit.Position](target)
	kind := mmokit.Get[mmokit.EntityKind](target)
	if pos == nil || kind == nil {
		return
	}
	table, ok := NPCDropTables[kind.Type]
	if !ok {
		return
	}
	items := RollDrops(table)
	if len(items) == 0 {
		return
	}

	// Currency drops route to killer via KillCredit (cross-cell aware).
	for itemID, qty := range items {
		if !item.IsCurrency(itemID) {
			continue
		}
		if killer.Alive() {
			killer.Send(&KillCredit{Currency: itemID, Amount: int64(qty)})
		} else {
			gw.eng.Log.Log(CatEconomyLoot, "currency drop: dropped (no live killer): currency=%d amount=%d", itemID, qty)
		}
		delete(items, itemID)
	}

	// Non-currency items go into a loot crate.
	if len(items) > 0 {
		// Deferred — observer fires from OnTickEach iteration, where spawning new
		// entities is unsafe. PendingLootDrop is drained in postFlush.
		mmokit.Enqueue(gw.Queue, PendingLootDrop{X: pos.X, Y: pos.Y, Items: items})
	}
}
