// Package mmokit is the game-facing facade for the mmokit MMO framework.
//
// The framework reduces game development to five primitives:
//
//  1. Entity   — a value handle that's transparent across cell boundaries.
//  2. Get/Set  — generic component access on an entity.
//  3. Spawn    — create entities of a registered kind at a position.
//  4. Nearby   — iterate entities within a radius on the spatial grid.
//  5. Send     — deliver a typed message to an entity, regardless of which
//     cell currently owns it. Handlers register via Handle[M].
//
// Per-tick game logic uses OnTickEach[Bundle] (or OnTick[T] / OnWorldTick)
// rather than custom system structs with Query fields. Direct ECS access
// (RawWorld) is available as a labeled escape hatch.
//
// See docs/superpowers/specs/2026-05-03-entity-message-passing-design.md
// for the full design rationale.
//
// Example: deal damage that works regardless of cell boundaries.
//
//	type Damage struct {
//	    Amount float32
//	    Source mmokit.Entity
//	    Dealt  float32  // result, mutated by handler
//	}
//
//	mmokit.Handle(stage, func(target mmokit.Entity, msg *Damage) {
//	    h := mmokit.Get[Health](target); if h == nil { return }
//	    h.Current -= msg.Amount
//	    msg.Dealt = msg.Amount
//	})
//
//	// Anywhere in game code:
//	target.Send(&Damage{Amount: 25, Source: caster})
//
// In addition to the new five primitives, the package re-exports types from
// all pkg/ sub-packages so games can use one import instead of 5–7 aliased
// ones. For ECS queries and custom systems, also import
// "github.com/mlange-42/ark/ecs" since generic types like ecs.Map1[T] and
// ecs.Filter2[A,B] cannot be aliased.
package mmokit
