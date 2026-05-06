package bot

import (
	"encoding/binary"
	"log"
	"os"
	"reflect"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// botDebug enables verbose per-frame logging when BOT_DEBUG=1 is set.
// Off by default — bots run thousands of frames per second under load
// tests and even per-tick logs flood quickly.
var botDebug = os.Getenv("BOT_DEBUG") == "1"

// typeID lookups for the events the bot consumes. Computed once per
// process at package init from FNV-1a(reflect.Type.String()) — the
// same hash the server uses on the wire (mmokit.TypeIDOf).
var (
	tidPlayerEntityAssigned = mmokit.TypeIDOf(reflect.TypeFor[mmokit.PlayerEntityAssigned]())
	tidWorldDelta           = mmokit.TypeIDOf(reflect.TypeFor[mmokit.WorldDelta]())
	tidPong                 = mmokit.TypeIDOf(reflect.TypeFor[mmokit.Pong]())
	tidServerConfig         = mmokit.TypeIDOf(reflect.TypeFor[mmokit.ServerConfig]())
	tidCellChange           = mmokit.TypeIDOf(reflect.TypeFor[mmokit.CellChange]())
	tidDebugInfo            = mmokit.TypeIDOf(reflect.TypeFor[mmokit.DebugInfo]())

	tidPlayerSpawned  = mmokit.TypeIDOf(reflect.TypeFor[game.PlayerSpawned]())
	tidPlayerDied     = mmokit.TypeIDOf(reflect.TypeFor[game.PlayerDied]())
	tidPlayerOwnState = mmokit.TypeIDOf(reflect.TypeFor[game.PlayerOwnState]())
)

// decodeTypedEventFrame parses a 0x00 channel payload (channel byte
// already stripped) and dispatches each entry. Wire format per entry
// matches pkguniverse.EncodeBatchedTypedEventFrame:
//
//	[typeID:u32 LE][body_len:u32 LE][body bytes]
//
// Multiple entries may be concatenated within a single frame.
func (b *Bot) decodeTypedEventFrame(payload []byte) {
	const headerLen = 8
	off := 0
	if botDebug {
		dump := payload
		if len(dump) > 64 {
			dump = dump[:64]
		}
		log.Printf("[bot:%s] DEBUG event frame len=%d hex=% x", b.name, len(payload), dump)
	}
	for off+headerLen <= len(payload) {
		typeID := binary.LittleEndian.Uint32(payload[off : off+4])
		bodyLen := binary.LittleEndian.Uint32(payload[off+4 : off+8])
		off += headerLen
		if int(bodyLen) > len(payload)-off {
			log.Printf("[bot:%s] typed-event frame truncated typeID=%#x bodyLen=%d remaining=%d totalLen=%d",
				b.name, typeID, bodyLen, len(payload)-off, len(payload))
			return
		}
		body := payload[off : off+int(bodyLen)]
		off += int(bodyLen)
		if botDebug {
			log.Printf("[bot:%s] DEBUG entry typeID=%#x bodyLen=%d", b.name, typeID, bodyLen)
		}
		b.dispatchTypedEvent(typeID, body)
	}
	if off != len(payload) {
		log.Printf("[bot:%s] typed-event frame trailing bytes: remaining=%d totalLen=%d",
			b.name, len(payload)-off, len(payload))
	}
}

// dispatchTypedEvent decodes the body for known typeIDs. Unknown
// typeIDs are silently dropped — the bot is a partial client and
// only consumes events it acts on.
func (b *Bot) dispatchTypedEvent(typeID uint32, body []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[bot:%s] event handler panic typeID=%#x: %v", b.name, typeID, r)
		}
	}()

	switch typeID {
	case tidWorldDelta:
		var msg mmokit.WorldDelta
		pkguniverse.ReflectUnmarshal(body, &msg)
		b.applyWorldDelta(msg.Body)

	case tidPlayerEntityAssigned:
		var msg mmokit.PlayerEntityAssigned
		pkguniverse.ReflectUnmarshal(body, &msg)
		b.handleSpawned(msg.EntityNetID, "engine.PlayerEntityAssigned")

	case tidPlayerSpawned:
		var msg game.PlayerSpawned
		pkguniverse.ReflectUnmarshal(body, &msg)
		b.handleSpawned(msg.YourEntityID, "game.PlayerSpawned")

	case tidPlayerDied:
		var msg game.PlayerDied
		pkguniverse.ReflectUnmarshal(body, &msg)
		b.handleDied(msg.KillerID)

	case tidPlayerOwnState:
		var msg game.PlayerOwnState
		pkguniverse.ReflectUnmarshal(body, &msg)
		b.handleOwnState(&msg)

	// Engine events the bot doesn't act on — drop silently to avoid
	// noisy "unknown typeID" logs in normal operation.
	case tidPong, tidServerConfig, tidCellChange, tidDebugInfo:
		// no-op

	default:
		// Unknown typeID — likely a game-specific event the bot doesn't
		// model. Drop silently; logging every per-tick event would drown
		// the load-test output.
	}
}

// handleSpawned commits the bot's authoritative entity ID and fires
// spawnCh exactly once per session. Late PlayerSpawned arrivals after
// the engine-default PlayerEntityAssigned are tolerated — both events
// carry the same NetID for a fresh spawn.
func (b *Bot) handleSpawned(netID uint32, source string) {
	b.mu.Lock()
	first := b.myEntityID == 0
	b.myEntityID = netID
	b.alive = true
	b.mu.Unlock()

	if first {
		log.Printf("[bot:%s] spawned entityID=%d (via %s)", b.name, netID, source)
		select {
		case b.spawnCh <- struct{}{}:
		default:
		}
		if b.onSpawn != nil {
			b.onSpawn()
		}
	}
}

// handleDied marks the bot dead, drains the deathCh consumer (if
// present), and fires the OnDeath callback.
func (b *Bot) handleDied(killerID uint32) {
	b.mu.Lock()
	b.alive = false
	b.mu.Unlock()

	log.Printf("[bot:%s] died killer=%d", b.name, killerID)
	select {
	case b.deathCh <- killerID:
	default:
	}
	if b.onDeath != nil {
		b.onDeath(killerID)
	}
}

// handleOwnState mirrors the per-tick own-entity state into b.ownState.
// Field-by-field copy (rather than struct assignment) lets the bot keep
// its compact mirror types — game.PlayerOwnState carries fields the bot
// AIs don't currently consult (cooldowns, equipment) so we drop them.
func (b *Bot) handleOwnState(msg *game.PlayerOwnState) {
	cargo := make([]*InventoryItem, 0, len(msg.CargoItems))
	for _, item := range msg.CargoItems {
		cargo = append(cargo, &InventoryItem{
			ItemID:   item.ItemID,
			Quantity: item.Quantity,
		})
	}

	b.mu.Lock()
	b.ownState = OwnState{
		CargoItems:       cargo,
		CargoMass:        msg.CargoMass,
		MaxCargoMass:     msg.MaxCargoMass,
		LockProgress:     msg.LockProgress,
		LockTargetID:     msg.LockTargetID,
		LockedByID:       msg.BeingLockedByID,
		LockedByProgress: msg.BeingLockedByProgress,
	}
	b.mu.Unlock()
}

// applyWorldDelta decodes a WorldDelta body into the bot's WorldState
// mirror via the existing baseline+delta decoder. Updates b.state in
// place and fires the OnUpdate callback.
//
// PlayerSpawned may not have arrived yet at the moment the first
// WorldDelta lands (the framework dispatches PlayerEntityAssigned
// inline before any tick fires; the game's PlayerSpawned ships from
// the OnSpawn hook on the next tick boundary). So we additionally
// promote-on-sight: the first WorldDelta carrying our player's
// entity id (matched via PilotName) seeds b.alive.
func (b *Bot) applyWorldDelta(body []byte) {
	ws, ok := decodeBinaryFrame(body, b.baselines, b.decoders)
	if !ok {
		log.Printf("[bot:%s] WorldDelta decode failed body=%d bytes", b.name, len(body))
		return
	}

	b.mu.Lock()
	if b.state.Entities == nil {
		b.state.Entities = make(map[uint32]*EntitySnapshot)
	}
	for id, e := range ws.Entities {
		b.state.Entities[id] = e
	}
	for _, id := range ws.DestroyedIDs {
		delete(b.state.Entities, id)
	}
	for _, id := range ws.ExitedIDs {
		delete(b.state.Entities, id)
	}
	b.state.Tick = ws.Tick
	b.state.DestroyedIDs = ws.DestroyedIDs
	b.state.ExitedIDs = ws.ExitedIDs

	// Death-by-removal fallback: only fire dead when our entity ID
	// appears in this frame's DestroyedIDs (or ExitedIDs). Don't infer
	// death from absence — early frames may not include our ship at
	// all (we just spawned), and a partial-decode failure leaves the
	// state map empty. game.PlayerDied is the authoritative signal;
	// this catches the case where the server cleans up the entity
	// without sending PlayerDied.
	wasAlive := b.alive
	removedSelf := false
	for _, id := range ws.DestroyedIDs {
		if id == b.myEntityID && b.myEntityID != 0 {
			removedSelf = true
			break
		}
	}
	if removedSelf {
		b.alive = false
	}

	// Self-recognition fallback for PlayerSpawned-late race: scan
	// the freshly-arrived ships for one bearing our pilot name and
	// adopt its ID if we don't have one yet.
	if b.myEntityID == 0 {
		for id, e := range ws.Entities {
			if e.Type == gamepb.EntityType_ENTITY_TYPE_SHIP && e.PilotName == b.name {
				b.myEntityID = id
				b.alive = true
				select {
				case b.spawnCh <- struct{}{}:
				default:
				}
				if b.onSpawn != nil {
					b.onSpawn()
				}
				break
			}
		}
	}
	snapshot := b.state
	b.mu.Unlock()

	if wasAlive && removedSelf {
		log.Printf("[bot:%s] entity removed from world, marking dead", b.name)
		select {
		case b.deathCh <- 0:
		default:
		}
		if b.onDeath != nil {
			b.onDeath(0)
		}
	}

	if b.onUpdate != nil {
		b.onUpdate(&snapshot)
	}
}
