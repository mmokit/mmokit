package universe

import (
	"encoding/binary"

	"github.com/mlange-42/ark/ecs"
)

// Border frame component-slice codec.
//
// BorderDispatcher emits per-entity entries with a fixed 28-byte header
// followed by a length-prefixed list of per-component data. The full
// per-entity DeltaBuf layout is:
//
//	[4]  worldX        float32 LE
//	[4]  worldY        float32 LE
//	[4]  radius        float32 LE
//	[2]  qvx           int16 LE
//	[2]  qvy           int16 LE
//	[2]  qangle        uint16 LE — quantize.Angle(Rotation.Angle) in [-pi, pi]
//	[8]  producedAtMs  uint64 LE — authoritative producer's ClusterClock.TickTime (tick-aligned)
//	[2]  componentCount uint16 LE (or 0xFFFF = unchanged sentinel)
//	repeated componentCount times:
//	  [2] componentID  uint16 LE  (ReplicationRegistry auto-assigned ID)
//	  [2] dataLen      uint16 LE  (opaque component bytes, max 64 KiB)
//	  [N] data
//
// A zero count is valid and takes 2 bytes. The 0xFFFF sentinel means
// "tail unchanged since last frame" — the receiver leaves existing
// replica components in place.
//
// Component IDs are coordinated implicitly: both nodes register the same
// components in the same order, so IDs match. Unknown IDs on the receiver
// are silently skipped (forward-compatible with schema drift), and the
// length prefix lets the decoder advance past unknown components safely.

// scanEntityComponents appends a component-slice tail for entity onto
// dst using the registered ReplicationRegistry. Returns the extended
// buffer. If the registry is nil or has no components, emits just the
// 2-byte zero count.
func (b *Stage) scanEntityComponents(entity ecs.Entity, dst []byte) []byte {
	// Reserve 2 bytes for the count; back-patch after scanning.
	countOffset := len(dst)
	dst = append(dst, 0, 0)

	if b.replRegistry == nil {
		return dst
	}

	var count uint16
	for _, cr := range b.replRegistry.All() {
		if cr.Scan == nil {
			continue
		}
		// Skip only what the BORDER HEADER carries (Position, Velocity,
		// Rotation, CellCoord). Their authoritative border values are
		// already encoded in the fixed DeltaBuf fields, and including them
		// in the tail would make applyEntityComponents overwrite the
		// correctly-computed local position with the source's raw value.
		//
		// NOT SkipOnTransfer, which is a larger set: Collider is in it
		// because TransferFrame carries all 18 bytes, while this header
		// carries the radius alone. Using the transfer set here is what
		// shipped a neighbour-owned entity with no extents, no layer and no
		// shape — invisible to every layer-masked query.
		if cr.SkipOnBorder {
			continue
		}
		data := cr.Scan(entity)
		if data == nil {
			continue
		}
		if len(data) > 0xFFFF {
			// Component serialized form exceeds uint16 length prefix.
			// Skip rather than truncate — a partial component would
			// decode to garbage on the receiver.
			continue
		}
		dst = binary.LittleEndian.AppendUint16(dst, uint16(cr.ID))
		dst = binary.LittleEndian.AppendUint16(dst, uint16(len(data)))
		dst = append(dst, data...)
		count++
	}

	binary.LittleEndian.PutUint16(dst[countOffset:countOffset+2], count)
	return dst
}

// applyEntityComponents reads a component-slice tail starting at the
// beginning of tail and applies each known component onto entity using
// the receiver's ReplicationRegistry. Unknown component IDs are skipped
// but the decoder still advances past their data via the length prefix.
//
// A componentCount of borderTailUnchanged (0xFFFF) is the delta-
// compression sentinel meaning "the sender's tail bytes for this entity
// are identical to the last tail it sent me, reuse whatever I already
// have". The receiver treats this as a no-op: the replica entity's
// existing component values (created on the previous frame or via
// EnsureEntityKindComponents) stay in place.
//
// Truncated tails (shorter than declared) stop the decode silently
// without applying partial data. This is defensive against malformed
// frames from a misbehaving peer — the component values already on the
// entity remain as the fallback.
func (b *Stage) applyEntityComponents(entity ecs.Entity, tail []byte) {
	if len(tail) < 2 {
		return
	}
	count := binary.LittleEndian.Uint16(tail[0:2])
	if count == borderTailUnchanged {
		return // sentinel: components unchanged since last frame
	}
	pos := 2

	for range count {
		if pos+4 > len(tail) {
			return // truncated header
		}
		id := binary.LittleEndian.Uint16(tail[pos : pos+2])
		dlen := binary.LittleEndian.Uint16(tail[pos+2 : pos+4])
		pos += 4
		if pos+int(dlen) > len(tail) {
			return // truncated data
		}
		data := tail[pos : pos+int(dlen)]
		pos += int(dlen)

		if b.replRegistry == nil {
			continue
		}
		rep := b.replRegistry.Get(ComponentID(id))
		if rep == nil {
			continue // unknown component — skip but keep advancing
		}
		// Prefer Apply (updates existing component in place). Fall back
		// to Add if the component type only registered Add. The replica
		// already has all kind-registered components via
		// EnsureEntityKindComponents, so Apply should always find its
		// target — but Add is safe either way (it detects existing
		// components and updates them in place).
		//
		// Skip only what the border header carries — see the encode side.
		if rep.SkipOnBorder {
			continue
		}
		// A refused blob skips this component and the walk continues to the
		// next one: the tail's framing is still trustworthy (dlen was honored
		// above), and the replica keeps every component that did decode. See
		// noteComponentDecodeDrop for the policy and its trade.
		var err error
		if rep.Apply != nil {
			err = rep.Apply(entity, data)
		} else if rep.Add != nil {
			err = rep.Add(entity, data)
		}
		if err != nil {
			noteComponentDecodeDrop("border replica", ComponentID(id), err)
		}
	}
}
