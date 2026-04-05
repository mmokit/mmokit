package system

import (
	"fmt"
	"reflect"

	"github.com/mlange-42/ark/ecs"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/quantize"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// ---------------------------------------------------------------------------
// ComponentBinding — composable building block for AutoReplicator
// ---------------------------------------------------------------------------

// ComponentBinding defines how one ECS component participates in replication.
// Built-in bindings cover common patterns (position, velocity, rotation, size).
// Use Component[T] / OptionalComponent[T] for struct-tag-driven reflection bindings.
type ComponentBinding interface {
	snapshotFields() []int // wire sizes of per-tick fields
	hash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
	snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry)
	hasInitial() bool
	initialData(entity ecs.Entity, viewer *ViewerInfo, entry spatial.Entry, buf []byte) []byte
	schema() BindingSchema // machine-readable description for client codegen
}

// ---------------------------------------------------------------------------
// autoReplicator — implements EntityReplicator from composed bindings
// ---------------------------------------------------------------------------

type autoReplicator struct {
	entityType uint8
	bindings   []ComponentBinding
	layout     []int // cached SnapshotLayout
	anyInitial bool  // true if any binding has initial data
}

// AutoReplicator builds an EntityReplicator from composable ComponentBinding values.
// The entityType is the wire constant sent to clients.
func AutoReplicator(entityType uint8, bindings ...ComponentBinding) EntityReplicator {
	var layout []int
	var anyInitial bool
	for _, b := range bindings {
		layout = append(layout, b.snapshotFields()...)
		if b.hasInitial() {
			anyInitial = true
		}
	}
	return &autoReplicator{
		entityType: entityType,
		bindings:   bindings,
		layout:     layout,
		anyInitial: anyInitial,
	}
}

func (a *autoReplicator) EntityType() uint8 { return a.entityType }

// Schema implements SchemaProvider for client SDK codegen.
func (a *autoReplicator) Schema() EntitySchema {
	var bindings []BindingSchema
	for _, b := range a.bindings {
		bindings = append(bindings, b.schema())
	}
	initialData := ""
	if a.anyInitial {
		initialData = "length_prefixed_string_u8"
	}
	return EntitySchema{
		Kind:        a.entityType,
		Bindings:    bindings,
		Layout:      a.layout,
		InitialData: initialData,
	}
}

func (a *autoReplicator) Hash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range a.bindings {
		b.hash(entry.Entity, h, viewer, entry)
	}
}

func (a *autoReplicator) Snapshot(w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range a.bindings {
		b.snapshot(entry.Entity, w, viewer, entry)
	}
}

func (a *autoReplicator) SnapshotLayout() []int {
	return a.layout
}

func (a *autoReplicator) InitialData(viewer *ViewerInfo, entry spatial.Entry) []byte {
	if !a.anyInitial {
		return nil
	}
	var buf []byte
	for _, b := range a.bindings {
		if b.hasInitial() {
			buf = b.initialData(entry.Entity, viewer, entry, buf)
		}
	}
	return buf
}

// ---------------------------------------------------------------------------
// Built-in bindings
// ---------------------------------------------------------------------------

// entryPositionBinding uses the spatial entry X/Y directly (2x float32).
type entryPositionBinding struct{}

// EntryPosition returns a binding that writes the spatial.Entry X/Y as 2x float32.
func EntryPosition() ComponentBinding {
	return entryPositionBinding{}
}

func (entryPositionBinding) snapshotFields() []int { return []int{4, 4} }

func (entryPositionBinding) hash(_ ecs.Entity, h *Hasher, _ *ViewerInfo, entry spatial.Entry) {
	h.Float32(entry.X)
	h.Float32(entry.Y)
}

func (entryPositionBinding) snapshot(_ ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, entry spatial.Entry) {
	w.Float32(entry.X)
	w.Float32(entry.Y)
}

func (entryPositionBinding) hasInitial() bool { return false }
func (entryPositionBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (entryPositionBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "entry_position",
		Fields: []BindingSchemaField{
			{Name: "x", Encoding: "f32", Size: 4},
			{Name: "y", Encoding: "f32", Size: 4},
		},
	}
}

// viewerRelativePosBinding computes world-absolute position from cell-local pos + cell offset.
type viewerRelativePosBinding struct {
	posMap     *ecs.Map1[component.Position]
	cellMap    *ecs.Map1[component.CellCoord]
	cellSizeFn func() float32
}

// ViewerRelativePos returns a binding that computes world-absolute position:
// worldX = pos.X + float32(cellCoord.CellX) * cellSize
//
// Cell size defaults to coords.CellSize. For dynamic cell partitioning where
// cell sizes change at runtime, use ViewerRelativePosWithCellSize instead.
func ViewerRelativePos(posMap *ecs.Map1[component.Position], cellCoordMap *ecs.Map1[component.CellCoord]) ComponentBinding {
	return &viewerRelativePosBinding{posMap: posMap, cellMap: cellCoordMap, cellSizeFn: func() float32 { return coords.CellSize }}
}

// ViewerRelativePosWithCellSize is like ViewerRelativePos but uses a dynamic
// cell size from the provided callback. Use when cell sizes vary at runtime
// (dynamic cell partitioning).
func ViewerRelativePosWithCellSize(posMap *ecs.Map1[component.Position], cellCoordMap *ecs.Map1[component.CellCoord], cellSizeFn func() float32) ComponentBinding {
	return &viewerRelativePosBinding{posMap: posMap, cellMap: cellCoordMap, cellSizeFn: cellSizeFn}
}

func (b *viewerRelativePosBinding) snapshotFields() []int { return []int{4, 4} }

func (b *viewerRelativePosBinding) worldPos(entity ecs.Entity) (float32, float32) {
	if !b.posMap.HasAll(entity) || !b.cellMap.HasAll(entity) {
		return 0, 0
	}
	pos := b.posMap.Get(entity)
	cell := b.cellMap.Get(entity)
	cs := b.cellSizeFn()
	worldX := pos.X + float32(cell.CellX)*cs
	worldY := pos.Y + float32(cell.CellY)*cs
	return worldX, worldY
}

func (b *viewerRelativePosBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	wx, wy := b.worldPos(entity)
	h.Float32(wx)
	h.Float32(wy)
}

func (b *viewerRelativePosBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	wx, wy := b.worldPos(entity)
	w.Float32(wx)
	w.Float32(wy)
}

func (b *viewerRelativePosBinding) hasInitial() bool { return false }
func (b *viewerRelativePosBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *viewerRelativePosBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "viewer_relative_pos",
		Fields: []BindingSchemaField{
			{Name: "worldX", Encoding: "f32", Size: 4},
			{Name: "worldY", Encoding: "f32", Size: 4},
		},
	}
}

// qVelocityBinding quantizes Velocity X/Y as 2x int16.
type qVelocityBinding struct {
	velMap *ecs.Map1[component.Velocity]
	scale  float32
}

// QVelocity returns a binding that quantizes Velocity.X and Velocity.Y as int16.
func QVelocity(velMap *ecs.Map1[component.Velocity], scale float32) ComponentBinding {
	return &qVelocityBinding{velMap: velMap, scale: scale}
}

func (b *qVelocityBinding) snapshotFields() []int { return []int{2, 2} }

func (b *qVelocityBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.velMap.HasAll(entity) {
		h.Float32(0)
		h.Float32(0)
		return
	}
	vel := b.velMap.Get(entity)
	h.Float32(vel.X)
	h.Float32(vel.Y)
}

func (b *qVelocityBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.velMap.HasAll(entity) {
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		return
	}
	vel := b.velMap.Get(entity)
	w.QVel(vel.X, b.scale)
	w.QVel(vel.Y, b.scale)
}

func (b *qVelocityBinding) hasInitial() bool { return false }
func (b *qVelocityBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qVelocityBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_velocity",
		Fields: []BindingSchemaField{
			{Name: "velX", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "velY", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
		},
	}
}

// qAngleBinding quantizes Rotation.Angle as uint16.
type qAngleBinding struct {
	rotMap *ecs.Map1[component.Rotation]
}

// QAngle returns a binding that quantizes Rotation.Angle as uint16.
func QAngle(rotMap *ecs.Map1[component.Rotation]) ComponentBinding {
	return &qAngleBinding{rotMap: rotMap}
}

func (b *qAngleBinding) snapshotFields() []int { return []int{2} }

func (b *qAngleBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.rotMap.HasAll(entity) {
		h.Float32(0)
		return
	}
	rot := b.rotMap.Get(entity)
	h.Float32(rot.Angle)
}

func (b *qAngleBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.rotMap.HasAll(entity) {
		w.QAngle(0)
		return
	}
	rot := b.rotMap.Get(entity)
	w.QAngle(rot.Angle)
}

func (b *qAngleBinding) hasInitial() bool { return false }
func (b *qAngleBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qAngleBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_angle",
		Fields: []BindingSchemaField{
			{Name: "angle", Encoding: "qangle", Size: 2},
		},
	}
}

// qSizeBinding quantizes Collider.Radius as uint16 (using QVel internally).
type qSizeBinding struct {
	colliderMap *ecs.Map1[component.Collider]
	scale       float32
}

// QSize returns a binding that quantizes Collider.Radius as uint16 using QVel.
func QSize(colliderMap *ecs.Map1[component.Collider], scale float32) ComponentBinding {
	return &qSizeBinding{colliderMap: colliderMap, scale: scale}
}

func (b *qSizeBinding) snapshotFields() []int { return []int{2} }

func (b *qSizeBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		h.Float32(0)
		return
	}
	col := b.colliderMap.Get(entity)
	h.Float32(col.Radius)
}

func (b *qSizeBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		w.QVel(0, b.scale)
		return
	}
	col := b.colliderMap.Get(entity)
	w.QVel(col.Radius, b.scale)
}

func (b *qSizeBinding) hasInitial() bool { return false }
func (b *qSizeBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qSizeBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_size",
		Fields: []BindingSchemaField{
			{Name: "radius", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
		},
	}
}

// meshStateBinding writes entity mesh ownership: meshState (u8) + ownerNode (u8).
// Values come from the enginepb.EntityMeshState proto enum — single source of truth.
type meshStateBinding struct {
	ghostMap   *ecs.Map1[component.Ghost]
	replicaMap *ecs.Map1[component.Replica]
	cellMap    *ecs.Map1[component.CellCoord]
	gridWidth  uint32
}

// MeshState returns a binding that writes 2 bytes per entity:
//   - meshState (u8): EMS_LOCAL (0), EMS_REPLICA (1), or EMS_GHOST (2)
//   - ownerNode (u8): flat index (cellY * gridWidth + cellX) of the authoritative node
//
// The enum values are defined in proto/enginepb/engine.proto (EntityMeshState).
func MeshState(
	ghostMap *ecs.Map1[component.Ghost],
	replicaMap *ecs.Map1[component.Replica],
	cellMap *ecs.Map1[component.CellCoord],
	gridWidth uint32,
) ComponentBinding {
	return &meshStateBinding{
		ghostMap:   ghostMap,
		replicaMap: replicaMap,
		cellMap:    cellMap,
		gridWidth:  gridWidth,
	}
}

func (b *meshStateBinding) snapshotFields() []int { return []int{1, 1} }

func (b *meshStateBinding) resolve(entity ecs.Entity) (uint8, uint8) {
	if b.ghostMap.HasAll(entity) {
		return uint8(enginepb.EntityMeshState_EMS_GHOST),
			parseNodeIndex(b.ghostMap.Get(entity).DestNodeID, b.gridWidth)
	}
	if b.replicaMap.HasAll(entity) {
		return uint8(enginepb.EntityMeshState_EMS_REPLICA),
			parseNodeIndex(b.replicaMap.Get(entity).SourceNodeID, b.gridWidth)
	}
	var nodeIdx uint8
	if b.cellMap.HasAll(entity) {
		cc := b.cellMap.Get(entity)
		nodeIdx = uint8(uint32(cc.CellY)*b.gridWidth + uint32(cc.CellX))
	}
	return uint8(enginepb.EntityMeshState_EMS_LOCAL), nodeIdx
}

func (b *meshStateBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	state, owner := b.resolve(entity)
	h.Uint8(state)
	h.Uint8(owner)
}

func (b *meshStateBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	state, owner := b.resolve(entity)
	w.Uint8(state)
	w.Uint8(owner)
}

func (b *meshStateBinding) hasInitial() bool { return false }
func (b *meshStateBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *meshStateBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "mesh_state",
		Fields: []BindingSchemaField{
			{Name: "meshState", Encoding: "u8", Size: 1},
			{Name: "ownerNode", Encoding: "u8", Size: 1},
		},
	}
}

// parseNodeIndex extracts cell coordinates from a node ID and returns a flat index.
// Supports "node_X_Y" (depth 0) and "node_dD_X_Y" (depth > 0) formats.
func parseNodeIndex(nodeID string, gridWidth uint32) uint8 {
	var sx, sy int32
	if _, err := fmt.Sscanf(nodeID, "node_d%*d_%d_%d", &sx, &sy); err != nil {
		fmt.Sscanf(nodeID, "node_%d_%d", &sx, &sy)
	}
	return uint8(uint32(sy)*gridWidth + uint32(sx))
}

// ---------------------------------------------------------------------------
// bindingGroup — aggregates multiple bindings as one ComponentBinding
// ---------------------------------------------------------------------------

// bindingGroup implements ComponentBinding by delegating to child bindings in order.
// AutoReplicator treats it as a single binding with all children's fields combined.
type bindingGroup struct {
	bindings []ComponentBinding
}

func (g *bindingGroup) snapshotFields() []int {
	var fields []int
	for _, b := range g.bindings {
		fields = append(fields, b.snapshotFields()...)
	}
	return fields
}

func (g *bindingGroup) hash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range g.bindings {
		b.hash(entity, h, viewer, entry)
	}
}

func (g *bindingGroup) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range g.bindings {
		b.snapshot(entity, w, viewer, entry)
	}
}

func (g *bindingGroup) hasInitial() bool {
	for _, b := range g.bindings {
		if b.hasInitial() {
			return true
		}
	}
	return false
}

func (g *bindingGroup) initialData(entity ecs.Entity, viewer *ViewerInfo, entry spatial.Entry, buf []byte) []byte {
	for _, b := range g.bindings {
		buf = b.initialData(entity, viewer, entry, buf)
	}
	return buf
}

func (g *bindingGroup) schema() BindingSchema {
	bs := BindingSchema{Type: "engine_bindings"}
	for _, b := range g.bindings {
		s := b.schema()
		bs.Fields = append(bs.Fields, s.Fields...)
	}
	return bs
}

// ---------------------------------------------------------------------------
// EngineBindings — standard replication bindings for meshed entities
// ---------------------------------------------------------------------------

// EngineBindingsConfig configures the standard engine-level replication bindings.
type EngineBindingsConfig struct {
	// GridWidth is the mesh grid width for MeshState owner index computation.
	// Only used when IncludeMeshState is true.
	GridWidth uint32

	// VelQuantScale is the velocity quantization multiplier: int16 = vel * scale.
	// Higher values give more precision but lower max speed (32767 / scale).
	// Zero defaults to 100 (max ~327 units/s, precision 0.01).
	VelQuantScale float32

	// SizeQuantScale is the radius quantization multiplier: int16 = radius * scale.
	// Zero defaults to 100 (max ~327 units, precision 0.01).
	SizeQuantScale float32

	// CellSizeFn returns the current cell size. Nil defaults to coords.CellSize.
	// Set this when using dynamic cell partitioning where cell sizes change at runtime.
	CellSizeFn func() float32

	// IncludeMeshState enables the MeshState binding (meshState + ownerNode bytes).
	// This exposes server topology to clients (LOCAL/REPLICA/GHOST state).
	// Disabled by default — most games should not expose mesh state to clients.
	// Enable for debug overlays or tools that need to visualize server ownership.
	IncludeMeshState bool
}

// EngineBindings returns a ComponentBinding that bundles the standard engine-level
// replication fields: position, quantized velocity, quantized size, and mesh state.
// These are the bindings every meshed entity needs. Games append game-specific
// Component[T] bindings after this.
func EngineBindings(w *ecs.World, cfg EngineBindingsConfig) ComponentBinding {
	if cfg.VelQuantScale == 0 {
		cfg.VelQuantScale = 100
	}
	if cfg.SizeQuantScale == 0 {
		cfg.SizeQuantScale = 100
	}

	posMap := ecs.NewMap1[component.Position](w)
	cellMap := ecs.NewMap1[component.CellCoord](w)
	velMap := ecs.NewMap1[component.Velocity](w)
	colliderMap := ecs.NewMap1[component.Collider](w)
	ghostMap := ecs.NewMap1[component.Ghost](w)
	replicaMap := ecs.NewMap1[component.Replica](w)

	var posBinding ComponentBinding
	if cfg.CellSizeFn != nil {
		posBinding = ViewerRelativePosWithCellSize(posMap, cellMap, cfg.CellSizeFn)
	} else {
		posBinding = ViewerRelativePos(posMap, cellMap)
	}

	bindings := []ComponentBinding{
		posBinding,
		QVelocity(velMap, cfg.VelQuantScale),
		QSize(colliderMap, cfg.SizeQuantScale),
	}
	if cfg.IncludeMeshState {
		bindings = append(bindings, MeshState(ghostMap, replicaMap, cellMap, cfg.GridWidth))
	}
	return &bindingGroup{bindings: bindings}
}

// ---------------------------------------------------------------------------
// Reflection-based Component[T] / OptionalComponent[T]
// ---------------------------------------------------------------------------

// taggedField holds pre-parsed per-field closures built from struct tags.
type taggedField struct {
	index    int // struct field index
	meta     fieldMeta
	hashFn   hashWriterFunc
	snapFn   snapshotWriterFunc
	initFn   initialWriterFunc // nil if not initial
	wireSize int
}

// reflectBinding is a reflection-based ComponentBinding for arbitrary structs.
type reflectBinding[T any] struct {
	ecsMap     *ecs.Map1[T]
	fields     []taggedField // per-tick snapshot fields
	initials   []taggedField // initial-only fields
	layout     []int         // wire sizes for snapshot fields
	optional   bool          // write zeros if component absent (vs panic)
	structName string        // Go struct name (e.g. "DebugInfo"), for schema export
	fieldNames []string      // Go field names in order (snapshot then initial), lowercased
}

// Component returns a ComponentBinding that uses reflection to read `net:"..."` tags
// on the struct T. Panics at construction if tags are invalid, and at runtime if the
// entity is missing the component (programming error).
func Component[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return newReflectBinding(ecsMap, false)
}

// OptionalComponent returns a ComponentBinding like Component, but writes zero bytes
// when the entity does not have the component.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return newReflectBinding(ecsMap, true)
}

func newReflectBinding[T any](ecsMap *ecs.Map1[T], optional bool) *reflectBinding[T] {
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	rb := &reflectBinding[T]{
		ecsMap:     ecsMap,
		optional:   optional,
		structName: t.Name(),
	}

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("net")
		if tag == "" {
			continue
		}
		fm, err := parseNetTag(tag, f.Type.Kind())
		if err != nil {
			panic(fmt.Sprintf("auto_replicator: %s.%s: %v", t.Name(), f.Name, err))
		}

		tf := taggedField{
			index:    i,
			meta:     fm,
			wireSize: fm.wireSize,
		}

		// Build hash writer (needed for both initial and snapshot fields).
		hw, err := hashWriterFor(fm)
		if err != nil {
			panic(fmt.Sprintf("auto_replicator: %s.%s: %v", t.Name(), f.Name, err))
		}
		tf.hashFn = hw

		if fm.initial {
			iw, err := initialWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: %s.%s: %v", t.Name(), f.Name, err))
			}
			tf.initFn = iw
			rb.initials = append(rb.initials, tf)
		} else {
			sw, err := snapshotWriterFor(fm)
			if err != nil {
				panic(fmt.Sprintf("auto_replicator: %s.%s: %v", t.Name(), f.Name, err))
			}
			tf.snapFn = sw
			rb.fields = append(rb.fields, tf)
			rb.layout = append(rb.layout, fm.wireSize)
		}

		rb.fieldNames = append(rb.fieldNames, lcFirst(f.Name))
	}

	return rb
}

func (rb *reflectBinding[T]) snapshotFields() []int {
	return rb.layout
}

func (rb *reflectBinding[T]) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !rb.ecsMap.HasAll(entity) {
		if rb.optional {
			rb.hashZeros(h)
			return
		}
		panic("auto_replicator: required component missing on entity")
	}
	comp := rb.ecsMap.Get(entity)
	v := reflect.ValueOf(comp).Elem()
	for _, tf := range rb.fields {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
	for _, tf := range rb.initials {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
}

func (rb *reflectBinding[T]) hashZeros(h *Hasher) {
	for _, tf := range rb.fields {
		tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
	}
	for _, tf := range rb.initials {
		tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
	}
}

func (rb *reflectBinding[T]) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !rb.ecsMap.HasAll(entity) {
		if rb.optional {
			rb.snapshotZeros(w)
			return
		}
		panic("auto_replicator: required component missing on entity")
	}
	comp := rb.ecsMap.Get(entity)
	v := reflect.ValueOf(comp).Elem()
	for _, tf := range rb.fields {
		tf.snapFn(v.Field(tf.index).Interface(), w)
	}
}

func (rb *reflectBinding[T]) snapshotZeros(w *quantize.SnapshotWriter) {
	for _, tf := range rb.fields {
		tf.snapFn(zeroForEncoding(tf.meta.encoding), w)
	}
}

func (rb *reflectBinding[T]) hasInitial() bool {
	return len(rb.initials) > 0
}

func (rb *reflectBinding[T]) initialData(entity ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	if !rb.ecsMap.HasAll(entity) {
		if rb.optional {
			// Write zero initial data.
			for _, tf := range rb.initials {
				buf = tf.initFn(zeroForEncoding(tf.meta.encoding), buf)
			}
			return buf
		}
		panic("auto_replicator: required component missing on entity")
	}
	comp := rb.ecsMap.Get(entity)
	v := reflect.ValueOf(comp).Elem()
	for _, tf := range rb.initials {
		buf = tf.initFn(v.Field(tf.index).Interface(), buf)
	}
	return buf
}

func (rb *reflectBinding[T]) schema() BindingSchema {
	bs := BindingSchema{Type: "component"}
	nameIdx := 0
	for _, tf := range rb.fields {
		name := ""
		if nameIdx < len(rb.fieldNames) {
			name = rb.fieldNames[nameIdx]
		}
		nameIdx++
		bs.Fields = append(bs.Fields, BindingSchemaField{
			Name:     name,
			Encoding: tf.meta.encoding,
			Size:     tf.wireSize,
			Scale:    scaleFromOptions(tf.meta),
		})
	}
	for _, tf := range rb.initials {
		name := ""
		if nameIdx < len(rb.fieldNames) {
			name = rb.fieldNames[nameIdx]
		}
		nameIdx++
		bs.Fields = append(bs.Fields, BindingSchemaField{
			Name:     name,
			Encoding: tf.meta.encoding,
			Size:     tf.wireSize,
			Scale:    scaleFromOptions(tf.meta),
			Initial:  true,
		})
	}
	return bs
}

// scaleFromOptions extracts the "scale" option from fieldMeta, if present.
func scaleFromOptions(fm fieldMeta) float64 {
	if s, ok := fm.options["scale"]; ok {
		var f float64
		if _, err := fmt.Sscan(s, &f); err == nil {
			return f
		}
	}
	return 0
}

// zeroForEncoding returns the zero value suitable for a given encoding's writer.
func zeroForEncoding(enc string) any {
	switch enc {
	case "f32", "pos", "qvel", "qangle", "qsize", "qnorm":
		return float32(0)
	case "u8":
		return uint8(0)
	case "u16":
		return uint16(0)
	case "u32":
		return uint32(0)
	case "i16":
		return int16(0)
	case "bool":
		return false
	case "string":
		return ""
	default:
		return float32(0)
	}
}
