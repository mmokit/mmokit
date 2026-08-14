package system

import (
	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/quantize"
	"github.com/zenion/mmokit/pkg/spatial"
)

// VarTailProvider is an optional interface on ComponentBinding. autoReplicator
// detects implementers and collects their VarTailSchema into the EntitySchema
// for client codegen. Only one var-tail binding is allowed per entity.
type VarTailProvider interface {
	VarTailSchema() *VarTailSchema
}

// VarTailAccessor describes how a component exposes its variable-length tail
// to a VarTailComponent binding. The caller provides closures that read the
// count, serialize all items to a SnapshotWriter, and hash all items to a
// Hasher.
//
// WriteItems must write exactly Count * ItemSize bytes. The binding writes
// a uint16 BE byte-length prefix before invoking WriteItems.
//
// HashItems must produce a hash equivalent to the byte sequence that
// WriteItems emits, in the same deterministic order. Divergence between
// HashItems and WriteItems causes silent missed updates: the hash detects
// no change, so no delta is produced, but the wire bytes are stale.
type VarTailAccessor[T any] struct {
	Name       string               // field name on the generated entity type
	ItemSize   int                  // bytes per item
	ItemFields []BindingSchemaField // per-item sub-fields for sdkgen
	Count      func(comp *T) int
	WriteItems func(comp *T, w *quantize.SnapshotWriter)
	HashItems  func(comp *T, h *Hasher)
}

// VarTailComponent returns a ComponentBinding that emits a variable-length
// tail from an ECS component. The tail wire format is:
//
//	[uint16 BE byte length][count * itemSize bytes]
//
// The binding advertises a layout of []int{-1} so DeltaEncoder treats it as
// the single var-tail field. Because of that, a VarTailComponent binding MUST
// be the last binding in an AutoReplicator's binding list. BuildReplicators in
// pkg/mmokit auto-hoists var-tail bindings to the end so games don't need to
// worry about ordering manually.
func VarTailComponent[T any](ecsMap *ecs.Map1[T], acc VarTailAccessor[T]) ComponentBinding {
	if acc.Count == nil || acc.WriteItems == nil || acc.HashItems == nil {
		panic("VarTailComponent: Count/WriteItems/HashItems must all be set")
	}
	if acc.ItemSize <= 0 {
		panic("VarTailComponent: ItemSize must be positive")
	}
	return &varTailBinding[T]{ecsMap: ecsMap, acc: acc}
}

type varTailBinding[T any] struct {
	ecsMap *ecs.Map1[T]
	acc    VarTailAccessor[T]
}

func (b *varTailBinding[T]) snapshotFields() []int { return []int{-1} }

func (b *varTailBinding[T]) hash(entity ecs.Entity, h *Hasher, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		h.Uint32(0) // hash zero count when component absent
		return
	}
	comp := b.ecsMap.Get(entity)
	h.Uint32(uint32(b.acc.Count(comp)))
	b.acc.HashItems(comp, h)
}

func (b *varTailBinding[T]) snapshot(entity ecs.Entity, w *quantize.SnapshotWriter, _ *ViewerInfo, _ spatial.Entry) {
	if !b.ecsMap.HasAll(entity) {
		w.Uint16(0)
		return
	}
	comp := b.ecsMap.Get(entity)
	count := b.acc.Count(comp)
	byteLen := uint16(count * b.acc.ItemSize)
	w.Uint16(byteLen)
	b.acc.WriteItems(comp, w)
}

func (b *varTailBinding[T]) hasInitial() bool { return false }

func (b *varTailBinding[T]) initialData(_ ecs.Entity, _ *ViewerInfo, _ spatial.Entry, buf []byte) []byte {
	return buf
}

func (b *varTailBinding[T]) initialHash(ecs.Entity, *Hasher, *ViewerInfo, spatial.Entry) {}

func (b *varTailBinding[T]) schema() BindingSchema {
	// The BindingSchema describes scalar per-tick wire fields. A var-tail
	// binding has none; its variable tail is surfaced via VarTailProvider so
	// autoReplicator can attach it to EntitySchema.VarTail.
	return BindingSchema{Type: "var_tail"}
}

func (b *varTailBinding[T]) VarTailSchema() *VarTailSchema {
	return &VarTailSchema{
		Name:       b.acc.Name,
		ItemSize:   b.acc.ItemSize,
		ItemFields: b.acc.ItemFields,
	}
}
