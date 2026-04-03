package system

import (
	"encoding/binary"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
	"github.com/zenion/mmoserver/pkg/system"
)

const (
	qVelScale  = float32(2000.0)
	qSizeScale = float32(500.0)
)

// baseFieldLayout is the snapshot field layout for common entity fields.
// Positions use Float32 (not quantized) — quantized positions cause visible
// artifacts at cell boundaries (~0.7 unit shift from rounding mismatch).
// Fields: relX(4), relY(4), vx(2), vy(2), rotation(2), radius(2), width(2), height(2), lockedByID(4), lockedByProgress(1)
// Total: 25 bytes, 10 fields.
var baseFieldLayout = []int{4, 4, 2, 2, 2, 2, 2, 2, 4, 1}

// snapshotBaseFields writes base fields into the snapshot writer.
// Fields must match baseFieldLayout order.
func snapshotBaseFields(w *quantize.SnapshotWriter, gw *game.GameWorld, ctx *gameNetContext, viewer *system.ViewerInfo, entry spatial.Entry) {
	relX, relY := system.CellRelativePos(gw.C.CellCoord, viewer.Entity, entry)

	// Cell-relative positions as Float32 — no quantization loss, no cell-boundary
	// shift artifacts, deterministic for stationary entities.
	w.Float32(relX)
	w.Float32(relY)

	var vx, vy float32
	if gw.C.Velocity.HasAll(entry.Entity) {
		vel := gw.C.Velocity.Get(entry.Entity)
		vx = vel.X
		vy = vel.Y
	}
	w.QVel(vx, qVelScale)
	w.QVel(vy, qVelScale)

	var rotation float32
	if gw.C.Rotation.HasAll(entry.Entity) {
		rotation = gw.C.Rotation.Get(entry.Entity).Angle
	}
	w.QAngle(rotation)

	w.QVel(entry.Radius, qSizeScale)
	w.QVel(entry.Width, qSizeScale)
	w.QVel(entry.Height, qSizeScale)

	if lb, ok := ctx.lockedBy[entry.Entity]; ok {
		w.Uint32(lb.netID)
		w.QNorm(lb.progress)
	} else {
		w.Uint32(0)
		w.QNorm(0)
	}
}

// hashCombat hashes health, shield, and status effect types+count into the hasher.
func hashCombat(h *mmokit.Hasher, gw *game.GameWorld, entity ecs.Entity) {
	if gw.C.Health.HasAll(entity) {
		hp := gw.C.Health.Get(entity)
		h.Float32(hp.Current)
		h.Float32(hp.Max)
	}
	if gw.C.Shield.HasAll(entity) {
		sh := gw.C.Shield.Get(entity)
		h.Float32(sh.Current)
		h.Float32(sh.Max)
	}
	if gw.C.StatusEffects.HasAll(entity) {
		se := gw.C.StatusEffects.Get(entity)
		h.Uint8(se.Count)
		for i := uint8(0); i < se.Count; i++ {
			h.Uint8(uint8(se.Effects[i].Type))
		}
	}
}

// snapshotCombat writes health and shield as raw Uint16 values.
// Returns 8 bytes: healthCur(2) + healthMax(2) + shieldCur(2) + shieldMax(2).
func snapshotCombat(w *quantize.SnapshotWriter, gw *game.GameWorld, entity ecs.Entity) {
	var healthCur, healthMax, shieldCur, shieldMax uint16
	if gw.C.Health.HasAll(entity) {
		hp := gw.C.Health.Get(entity)
		healthCur = uint16(hp.Current)
		healthMax = uint16(hp.Max)
	}
	if gw.C.Shield.HasAll(entity) {
		sh := gw.C.Shield.Get(entity)
		shieldCur = uint16(sh.Current)
		shieldMax = uint16(sh.Max)
	}
	w.Uint16(healthCur)
	w.Uint16(healthMax)
	w.Uint16(shieldCur)
	w.Uint16(shieldMax)
}

// encodeLengthPrefixedString returns a length-prefixed UTF-8 string as bytes.
// Format: uint16 length (big-endian) + UTF-8 bytes.
func encodeLengthPrefixedString(s string) []byte {
	if s == "" {
		buf := make([]byte, 2)
		return buf
	}
	buf := make([]byte, 2+len(s))
	binary.BigEndian.PutUint16(buf, uint16(len(s)))
	copy(buf[2:], s)
	return buf
}

