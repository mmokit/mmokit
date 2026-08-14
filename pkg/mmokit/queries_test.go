package mmokit_test

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/zenion/mmokit/pkg/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestAny_True returns true when an entity with T exists.
func TestAny_True(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap1[component.Position](w)
	mapper.NewEntity(&component.Position{X: 1})

	if !mmokit.Any[component.Position](stage) {
		t.Fatal("Any returned false for present component")
	}
}

// TestAny_False returns false when no entity has T.
func TestAny_False(t *testing.T) {
	stage, _ := newTestStage(t)
	if mmokit.Any[component.Position](stage) {
		t.Fatal("Any returned true for absent component")
	}
}

// TestFindOne returns the first matching entity wrapped as mmokit.Entity.
func TestFindOne(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	want := mapper.NewEntity(
		&component.Position{X: 42},
		&component.NetworkID{ID: 7},
	)
	stage.RegisterLiveNetID(7, want)

	got, ok := mmokit.FindOne[component.Position](stage)
	if !ok {
		t.Fatal("FindOne returned ok=false")
	}
	if got.Handle() != want {
		t.Fatalf("FindOne returned handle %v, want %v", got.Handle(), want)
	}
}

// TestFindOne_None returns ok=false when no entity has T.
func TestFindOne_None(t *testing.T) {
	stage, _ := newTestStage(t)
	_, ok := mmokit.FindOne[component.Position](stage)
	if ok {
		t.Fatal("FindOne returned ok=true with no matching entity")
	}
}

// TestForEach1 iterates every entity with T.
func TestForEach1(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mapper := ecs.NewMap2[component.Position, component.NetworkID](w)
	for i := 0; i < 5; i++ {
		h := mapper.NewEntity(
			&component.Position{X: float32(i)},
			&component.NetworkID{ID: uint32(100 + i)},
		)
		stage.RegisterLiveNetID(uint32(100+i), h)
	}

	count := 0
	mmokit.ForEach1(stage, func(e mmokit.Entity, pos *component.Position) {
		count++
	})
	if count != 5 {
		t.Fatalf("ForEach1 visited %d entities, want 5", count)
	}
}

// TestForEach2 iterates entities with both T1 and T2.
func TestForEach2(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	pv := ecs.NewMap3[component.Position, component.Velocity, component.NetworkID](w)
	pOnly := ecs.NewMap2[component.Position, component.NetworkID](w)
	h1 := pv.NewEntity(
		&component.Position{X: 1}, &component.Velocity{X: 2}, &component.NetworkID{ID: 200},
	)
	h2 := pv.NewEntity(
		&component.Position{X: 3}, &component.Velocity{X: 4}, &component.NetworkID{ID: 201},
	)
	pOnly.NewEntity(&component.Position{X: 99}, &component.NetworkID{ID: 202}) // should NOT be visited
	stage.RegisterLiveNetID(200, h1)
	stage.RegisterLiveNetID(201, h2)

	count := 0
	mmokit.ForEach2(stage,
		func(e mmokit.Entity, p *component.Position, v *component.Velocity) {
			count++
		})
	if count != 2 {
		t.Fatalf("ForEach2 visited %d entities, want 2", count)
	}
}

// TestForEach3 iterates entities with three required components.
func TestForEach3(t *testing.T) {
	stage, _ := newTestStage(t)
	w := stage.ECSWorld()
	mp := ecs.NewMap4[component.Position, component.Velocity, component.Rotation, component.NetworkID](w)
	h := mp.NewEntity(
		&component.Position{X: 1},
		&component.Velocity{X: 2},
		&component.Rotation{Angle: 0.5},
		&component.NetworkID{ID: 300},
	)
	stage.RegisterLiveNetID(300, h)

	count := 0
	mmokit.ForEach3(stage,
		func(e mmokit.Entity, p *component.Position, v *component.Velocity, r *component.Rotation) {
			count++
		})
	if count != 1 {
		t.Fatalf("ForEach3 visited %d entities, want 1", count)
	}
}
