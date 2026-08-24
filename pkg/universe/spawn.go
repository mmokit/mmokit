package universe

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit/pkg/component"
	"github.com/mmokit/mmokit/pkg/engine"
	"github.com/mmokit/mmokit/pkg/spatial"
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
// with an existing slot — the spawn is rolled back from the ECS world.
func (b *Stage) Spawn(components ...any) Entity {
	var (
		pos         component.Position
		hasPos      bool
		kind        component.EntityKind
		hasKind     bool
		col         component.Collider
		hasCollider bool
		rot         component.Rotation
		hasRot      bool
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
			// Rejected here rather than clamped, because this is where a game
			// author gets a stack trace pointing at their own Spawn call.
			// Shape indexes pkg/spatial's dispatch tables; an out-of-range
			// value would panic inside a bucket scan on the cell loop, with
			// nothing in the trace naming the entity that caused it.
			if !v.Shape.Valid() {
				panic(fmt.Sprintf("universe.Stage.Spawn: Collider.Shape %d is not a shape this build implements "+
					"(valid: 0..%d)", uint8(v.Shape), component.ShapeCount-1))
			}
			col = v
			hasCollider = true
		case component.Rotation:
			rot = v
			hasRot = true
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

	// Velocity defaults to zero when not supplied. Several systems (physics,
	// AI, replication) Query on *Velocity and silently exclude entities
	// missing it — legacy SpawnEntity always auto-attached zero Velocity.
	if _, gotVel := seen[reflect.TypeFor[component.Velocity]()]; !gotVel {
		b.velMap.Add(entity, &component.Velocity{})
	}

	if hasCollider && b.spatialGrid != nil {
		// Through EntryFrom, so this entry is identical to the one
		// SpatialSystem writes a tick later. It used to carry Entity, X, Y
		// and Radius alone, which left Layer at zero — and a zero layer is
		// invisible to every layer-masked query, so a wall spawned here
		// blocked nothing until the next tick, and nothing ever in a process
		// with no SpatialSystem.
		var rotPtr *component.Rotation
		if hasRot {
			rotPtr = &rot
		}
		b.spatialGrid.Register(spatial.EntryFrom(entity, &pos, &col, rotPtr))
	}
	if hasKind && b.coord != nil && b.coord.invariantMode == InvariantPanic {
		if def, ok := b.entityKinds[kind.Type]; ok {
			if missing := findMissingRequiredComponents(b.ECSWorld(), entity, def, seen); len(missing) > 0 {
				panic(fmt.Sprintf("universe.Stage.Spawn: kind %q (%d) missing required components: %v",
					def.Name, def.Kind, missing))
			}
		}
	}

	if b.netIDIdx != nil && nid != 0 {
		res := b.netIDIdx.Enter(nid, entity, PresenceLive)
		switch res.Action {
		case ActionInstalled, ActionUpdated:
			// Normal install; no rollback.
		case ActionDuplicate:
			b.eng.Log.Log(CatMeshCell,
				"[%s] duplicate live spawn blocked: netID=%d", b.cellID, nid)
			if b.strictNetIDIndex {
				b.eng.RemoveEntityNowWithPolicy(entity, engine.RemovalLocalOnly)
				return Entity{}
			}
		case ActionRejected:
			// Local live spawns shouldn't conflict with existing Replica
			// under normal operation; if they do, strict mode rolls back.
			// The sanctioned Replica→Live path is PromoteReplicaToLive.
			if b.strictNetIDIndex && b.eng.ECS.Alive(entity) {
				b.eng.RemoveEntityNowWithPolicy(entity, engine.RemovalLocalOnly)
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

// findMissingRequiredComponents returns the names of Bundle fields registered
// on the kind that are not present in `seen` (the set of component types
// passed to Spawn). Skips fields tagged mmokit:"local" — those are
// transfer-local and not user-required (KindComponentByID excludes them
// from requiredTypes at registration time).
func findMissingRequiredComponents(
	w *ecs.World, entity ecs.Entity, def *EntityKindDef, seen map[reflect.Type]struct{},
) []string {
	var missing []string
	u := w.Unsafe()
	for _, c := range def.requiredTypes {
		if _, ok := seen[c]; ok {
			continue
		}
		id := ecs.TypeID(w, c)
		if u.Has(entity, id) {
			continue
		}
		missing = append(missing, c.Name())
	}
	return missing
}
