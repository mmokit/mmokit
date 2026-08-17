package system

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/coords"
	"github.com/mmokit/mmokit/pkg/quantize"
	"github.com/mmokit/mmokit/pkg/spatial"
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
	// initialHash writes the entity's initial-only fields into the hasher.
	// No-op for bindings without initial fields. Used to detect when initial
	// data changed so it can be re-sent to already-visible viewers.
	initialHash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry)
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
// The entityType is the wire constant sent to clients. If any binding is a
// VarTailProvider it must be the final binding (enforced at construction).
func AutoReplicator(entityType uint8, bindings ...ComponentBinding) EntityReplicator {
	var layout []int
	var anyInitial bool
	for i, b := range bindings {
		layout = append(layout, b.snapshotFields()...)
		if b.hasInitial() {
			anyInitial = true
		}
		if _, ok := b.(VarTailProvider); ok && i != len(bindings)-1 {
			panic("AutoReplicator: var-tail binding must be the last binding")
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
	var varTail *VarTailSchema
	for _, b := range a.bindings {
		bindings = append(bindings, b.schema())
		if vtp, ok := b.(VarTailProvider); ok {
			if varTail != nil {
				panic("autoReplicator: only one var-tail binding allowed per entity")
			}
			varTail = vtp.VarTailSchema()
		}
	}
	resolveFieldNameCollisions(bindings)
	initialData := ""
	if a.anyInitial {
		initialData = "length_prefixed_string_u8"
	}
	return EntitySchema{
		Kind:        a.entityType,
		Bindings:    bindings,
		Layout:      a.layout,
		VarTail:     varTail,
		InitialData: initialData,
	}
}

// resolveFieldNameCollisions prefixes field names with the component struct name
// when multiple bindings declare the same field name. This produces unique flat
// entity interfaces in the generated client SDK (e.g. Ship has both Health and
// Shield with Current/Max — the result is healthCurrent, healthMax,
// shieldCurrent, shieldMax). Non-colliding names are left unchanged so
// single-use fields like "name", "aoIRadius" don't get ugly prefixes.
func resolveFieldNameCollisions(bindings []BindingSchema) {
	// First pass: count field names across all bindings.
	counts := make(map[string]int)
	for _, b := range bindings {
		for _, f := range b.Fields {
			counts[f.Name]++
		}
	}
	// Second pass: for fields whose name has count > 1, prefix with struct name.
	for bi := range bindings {
		b := &bindings[bi]
		if b.StructName == "" {
			continue
		}
		prefix := lcFirst(b.StructName)
		for fi := range b.Fields {
			f := &b.Fields[fi]
			if counts[f.Name] > 1 {
				f.Name = prefix + ucFirst(f.Name)
			}
		}
	}
}

// ucFirst uppercases the first letter of s.
func ucFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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

func (a *autoReplicator) HasInitial() bool { return a.anyInitial }

func (a *autoReplicator) InitialHash(h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range a.bindings {
		b.initialHash(entry.Entity, h, viewer, entry)
	}
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

func (entryPositionBinding) hasInitial() bool                                            { return false }
func (entryPositionBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
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
// Cell size defaults to coords.CellSize.
func ViewerRelativePos(posMap *ecs.Map1[component.Position], cellCoordMap *ecs.Map1[component.CellCoord]) ComponentBinding {
	return &viewerRelativePosBinding{posMap: posMap, cellMap: cellCoordMap, cellSizeFn: func() float32 { return coords.CellSize }}
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

func (b *viewerRelativePosBinding) hasInitial() bool                                            { return false }
func (b *viewerRelativePosBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
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

func (b *qVelocityBinding) hasInitial() bool                                            { return false }
func (b *qVelocityBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
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

func (b *qAngleBinding) hasInitial() bool                                            { return false }
func (b *qAngleBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
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

func (b *qSizeBinding) snapshotFields() []int { return []int{2, 2, 2} }

func (b *qSizeBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		h.Float32(0)
		h.Float32(0)
		h.Float32(0)
		return
	}
	col := b.colliderMap.Get(entity)
	h.Float32(col.Radius)
	h.Float32(col.Width)
	h.Float32(col.Height)
}

func (b *qSizeBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.colliderMap.HasAll(entity) {
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		w.QVel(0, b.scale)
		return
	}
	col := b.colliderMap.Get(entity)
	w.QVel(col.Radius, b.scale)
	w.QVel(col.Width, b.scale)
	w.QVel(col.Height, b.scale)
}

func (b *qSizeBinding) hasInitial() bool                                            { return false }
func (b *qSizeBinding) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}
func (b *qSizeBinding) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}
func (b *qSizeBinding) schema() BindingSchema {
	return BindingSchema{
		Type: "q_size",
		Fields: []BindingSchemaField{
			{Name: "radius", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "width", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
			{Name: "height", Encoding: "qvel", Size: 2, Scale: float64(b.scale)},
		},
	}
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

func (g *bindingGroup) initialHash(entity ecs.Entity, h *Hasher, viewer *ViewerInfo, entry spatial.Entry) {
	for _, b := range g.bindings {
		b.initialHash(entity, h, viewer, entry)
	}
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

// EngineBindings returns a ComponentBinding bundling the standard engine-level
// replication fields: viewer-relative position, quantized velocity, quantized
// size. velScale and sizeScale come from the process Config.
func EngineBindings(w *ecs.World, velScale, sizeScale float32) ComponentBinding {
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

	bindings := []ComponentBinding{
		ViewerRelativePos(posMap, cellMap),
		QVelocity(velMap, velScale),
		QSize(colliderMap, sizeScale),
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

// componentReader abstracts how reflectBinding accesses a component on an
// entity. The typed Component[T] path captures *ecs.Map1[T] in the closure;
// the type-erased ComponentByID path captures (*ecs.World, ecs.ID, reflect.Type).
//
// has reports whether the entity has the component.
// readValue returns a reflect.Value whose Kind is reflect.Struct (the underlying
// component value) so that the shared snapshot/hash/initialData methods can
// uniformly call v.Field(i).Interface().
type componentReader struct {
	has       func(ecs.Entity) bool
	readValue func(ecs.Entity) reflect.Value
}

// reflectBinding is a reflection-based ComponentBinding for arbitrary structs.
// All entity access is funneled through a componentReader so the same struct
// serves both the typed Component[T] path and the type-erased ComponentByID
// path. The pre-computed []taggedField list, layout, fieldNames, and structName
// are populated from the component type at construction time and consumed by
// the shared methods below — they do not depend on T.
type reflectBinding struct {
	reader     componentReader
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
	return newReflectBindingTyped(ecsMap, false)
}

// OptionalComponent returns a ComponentBinding like Component, but writes zero bytes
// when the entity does not have the component.
func OptionalComponent[T any](ecsMap *ecs.Map1[T]) ComponentBinding {
	return newReflectBindingTyped(ecsMap, true)
}

// ComponentByID is the type-erased counterpart to Component[T]. It builds the
// same reflection-based ComponentBinding driven by `net:"..."` struct tags,
// but accesses the component via the world's Unsafe handle instead of a typed
// *ecs.Map1[T]. Used by the bundle-reflection walker in mmokit.RegisterKind[T]
// where the component type is only known via reflect.Type.
//
// Wire bytes are byte-identical to Component[T] — the same taggedField encoders
// drive the snapshot/hash/initial writers; only the entity-access path differs.
func ComponentByID(w *ecs.World, id ecs.ID, t reflect.Type) ComponentBinding {
	return newReflectBindingByID(w, id, t, false)
}

// OptionalComponentByID is the type-erased counterpart to OptionalComponent[T].
func OptionalComponentByID(w *ecs.World, id ecs.ID, t reflect.Type) ComponentBinding {
	return newReflectBindingByID(w, id, t, true)
}

// buildTaggedFields parses `net:"..."` struct tags on t and populates rb's
// snapshot/initial field lists, wire layout, and field names. Panics with a
// descriptive message if any tag is invalid.
func buildTaggedFields(rb *reflectBinding, t reflect.Type) {
	rb.structName = t.Name()
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
}

// newReflectBindingTyped constructs a reflectBinding whose entity access goes
// through a typed *ecs.Map1[T]. The reader closure captures the map and reads
// via the typed Map1 API, which is the historical fast path.
func newReflectBindingTyped[T any](ecsMap *ecs.Map1[T], optional bool) *reflectBinding {
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	rb := &reflectBinding{
		reader: componentReader{
			has: func(e ecs.Entity) bool { return ecsMap.HasAll(e) },
			readValue: func(e ecs.Entity) reflect.Value {
				return reflect.ValueOf(ecsMap.Get(e)).Elem()
			},
		},
		optional: optional,
	}
	buildTaggedFields(rb, t)
	return rb
}

// newReflectBindingByID constructs a reflectBinding whose entity access goes
// through World.Unsafe(). The reader closure captures (world, id, type) and
// resolves component pointers via Unsafe.Get → reflect.NewAt(t, ptr).Elem().
func newReflectBindingByID(w *ecs.World, id ecs.ID, t reflect.Type, optional bool) *reflectBinding {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("auto_replicator: ComponentByID: type %v must be a struct", t))
	}
	u := w.Unsafe()

	rb := &reflectBinding{
		reader: componentReader{
			has: func(e ecs.Entity) bool { return u.Has(e, id) },
			readValue: func(e ecs.Entity) reflect.Value {
				return reflect.NewAt(t, u.Get(e, id)).Elem()
			},
		},
		optional: optional,
	}
	buildTaggedFields(rb, t)
	return rb
}

func (rb *reflectBinding) snapshotFields() []int {
	return rb.layout
}

func (rb *reflectBinding) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !rb.reader.has(entity) {
		if rb.optional {
			rb.hashZeros(h)
			return
		}
		panic(fmt.Sprintf("auto_replicator: required component %q missing on entity %v (mark the bundle field `mmokit:\"optional\"` if absence is legitimate)", rb.structName, entity))
	}
	v := rb.reader.readValue(entity)
	for _, tf := range rb.fields {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
	for _, tf := range rb.initials {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
}

func (rb *reflectBinding) hashZeros(h *Hasher) {
	for _, tf := range rb.fields {
		tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
	}
	for _, tf := range rb.initials {
		tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
	}
}

func (rb *reflectBinding) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !rb.reader.has(entity) {
		if rb.optional {
			rb.snapshotZeros(w)
			return
		}
		panic(fmt.Sprintf("auto_replicator: required component %q missing on entity %v (mark the bundle field `mmokit:\"optional\"` if absence is legitimate)", rb.structName, entity))
	}
	v := rb.reader.readValue(entity)
	for _, tf := range rb.fields {
		tf.snapFn(v.Field(tf.index).Interface(), w)
	}
}

func (rb *reflectBinding) snapshotZeros(w *quantize.SnapshotWriter) {
	for _, tf := range rb.fields {
		tf.snapFn(zeroForEncoding(tf.meta.encoding), w)
	}
}

func (rb *reflectBinding) hasInitial() bool {
	return len(rb.initials) > 0
}

func (rb *reflectBinding) initialData(entity ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	if !rb.reader.has(entity) {
		if rb.optional {
			// Write zero initial data.
			for _, tf := range rb.initials {
				buf = tf.initFn(zeroForEncoding(tf.meta.encoding), buf)
			}
			return buf
		}
		panic(fmt.Sprintf("auto_replicator: required component %q missing on entity %v (mark the bundle field `mmokit:\"optional\"` if absence is legitimate)", rb.structName, entity))
	}
	v := rb.reader.readValue(entity)
	for _, tf := range rb.initials {
		buf = tf.initFn(v.Field(tf.index).Interface(), buf)
	}
	return buf
}

// initialHash hashes only the initial-only fields. Lenient on a missing
// component (the combined Hash/Snapshot paths enforce required presence and
// panic downstream); a binding with no initial fields is a no-op.
func (rb *reflectBinding) initialHash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if len(rb.initials) == 0 {
		return
	}
	if !rb.reader.has(entity) {
		if rb.optional {
			for _, tf := range rb.initials {
				tf.hashFn(zeroForEncoding(tf.meta.encoding), h)
			}
		}
		return
	}
	v := rb.reader.readValue(entity)
	for _, tf := range rb.initials {
		tf.hashFn(v.Field(tf.index).Interface(), h)
	}
}

func (rb *reflectBinding) schema() BindingSchema {
	bs := BindingSchema{Type: "component", StructName: rb.structName}
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
