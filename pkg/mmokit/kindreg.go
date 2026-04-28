// Package-internal kind registration. Game code uses mmokit.RegisterKind[T]
// to declare entity kinds via a typed component-bundle struct. Reflection
// enumerates the bundle's *ComponentType fields and registers each as a
// KindComponent on every cell's Stage via ark's type-erased TypeID +
// Unsafe.Add primitives.
//
// Pattern mirrors pkg/query/query.go: a generic bundle struct serves as
// both the kind spec (here) and the query iterator (there).

package mmokit

import (
	"fmt"
	"reflect"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/system"
	"github.com/zenion/mmoserver/pkg/universe"
)

// FieldOption is the unified per-component option type. Aliased to
// universe.ComponentOption so the same value flows through both the
// bundle path (RegisterKind[T] + WithField[T]) and the explicit-call
// path (KindComponent[T]) without conversion.
type FieldOption = universe.ComponentOption

// WithBinding overrides the AutoReplicator binding for a bundle field
// with a caller-supplied ComponentBinding (typically a custom var-tail
// binding like NewStatusEffectsBinding or NewInventoryBinding). Only
// meaningful inside WithField[T] — the explicit-call path uses
// KindComponentWithBinding[T] which takes the binding as a positional
// argument.
func WithBinding(b system.ComponentBinding) FieldOption {
	return universe.ComponentOption{Apply: func(o *universe.ErasedOpts) {
		o.Binding = b
	}}
}

// WithBindingFn overrides the AutoReplicator binding for a bundle field
// using a per-stage factory closure. Useful for var-tail bindings
// (NewStatusEffectsBinding, NewInventoryBinding) that capture a typed
// *ecs.Map1[T] tied to a specific cell's ECS world — RegisterKind[T]
// runs the closure inside its per-stage realize step so each stage gets
// a binding bound to its own world.
//
// Takes priority over WithBinding when both are set. Only meaningful
// inside WithField[T].
func WithBindingFn(fn func(w *ecs.World) system.ComponentBinding) FieldOption {
	return universe.ComponentOption{Apply: func(o *universe.ErasedOpts) {
		o.BindingFn = fn
	}}
}

// LocalOnly marks a bundle field as local-only — added on transfer
// receive but never serialized for cross-cell transfer or replication.
// Equivalent to the `mmokit:"local"` struct tag, but useful when the
// local-only decision is computed at registration time.
func LocalOnly() FieldOption {
	return universe.ComponentOption{Apply: func(o *universe.ErasedOpts) {
		o.LocalOnly = true
	}}
}

// fieldOverride is the bundle-walker's per-component override record.
// Built by WithField[T](opts...) and indexed by component type during
// reflection so the walker knows which fields have non-default behavior.
type fieldOverride struct {
	typ  reflect.Type
	opts []FieldOption
}

func (fieldOverride) isRegisterKindArg() {}

// WithField returns a fieldOverride for component type T inside a bundle
// passed to RegisterKind[BundleT]. T must match the pointer-element type
// of one of the bundle's exported fields.
//
// Example:
//
//	mmokit.RegisterKind[ShipBundle](mmo, KindShip, "Ship", bindings,
//	    mmokit.WithField[gamecomp.Inventory](
//	        mmokit.WithMarshal(MarshalInventory, UnmarshalInventoryInto),
//	    ),
//	    mmokit.WithField[gamecomp.StatusEffects](
//	        mmokit.WithBinding(NewStatusEffectsBinding(c.StatusEffects)),
//	        mmokit.WithPreMarshal(clearSourceRefs),
//	    ),
//	)
func WithField[T any](opts ...FieldOption) fieldOverride {
	return fieldOverride{typ: reflect.TypeFor[T](), opts: opts}
}

// KindOption configures kind-scoped (rather than per-component) behavior on
// RegisterKind[T]. Constructed via WithExtraBinding.
type KindOption struct {
	apply func(*kindBuildContext)
}

func (KindOption) isRegisterKindArg() {}

// WithExtraBinding attaches an additional network binding to the entity
// kind that isn't tied to any bundle field. Used for components like
// Rotation that are registered for cross-cell transfer globally (via
// RegisterGlobalTransferComponents) but still need a per-kind network
// binding for replication to clients.
//
// Example:
//
//	mmokit.RegisterKind[ShipBundle](mmo, KindShip, "Ship", bindings,
//	    mmokit.WithExtraBinding(mmokit.QAngle(c.Rotation)),
//	)
func WithExtraBinding(b system.ComponentBinding) KindOption {
	return KindOption{apply: func(ctx *kindBuildContext) {
		ctx.extraBindings = append(ctx.extraBindings, func(_ *ecs.World) system.ComponentBinding { return b })
	}}
}

// WithExtraBindingFn is the per-stage variant of WithExtraBinding. The
// factory closure runs inside RegisterKind[T]'s realize step with the
// stage's ECS world, allowing the binding to capture a typed
// *ecs.Map1[T] bound to that specific world.
func WithExtraBindingFn(fn func(w *ecs.World) system.ComponentBinding) KindOption {
	return KindOption{apply: func(ctx *kindBuildContext) {
		ctx.extraBindings = append(ctx.extraBindings, fn)
	}}
}

// kindBuildContext is the internal state shared between RegisterKind and
// its KindOption appliers. extraBindings is a list of per-stage factory
// closures (static bindings are wrapped in a constant closure).
type kindBuildContext struct {
	extraBindings []func(w *ecs.World) system.ComponentBinding
}

// RegisterKindArg is the sealed sum type accepted by RegisterKind[T]'s
// variadic. Satisfied by fieldOverride (from WithField[T]) and KindOption
// (from WithExtraBinding).
type RegisterKindArg interface {
	isRegisterKindArg()
}

// RegisterKind registers an entity kind whose components are described by
// the bundle struct T. Each exported pointer-to-struct field of T becomes
// a registered KindComponent automatically — no per-field Field[T]() calls.
//
// Struct tags:
//
//	`mmokit:"local"`  — register as KindComponentLocalOnly (added on
//	                    transfer receive but never serialized).
//	`mmokit:"-"`      — skip this field entirely.
//
// Per-field overrides (custom binding, marshal, pre-marshal, local-only)
// are passed via WithField[T](opts...). Kind-scoped extras (e.g. Rotation
// network binding) attach via WithExtraBinding(b). Both flow through the
// same variadic via the RegisterKindArg sum.
//
// Example:
//
//	type PlayerBundle struct {
//	    Name       *PlayerName
//	    MoveTarget *mmokit.MoveTarget
//	    Input      *PlayerInput `mmokit:"local"`
//	}
//	mmokit.RegisterKind[PlayerBundle](mmo, KindPlayer, "Player", playerBindings)
func RegisterKind[T any](
	p *universe.Process,
	kind uint8,
	name string,
	bindings EngineBindingsConfig,
	args ...RegisterKindArg,
) {
	bundleType := reflect.TypeFor[T]()
	if bundleType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("mmokit.RegisterKind: T must be a struct, got %v", bundleType.Kind()))
	}

	// Partition args into per-field overrides and kind-scoped options.
	overrideByType := make(map[reflect.Type]fieldOverride)
	var ctx kindBuildContext
	for _, a := range args {
		switch v := a.(type) {
		case fieldOverride:
			if _, dup := overrideByType[v.typ]; dup {
				panic(fmt.Sprintf("mmokit.RegisterKind: duplicate WithField[%v]", v.typ))
			}
			overrideByType[v.typ] = v
		case KindOption:
			v.apply(&ctx)
		default:
			panic(fmt.Sprintf("mmokit.RegisterKind: unexpected arg type %T", a))
		}
	}

	// Walk bundle fields, build plan up front (for validation before
	// any cell exists).
	type fieldPlan struct {
		compType  reflect.Type
		localOnly bool
		opts      []FieldOption
	}
	var plan []fieldPlan
	for i := range bundleType.NumField() {
		f := bundleType.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("mmokit")
		if tag == "-" {
			continue
		}
		if f.Type.Kind() != reflect.Pointer {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must be a pointer (got %v)", bundleType.Name(), f.Name, f.Type.Kind()))
		}
		ct := f.Type.Elem()
		if ct.Kind() != reflect.Struct {
			panic(fmt.Sprintf("mmokit.RegisterKind: bundle field %s.%s must point to a struct (got *%v)", bundleType.Name(), f.Name, ct.Kind()))
		}
		ov, hasOv := overrideByType[ct]
		// Resolve LocalOnly from struct tag OR LocalOnly() option.
		local := tag == "local"
		if !local && hasOv {
			var probe universe.ErasedOpts
			for _, o := range ov.opts {
				o.Apply(&probe)
			}
			local = probe.LocalOnly
		}
		plan = append(plan, fieldPlan{
			compType:  ct,
			localOnly: local,
			opts:      ov.opts,
		})
	}
	if len(plan) == 0 {
		panic(fmt.Sprintf("mmokit.RegisterKind: bundle %s has no registrable fields", bundleType.Name()))
	}

	// Verify every override matched a field.
	matched := make(map[reflect.Type]bool, len(plan))
	for _, p := range plan {
		matched[p.compType] = true
	}
	for t := range overrideByType {
		if !matched[t] {
			panic(fmt.Sprintf("mmokit.RegisterKind: WithField[%v] does not match any bundle field", t))
		}
	}

	realize := func(stage *universe.Stage) {
		w := stage.ECSWorld()
		def := universe.EntityKindDef{Kind: kind, Name: name, EngineBindings: &bindings}
		for _, fp := range plan {
			id := ecs.TypeID(w, fp.compType)
			if fp.localOnly {
				universe.KindComponentByID(&def, w, id, fp.compType, true)
				continue
			}
			// Transfer codec — universe filters Binding/LocalOnly internally.
			universe.KindComponentByID(&def, w, id, fp.compType, false, fp.opts...)
			// Network binding precedence: BindingFn(w) > Binding > default reflect-derived.
			var bound system.ComponentBinding
			for _, o := range fp.opts {
				var probe universe.ErasedOpts
				o.Apply(&probe)
				if probe.BindingFn != nil {
					bound = probe.BindingFn.(func(*ecs.World) system.ComponentBinding)(w)
				} else if probe.Binding != nil {
					bound = probe.Binding.(system.ComponentBinding)
				}
			}
			if bound == nil {
				bound = system.ComponentByID(w, id, fp.compType)
			}
			def.NetworkBindings = append(def.NetworkBindings, bound)
		}
		for _, fn := range ctx.extraBindings {
			def.NetworkBindings = append(def.NetworkBindings, fn(w))
		}
		stage.RegisterEntityKind(def)
	}
	p.RegisterKindSpec(realize)
}
