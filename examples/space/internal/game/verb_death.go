package game

import (
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/examples/space/internal/item"
	"github.com/mmokit/mmokit/pkg/mmokit"
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
	Target mmokit.Entity // dying entity (populated by handler — needed by AoI client renderer)
}

// KillCredit awards a currency drop to the killer. Server-internal — no AoI
// broadcast (registered via mmokit.HandleAllInternal in RegisterDeathVerbs,
// which omits the type from the broadcast registry). Cross-cell aware: when
// the killer is a replica on the dying entity's cell, the message routes
// via mmokit.Send to the killer's authoritative cell. This is the
// typed-message replacement for the legacy gw.SideEffects.Emit +
// ActionResult-drain path.
type KillCredit struct {
	Currency uint32
	Amount   int64
}

// killedHandler runs on the dying entity's authoritative cell. Branches on
// kind (PlayerConn presence), routes per-currency KillCredit to the killer,
// queues non-currency loot, marks the entity for removal.
func killedHandler(target mmokit.Entity, msg *Killed) {
	// Populate Target before any logic so the AoI broadcast carries the
	// dying entity's NetID for the client explosion renderer.
	msg.Target = target

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
	pdata := gw.PlayerDB.Bind(s)
	pdata.AddCurrency(msg.Currency, msg.Amount)
	gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)

	gw.eng.Log.Log(CatEconomyLoot, "kill credit: player=%s currency=%d amount=%d balance=%d",
		s.Username, msg.Currency, msg.Amount, pdata.GetCurrency(msg.Currency))

	mmokit.SendEvent(gw.stage, conn.ConnID, &CurrencyUpdate{
		CurrencyID: msg.Currency,
		Balance:    pdata.GetCurrency(msg.Currency),
		Earned:     msg.Amount,
	})
}

// RegisterDeathVerbs wires killedHandler and killCreditHandler onto every
// Stage owned by p. Call once at startup from GameSetup.
//
// Killed broadcasts (HandleAll) so AoI clients can render explosion FX.
// KillCredit is server-internal (HandleAllInternal) — currency rewards are
// pure server-side accounting; clients receive a CurrencyUpdate event from
// the handler, not the message itself.
func RegisterDeathVerbs(p *mmokit.Process) {
	mmokit.HandleAll(p, killedHandler)
	mmokit.HandleAllInternal(p, killCreditHandler)
}

// handlePlayerKilled is the per-kind body for player deaths. Sends the death
// cue to the player's client, captures non-soulbound inventory + equipment
// as a loot drop, preserves soulbound items on the player's save so the
// starter kit survives respawn (closing the die→respawn→loot-fresh-starter
// exploit), and transitions the session to StateDead.
func (gw *GameWorld) handlePlayerKilled(target mmokit.Entity, killer mmokit.Entity) {
	// Caller (killedHandler) verified PlayerConn presence via mmokit.Has[PlayerConn].
	connID := mmokit.Get[mmokit.PlayerConn](target).ConnID
	// Death cancels any in-flight supercruise (no lockout — respawn will
	// start with a fresh component).
	cancelSupercruise(target)

	mmokit.SendEvent(gw.stage, connID, &PlayerDied{KillerID: killer.NetID()})

	pos := mmokit.Get[mmokit.Position](target)
	if pos == nil {
		if s := gw.Players.ByConnID(connID); s != nil {
			gw.Players.Transition(s, StateDead)
		}
		return
	}

	// Split inventory + equipment into "dropped as loot" and "kept across
	// death". Soulbound items (starter kit, seeded weapons) stay on the
	// player's save so respawn restores them.
	var drops map[uint32]int32
	var keptCargo map[uint32]int32
	var keptEquip EquipmentSave

	if inv := mmokit.Get[gamecomp.Inventory](target); inv != nil && !inv.IsEmpty() {
		all := inv.Clear()
		for id, qty := range all {
			if item.IsSoulbound(id) {
				if keptCargo == nil {
					keptCargo = make(map[uint32]int32)
				}
				keptCargo[id] = qty
			} else {
				if drops == nil {
					drops = make(map[uint32]int32)
				}
				drops[id] += qty
			}
		}
	}
	if eq := mmokit.Get[gamecomp.Equipment](target); eq != nil {
		// Equipment slots — soulbound items stay in their slot for respawn;
		// non-soulbound items go to the loot crate.
		dropOrKeep := func(slot *uint32, save *uint32) {
			if *slot == 0 {
				return
			}
			if item.IsSoulbound(*slot) {
				*save = *slot
			} else {
				if drops == nil {
					drops = make(map[uint32]int32)
				}
				drops[*slot] += 1
			}
			*slot = 0
		}
		dropOrKeep(&eq.Weapon1, &keptEquip.Weapon1)
		dropOrKeep(&eq.Weapon2, &keptEquip.Weapon2)
		dropOrKeep(&eq.Shield, &keptEquip.Shield)
		dropOrKeep(&eq.Thruster, &keptEquip.Thruster)
	}

	if s := gw.Players.ByConnID(connID); s != nil {
		if s.Username != "" {
			pdata := gw.PlayerDB.Bind(s)
			pdata.Cargo = keptCargo
			pdata.Equipment = keptEquip
			// HasSave stays true when ANY soulbound item is kept — otherwise
			// the respawn path would fall into the "fresh player" branch and
			// hand out a brand-new starter kit + seed pack, which is the
			// exact exploit we're closing.
			pdata.HasSave = keptCargo != nil || !keptEquip.IsZero()
			gw.PlayerDB.MarkDirtyByUserID(pdata.UserID)
		}
		gw.Players.Transition(s, StateDead)
	}

	if len(drops) > 0 {
		// Defer — observer fires from OnTickEach iteration, where spawning
		// new entities would panic against the ark locked-world check. The
		// closure runs at the next Commands flush (between systems).
		x, y, items := pos.X, pos.Y, drops
		gw.stage.Commands().Defer(func() {
			gw.SpawnLootCrate(x, y, items)
		})
	}
}

// deathObserverBundle is the per-entity bundle the death observer iterates.
type deathObserverBundle struct {
	H *gamecomp.Health
}

// deathObserver fires Killed when Health.Current drops to zero. Idempotent
// via Health.DeathFired so cross-cell handoff during death doesn't double-fire.
// Killer is resolved at fire time from Health.LastDamagedByNetID via the
// stage's NetID index — survives cell transfer.
func deathObserver(e mmokit.Entity, b *deathObserverBundle, _ float32) {
	if b.H.Current > 0 || b.H.DeathFired {
		return
	}
	// DeathFired must be set BEFORE Send — Send is sync-when-local, so any
	// inner handler that re-reads Health must already see the flag set.
	b.H.DeathFired = true
	killer := mmokit.EntityByNetID(e.Stage(), b.H.LastDamagedByNetID)
	e.Send(&Killed{Killer: killer})
}

// handleNPCKilled is the per-kind body for NPC deaths. Rolls drops; routes
// each currency item via KillCredit (cross-cell aware); queues the
// non-currency remainder as a loot crate. Currency-only kills produce no
// loot crate.
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
		// Defer — observer fires from OnTickEach iteration, where spawning
		// new entities would panic against the ark locked-world check. The
		// closure runs at the next Commands flush (between systems).
		x, y, drops := pos.X, pos.Y, items
		gw.stage.Commands().Defer(func() {
			gw.SpawnLootCrate(x, y, drops)
		})
	}
}
