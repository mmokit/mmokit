package system

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
)

// The engine-level replication bindings of the 3D profile.
//
// Every one of them is a sibling of a 2D binding rather than a replacement:
// the 2D set stays byte-for-byte frozen, because a 2D game must not pay a wire
// byte for a feature it does not use. The two sets are selected between at one
// call site — BuildReplicators — and never both installed.

// OrientationBinding marks a binding that emits entity orientation.
//
// It exists so BuildReplicators can refuse the one composition that silently
// produces garbage: a 3D profile whose engine set already emits orientation,
// plus a game that also attaches its own QAngle per entity kind — which is
// exactly what examples/space does today and what any game ported from the 2D
// profile would keep doing. The result would be two orientation fields in the
// snapshot, the second of which no generated client reads.
type OrientationBinding interface {
	ComponentBinding
	orientationBinding()
}

// ProvidesOrientation reports whether b, or any binding it groups, emits
// orientation.
func ProvidesOrientation(b ComponentBinding) bool {
	if _, ok := b.(OrientationBinding); ok {
		return true
	}
	if g, ok := b.(*bindingGroup); ok {
		for _, inner := range g.bindings {
			if ProvidesOrientation(inner) {
				return true
			}
		}
	}
	return false
}

func (b *qAngleBinding) orientationBinding() {}
func (b *qQuatBinding) orientationBinding()  {}

// ---------------------------------------------------------------------------
// viewerRelativePos3Binding — world-absolute position including Z
// ---------------------------------------------------------------------------

type viewerRelativePos3Binding struct {
	posMap   *ecs.Map1[component.Position]
	cellMap  *ecs.Map1[component.CellCoord]
	cellSize float32
}

// ViewerRelativePos3 is ViewerRelativePos plus a third world coordinate.
//
// Z carries NO cell term, and that is not an omission. Partitioning is
// horizontal-only by project decision (§7.4), so there is no vertical cell
// boundary and pos.Z is already world-absolute. Adding a cellZ term would
// require a third grid axis that does not exist.
func ViewerRelativePos3(posMap *ecs.Map1[component.Position], cellCoordMap *ecs.Map1[component.CellCoord], cellSize float32) ComponentBinding {
	return &viewerRelativePos3Binding{posMap: posMap, cellMap: cellCoordMap, cellSize: cellSize}
}

func (b *viewerRelativePos3Binding) snapshotFields() []int { return []int{4, 4, 4} }

func (b *viewerRelativePos3Binding) worldPos(entity ecs.Entity) (float32, float32, float32) {
	if !b.posMap.HasAll(entity) || !b.cellMap.HasAll(entity) {
		return 0, 0, 0
	}
	pos := b.posMap.Get(entity)
	cell := b.cellMap.Get(entity)
	cs := b.cellSize
	return pos.X + float32(cell.CellX)*cs, pos.Y + float32(cell.CellY)*cs, pos.Z
}

func (b *viewerRelativePos3Binding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	wx, wy, wz := b.worldPos(entity)
	h.Float32(wx)
	h.Float32(wy)
	h.Float32(wz)
}

func (b *viewerRelativePos3Binding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	wx, wy, wz := b.worldPos(entity)
	w.Float32(wx)
	w.Float32(wy)
	w.Float32(wz)
}

func (b *viewerRelativePos3Binding) hasInitial() bool                                            { return false }
func (b *viewerRelativePos3Binding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
func (b *viewerRelativePos3Binding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *viewerRelativePos3Binding) schema() BindingSchema {
	return BindingSchema{
		Type: "viewer_relative_pos3",
		Fields: []BindingSchemaField{
			{Name: "worldX", Encoding: "f32", Size: 4},
			{Name: "worldY", Encoding: "f32", Size: 4},
			{Name: "worldZ", Encoding: "f32", Size: 4},
		},
	}
}

// ---------------------------------------------------------------------------
// qVelocity3Binding — quantized velocity including Z
// ---------------------------------------------------------------------------

type qVelocity3Binding struct {
	velMap *ecs.Map1[component.Velocity]
	scale  float32
}

// QVelocity3 quantizes Velocity X, Y and Z as int16.
func QVelocity3(velMap *ecs.Map1[component.Velocity], scale float32) ComponentBinding {
	return &qVelocity3Binding{velMap: velMap, scale: scale}
}

func (b *qVelocity3Binding) snapshotFields() []int { return []int{2, 2, 2} }

func (b *qVelocity3Binding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.velMap.HasAll(entity) {
		h.Float32(0)
		h.Float32(0)
		h.Float32(0)
		return
	}
	vel := b.velMap.Get(entity)
	h.Float32(vel.X)
	h.Float32(vel.Y)
	h.Float32(vel.Z)
}

func (b *qVelocity3Binding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.velMap.HasAll(entity) {
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		return
	}
	vel := b.velMap.Get(entity)
	w.QVel(vel.X, b.scale)
	w.QVel(vel.Y, b.scale)
	w.QVel(vel.Z, b.scale)
}

func (b *qVelocity3Binding) hasInitial() bool                                            { return false }
func (b *qVelocity3Binding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
func (b *qVelocity3Binding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qVelocity3Binding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_velocity3",
		Fields: []BindingSchemaField{
			{Name: "velX", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "velY", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "velZ", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
		},
	}
}

// ---------------------------------------------------------------------------
// qSize3Binding — collider extents including Depth
// ---------------------------------------------------------------------------

type qSize3Binding struct {
	colliderMap *ecs.Map1[component.Collider]
	scale       float32
}

// QSize3 quantizes Collider radius, width, height and depth as int16.
//
// Depth is here rather than in QSize because adding it there is one of the
// three edits pkg/system/dimension_acceptance_test.go names as breaking phase
// 1's byte-invariance criterion.
func QSize3(colliderMap *ecs.Map1[component.Collider], scale float32) ComponentBinding {
	return &qSize3Binding{colliderMap: colliderMap, scale: scale}
}

func (b *qSize3Binding) snapshotFields() []int { return []int{2, 2, 2, 2} }

func (b *qSize3Binding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		h.Float32(0)
		h.Float32(0)
		h.Float32(0)
		h.Float32(0)
		return
	}
	col := b.colliderMap.Get(entity)
	h.Float32(col.Radius)
	h.Float32(col.Width)
	h.Float32(col.Height)
	h.Float32(col.Depth)
}

func (b *qSize3Binding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		return
	}
	col := b.colliderMap.Get(entity)
	w.QVel(col.Radius, b.scale)
	w.QVel(col.Width, b.scale)
	w.QVel(col.Height, b.scale)
	w.QVel(col.Depth, b.scale)
}

func (b *qSize3Binding) hasInitial() bool                                            { return false }
func (b *qSize3Binding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
func (b *qSize3Binding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qSize3Binding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_size3",
		Fields: []BindingSchemaField{
			{Name: "radius", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "width", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "height", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "depth", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
		},
	}
}

// ---------------------------------------------------------------------------
// qQuatBinding — full orientation as a smallest-three quaternion
// ---------------------------------------------------------------------------

type qQuatBinding struct {
	rotMap *ecs.Map1[component.Rotation]
}

// QQuat returns a binding that emits full orientation as a 7-byte
// smallest-three quaternion. The 3D counterpart of QAngle, which carries yaw
// only and would silently discard pitch and roll.
func QQuat(rotMap *ecs.Map1[component.Rotation]) ComponentBinding {
	return &qQuatBinding{rotMap: rotMap}
}

// snapshotFields reports ONE field, not three. That matters twice: a
// quaternion's components change together, so splitting them would spend three
// delta bitmask bits to say what one says; and a partial update is meaningless
// for an encoding whose components are jointly constrained.
func (b *qQuatBinding) snapshotFields() []int { return []int{quantize.QuatWireSize} }

func (b *qQuatBinding) quantized(entity ecs.Entity) uint64 {
	if !b.rotMap.HasAll(entity) {
		return quantize.Quat(0, 0, 0, 1) // identity
	}
	rot := b.rotMap.Get(entity)
	return quantize.Quat(rot.X, rot.Y, rot.Z, rot.W)
}

// hash hashes the QUANTIZED value rather than the raw floats. Orientation
// jitter below one quantum is invisible on the wire, so hashing the floats
// would mark the entity dirty for a snapshot that encodes identical bytes.
func (b *qQuatBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	v := b.quantized(entity)
	h.Uint32(uint32(v >> 32))
	h.Uint32(uint32(v))
}

func (b *qQuatBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.rotMap.HasAll(entity) {
		w.QQuat(0, 0, 0, 1)
		return
	}
	rot := b.rotMap.Get(entity)
	w.QQuat(rot.X, rot.Y, rot.Z, rot.W)
}

func (b *qQuatBinding) hasInitial() bool                                            { return false }
func (b *qQuatBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
func (b *qQuatBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qQuatBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_quat",
		Fields: []BindingSchemaField{
			{Name: "rot", Encoding: "qquat", Size: quantize.QuatWireSize},
		},
	}
}

// ---------------------------------------------------------------------------
// EngineBindings3D
// ---------------------------------------------------------------------------

// EngineBindings3D is the 3D profile's engine-level binding set: world
// position including Z, velocity including Z, collider extents including
// Depth, and full orientation.
//
// Orientation is in the ENGINE set here and deliberately not in the 2D one.
// The 2D profile leaves it to the game, which is what keeps its layout frozen;
// the 3D profile owns it, because a game that forgot to attach it would
// replicate entities with no orientation at all and nothing would report it.
func EngineBindings3D(w *ecs.World, velScale, sizeScale, cellSize float32) ComponentBinding {
	if velScale == 0 {
		velScale = 100
	}
	if sizeScale == 0 {
		sizeScale = 100
	}
	posMap := ecs.NewMap1[component.Position](w)
	cellMap := ecs.NewMap1[component.CellCoord](w)
	velMap := ecs.NewMap1[component.Velocity](w)
	colliderMap := ecs.NewMap1[component.Collider](w)
	rotMap := ecs.NewMap1[component.Rotation](w)

	return &bindingGroup{bindings: []ComponentBinding{
		ViewerRelativePos3(posMap, cellMap, cellSize),
		QVelocity3(velMap, velScale),
		QSize3(colliderMap, sizeScale),
		QQuat(rotMap),
	}}
}
