package universe

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// componentAttachHandlers caches per-reflect.Type closures that attach a
// component value to an entity. Populated lazily on cache miss; once warm,
// every spawn hits the indirect-call fast path.
var componentAttachHandlers sync.Map // map[reflect.Type]attachFn

// attachFn copies the component value v onto entity in stage's ECS world.
type attachFn func(stage *Stage, entity ecs.Entity, v any)

// Spawn creates an entity carrying the given components. The framework
// walks the variadic args, dispatches each by Go type, and attaches it
// to the new entity. Components must be passed by VALUE (not pointer).
//
// Position must be present — Spawn panics if not. The same component type
// passed twice is a programmer error; Spawn panics. Order of args has no
// semantic effect.
//
// Perf trade-off: each component is attached via a separate world.Unsafe().Add()
// call, causing one archetype migration per component. Typical 8-component
// spawns pay 8× the archetype-move cost vs the legacy Map6.NewEntity path.
// Acceptable at the spawn rates this game runs at (~100/s × 24µs ≈ 0.002% CPU
// per the spec's perf analysis); revisit before Phase 4 deletes the Map6 path
// if profiling shows otherwise. See
// docs/superpowers/specs/2026-05-13-entity-spawn-api-design.md step 6 +
// perf-analysis section.
// TODO(spawn-api-phase-4): if profiling regresses, replace the per-component
// u.Add() walk with a bulk-archetype API (ark exposes Unsafe().AddBatch).
//
// Returns the rich Entity wrapper, not the raw ecs.Entity handle. Returns
// the zero-value Entity when strictNetIDIndex is true and the netID collides
// with an existing slot (matches Stage.SpawnEntity rollback semantics).
func (b *Stage) Spawn(components ...any) Entity {
	var (
		pos         component.Position
		hasPos      bool
		kind        component.EntityKind
		hasKind     bool
		hasCollider bool
	)
	seen := make(map[reflect.Type]struct{}, len(components))
	for _, c := range components {
		t := reflect.TypeOf(c)
		if _, dup := seen[t]; dup {
			panic(fmt.Sprintf("universe.Stage.Spawn: component %s passed twice", t.String()))
		}
		seen[t] = struct{}{}
		switch v := c.(type) {
		case component.Position:
			pos = v
			hasPos = true
		case component.EntityKind:
			kind = v
			hasKind = true
		case component.Collider:
			hasCollider = true
		}
	}
	if !hasPos {
		panic("universe.Stage.Spawn: Position component is required")
	}

	w := b.ECSWorld()
	entity := w.NewEntity()
	nid := b.eng.NextNetID()

	for _, c := range components {
		t := reflect.TypeOf(c)
		fn := loadOrBuildAttachFn(t)
		fn(b, entity, c)
	}

	// NetworkID + CellCoord are framework-owned, not user-supplied.
	b.netIDMap.Add(entity, &component.NetworkID{ID: nid})
	b.cellMap.Add(entity, &component.CellCoord{CellX: b.rootCell().X, CellY: b.rootCell().Y})

	if hasCollider && b.spatialGrid != nil {
		var radius float32
		if b.colliderMap.HasAll(entity) {
			radius = b.colliderMap.Get(entity).Radius
		}
		b.spatialGrid.Register(spatial.Entry{
			Entity: entity,
			X:      pos.X,
			Y:      pos.Y,
			Radius: radius,
		})
	}
	_ = kind // invariant check lands in Task 3
	_ = hasKind

	if b.netIDIdx != nil && nid != 0 {
		res := b.netIDIdx.Enter(nid, entity, PresenceLive)
		switch res.Action {
		case ActionInstalled, ActionUpdated:
			// Normal install; no rollback.
		case ActionDuplicate:
			b.eng.Log.Log(CatMeshCell,
				"[%s] duplicate live spawn blocked: netID=%d", b.cellID, nid)
			if b.strictNetIDIndex {
				b.eng.ECS.RemoveEntity(entity)
				return Entity{}
			}
		case ActionRejected:
			// Local live spawns shouldn't conflict with existing Replica
			// under normal operation; if they do, strict mode rolls back.
			// The sanctioned Replica→Live path is PromoteReplicaToLive.
			if b.strictNetIDIndex && b.eng.ECS.Alive(entity) {
				b.eng.ECS.RemoveEntity(entity)
				return Entity{}
			}
		}
	}

	b.eng.Log.Log(CatMeshCell, "[%s] spawned entity netID=%d at (%.0f,%.0f)", b.cellID, nid, pos.X, pos.Y)
	return EntityFromECS(b, entity)
}

func loadOrBuildAttachFn(t reflect.Type) attachFn {
	if v, ok := componentAttachHandlers.Load(t); ok {
		return v.(attachFn)
	}
	fn := buildAttachFn(t)
	actual, _ := componentAttachHandlers.LoadOrStore(t, fn)
	return actual.(attachFn)
}

func buildAttachFn(t reflect.Type) attachFn {
	if t.Kind() == reflect.Pointer {
		panic(fmt.Sprintf("universe.Stage.Spawn: component %s must be passed by value, not pointer", t.String()))
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("universe.Stage.Spawn: component %s must be a struct, got %v", t.String(), t.Kind()))
	}
	return func(stage *Stage, e ecs.Entity, v any) {
		w := stage.ECSWorld()
		u := w.Unsafe()
		id := ecs.TypeID(w, t)
		if !u.Has(e, id) {
			u.Add(e, id)
		}
		ptr := u.Get(e, id)
		reflect.NewAt(t, unsafe.Pointer(ptr)).Elem().Set(reflect.ValueOf(v))
	}
}
