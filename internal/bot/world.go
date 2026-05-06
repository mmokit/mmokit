package bot

import (
	"encoding/binary"
	"math"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
	"github.com/zenion/mmoserver/pkg/quantize"
)

// Entity type constants (match internal/component and gamepb.EntityType).
const (
	typeShip      = uint8(gamepb.EntityType_ENTITY_TYPE_SHIP)
	typeAsteroid  = uint8(gamepb.EntityType_ENTITY_TYPE_ASTEROID)
	typeStation   = uint8(gamepb.EntityType_ENTITY_TYPE_STATION)
	typeLootCrate = uint8(gamepb.EntityType_ENTITY_TYPE_LOOT_CRATE)
	typeNPC       = uint8(gamepb.EntityType_ENTITY_TYPE_NPC)
)

// Quantization scales — must match server nethandler_shared.go.
const (
	qVelScale  = float32(2000.0)
	qSizeScale = float32(500.0)
)

// Snapshot field layouts — must match server nethandler_*.go SnapshotLayout().
var (
	// Base: relX(4), relY(4), vx(2), vy(2), rotation(2), radius(2), width(2), height(2), lockedByID(4), lockedByProgress(1)
	baseLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1}

	// Ship: base + healthCur(2), healthMax(2), shieldCur(2), shieldMax(2), flags(1), miningTargetID(4), combatTargetID(4)
	shipLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 2, 2, 2, 2, 1, 4, 4}

	// NPC: base + healthCur(2), healthMax(2), shieldCur(2), shieldMax(2)
	npcLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 2, 2, 2, 2}

	// Asteroid: base + itemID(4), remaining(4)
	asteroidLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, 4, 4}

	// Station: base only
	stationLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1}

	// LootCrate: base + variable tail (-1)
	lootCrateLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1, -1}
)

// InventoryItem is the bot's local mirror of game.InventoryItem — kept here
// to avoid a dependency on the typed-event registry until bot rewire lands
// (Phase 7+ TODO in bot.go).
type InventoryItem struct {
	ItemID   uint32
	Quantity int32
}

// EntitySnapshot holds a single entity's state from a world update.
type EntitySnapshot struct {
	ID                uint32
	Type              gamepb.EntityType
	PilotName         string
	X, Y              float32
	VX, VY            float32
	Rotation          float32
	Health, MaxHealth float32
	Shield, MaxShield float32
	Radius            float32
	MiningActive      bool
	MiningTargetID    uint32
	MiningBeamMask    uint32
	ItemID            uint32
	ResourceRemaining float32
	CargoItems        []*InventoryItem
	LockedByID        uint32
	LockedByProgress  float32
}

// OwnState holds state sent only to the owning player via the typed
// PlayerOwnState event. Fields that were sourced from typed-migrated messages
// (Cooldowns, Equipment, StatusEffects) are deferred until the bot rewires
// against the typed-event registry — see Phase 7+ TODO in bot.go.
type OwnState struct {
	CargoItems       []*InventoryItem
	CargoMass        float32
	MaxCargoMass     float32
	LockProgress     float32
	LockTargetID     uint32
	LockedByID       uint32
	LockedByProgress float32
}

// WorldState holds a complete snapshot of the visible world for one tick.
type WorldState struct {
	Tick         uint32
	Entities     map[uint32]*EntitySnapshot
	DestroyedIDs []uint32
	ExitedIDs    []uint32
}

// baselineEntry stores per-entity state for delta decoding.
type baselineEntry struct {
	snapshot   []byte
	entityType uint8
	pilotName  string // from initialData, doesn't change
}

// deltaDecoders holds pre-built DeltaEncoders per entity type.
type deltaDecoders struct {
	ship      *quantize.DeltaEncoder
	npc       *quantize.DeltaEncoder
	asteroid  *quantize.DeltaEncoder
	station   *quantize.DeltaEncoder
	lootCrate *quantize.DeltaEncoder
}

func newDeltaDecoders() *deltaDecoders {
	return &deltaDecoders{
		ship:      quantize.NewDeltaEncoder(shipLayout...),
		npc:       quantize.NewDeltaEncoder(npcLayout...),
		asteroid:  quantize.NewDeltaEncoder(asteroidLayout...),
		station:   quantize.NewDeltaEncoder(stationLayout...),
		lootCrate: quantize.NewDeltaEncoder(lootCrateLayout...),
	}
}

func (d *deltaDecoders) forType(entityType uint8) *quantize.DeltaEncoder {
	switch entityType {
	case typeShip:
		return d.ship
	case typeNPC:
		return d.npc
	case typeAsteroid:
		return d.asteroid
	case typeStation:
		return d.station
	case typeLootCrate:
		return d.lootCrate
	default:
		return nil
	}
}

// decodeBinaryFrame decodes a SE_DELTA_WORLD_UPDATE binary frame into a WorldState.
// It updates baselines in place for delta decoding across ticks.
func decodeBinaryFrame(data []byte, baselines map[uint32]*baselineEntry, decoders *deltaDecoders) (ws WorldState, ok bool) {
	// Guard against truncated/malformed UDP packets.
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	if len(data) < 24 {
		return WorldState{Entities: make(map[uint32]*EntitySnapshot)}, false
	}
	dec := quantize.NewFrameDecoder(data)
	hdr := dec.Header()

	ws = WorldState{
		Tick:     hdr.Tick,
		Entities: make(map[uint32]*EntitySnapshot, int(hdr.FullCount)+int(hdr.DeltaCount)),
	}

	// Full entities — store baseline and decode.
	for i := 0; i < int(hdr.FullCount); i++ {
		full := dec.NextFull()
		snap := make([]byte, len(full.Snapshot))
		copy(snap, full.Snapshot)

		var pilotName string
		if full.InitialData != nil {
			pilotName = decodeLengthPrefixedString(full.InitialData)
		}

		baselines[full.NetID] = &baselineEntry{
			snapshot:   snap,
			entityType: full.EntityType,
			pilotName:  pilotName,
		}

		es := decodeSnapshot(full.NetID, full.EntityType, snap)
		es.PilotName = pilotName
		ws.Entities[full.NetID] = es
	}

	// Delta entities — apply delta to baseline and decode.
	for i := 0; i < int(hdr.DeltaCount); i++ {
		delta := dec.NextDelta()
		bl, exists := baselines[delta.NetID]
		if !exists {
			continue // no baseline, skip
		}
		encoder := decoders.forType(delta.EntityType)
		if encoder == nil {
			continue
		}
		bl.snapshot = encoder.Decode(bl.snapshot, delta.Data)

		es := decodeSnapshot(delta.NetID, delta.EntityType, bl.snapshot)
		es.PilotName = bl.pilotName
		ws.Entities[delta.NetID] = es
	}

	// Destroyed IDs (entities removed from the world).
	for i := 0; i < int(hdr.RemovedCount); i++ {
		id := dec.NextUint32()
		ws.DestroyedIDs = append(ws.DestroyedIDs, id)
		delete(baselines, id)
	}

	// Exited IDs (left AoI, treat same as destroyed for bot purposes).
	for i := 0; i < int(hdr.ExitedCount); i++ {
		id := dec.NextUint32()
		ws.ExitedIDs = append(ws.ExitedIDs, id)
		delete(baselines, id)
	}

	return ws, true
}

// decodeSnapshot converts raw snapshot bytes into an EntitySnapshot.
// Positions in the snapshot are cell-local (from CellRelativePos on the server).
func decodeSnapshot(netID uint32, entityType uint8, snap []byte) *EntitySnapshot {
	r := quantize.NewSnapshotReader(snap)

	// Base fields (all entity types): 25 bytes
	x := r.Float32()
	y := r.Float32()
	vx := r.UnQVel(qVelScale)
	vy := r.UnQVel(qVelScale)
	rotation := r.UnQAngle()
	radius := r.UnQVel(qSizeScale)
	_ = r.UnQVel(qSizeScale) // width — bot doesn't need
	_ = r.UnQVel(qSizeScale) // height — bot doesn't need
	lockedByID := r.Uint32()
	lockedByProgress := r.UnQNorm()

	es := &EntitySnapshot{
		ID:               netID,
		Type:             gamepb.EntityType(entityType),
		X:                x,
		Y:                y,
		VX:               vx,
		VY:               vy,
		Rotation:         rotation,
		Radius:           radius,
		LockedByID:       lockedByID,
		LockedByProgress: lockedByProgress,
	}

	switch entityType {
	case typeShip, typeNPC:
		// Combat fields: healthCur(2), healthMax(2), shieldCur(2), shieldMax(2)
		es.Health = float32(r.Uint16())
		es.MaxHealth = float32(r.Uint16())
		es.Shield = float32(r.Uint16())
		es.MaxShield = float32(r.Uint16())

		if entityType == typeShip {
			// Mining: flags(1), miningTargetID(4), combatTargetID(4)
			flags := r.Uint8()
			es.MiningActive = flags != 0
			es.MiningBeamMask = uint32(flags)
			es.MiningTargetID = r.Uint32()
			_ = r.Uint32() // combatTargetID
		}

	case typeAsteroid:
		es.ItemID = r.Uint32()
		es.ResourceRemaining = r.Float32()

	case typeLootCrate:
		if r.Remaining() >= 2 {
			tailLen := int(r.Uint16())
			if tailLen > 0 && r.Remaining() >= 1 {
				count := r.Uint8()
				for j := uint8(0); j < count && r.Remaining() >= 8; j++ {
					itemID := r.Uint32()
					qty := r.Uint32()
					es.CargoItems = append(es.CargoItems, &InventoryItem{
						ItemID:   itemID,
						Quantity: int32(qty),
					})
				}
			}
		}
	}

	return es
}

// decodeLengthPrefixedString decodes a uint16-length-prefixed UTF-8 string.
func decodeLengthPrefixedString(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	length := int(binary.BigEndian.Uint16(data[0:2]))
	if length == 0 || 2+length > len(data) {
		return ""
	}
	return string(data[2 : 2+length])
}

// Keep unused import guard — math is used indirectly via quantize, but we declare
// the constant here for readability.
var _ = math.MaxFloat32
