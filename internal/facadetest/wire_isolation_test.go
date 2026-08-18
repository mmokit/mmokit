package facadetest

import (
	"reflect"
	"slices"
	"testing"

	"github.com/mmokit/mmokit"
)

type alphaOnlyEvent struct{ A uint32 }
type betaOnlyEvent struct{ B uint32 }

type alphaOnlyOpReq struct{ X int32 }
type alphaOnlyOpRes struct{ Y int32 }

// TestWireRegistriesAreProcessScoped is the acceptance criterion for CE-010
// part B, and it could not be written before it: the four wire registries were
// package globals, so two Processes in one binary shared one namespace by
// construction and every assertion below was true of both or neither.
//
// mmokit.New guards its flag parsing with `if !flag.Parsed()`, so building a
// second Process in one binary is safe.
func TestWireRegistriesAreProcessScoped(t *testing.T) {
	p1 := mmokit.New(mmokit.Config{Name: "a", Mode: "all", CellsX: 1, CellsY: 1, Headless: true})
	p2 := mmokit.New(mmokit.Config{Name: "b", Mode: "all", CellsX: 1, CellsY: 1, Headless: true})

	mmokit.RegisterEvent[alphaOnlyEvent](p1)
	mmokit.RegisterEvent[betaOnlyEvent](p2)

	alpha := reflect.TypeFor[alphaOnlyEvent]()
	beta := reflect.TypeFor[betaOnlyEvent]()

	if !slices.Contains(p1.Wire().ServerEventTypes(), alpha) {
		t.Error("p1 is missing the event registered against it")
	}
	if slices.Contains(p1.Wire().ServerEventTypes(), beta) {
		t.Error("p1 sees an event registered against p2 — the registry is still shared")
	}
	if !slices.Contains(p2.Wire().ServerEventTypes(), beta) {
		t.Error("p2 is missing the event registered against it")
	}
	if slices.Contains(p2.Wire().ServerEventTypes(), alpha) {
		t.Error("p2 sees an event registered against p1 — the registry is still shared")
	}

	// Both still carry the engine-level events bootstrapWire installs: process
	// scoping must not cost a process its framework types.
	for _, p := range []*mmokit.Process{p1, p2} {
		if !slices.Contains(p.Wire().ServerEventTypes(), reflect.TypeFor[mmokit.Pong]()) {
			t.Errorf("%s: engine-default Pong missing", p.Config().Name)
		}
		if !slices.Contains(p.Wire().ClientInputTypes(), reflect.TypeFor[mmokit.Ping]()) {
			t.Errorf("%s: engine-default Ping client input missing", p.Config().Name)
		}
	}
}

// TestTypedOpHandlersDoNotLeakAcrossProcesses is the live bug this unit exists
// to close, reduced to its mechanism.
//
// RegisterOp's duplicate path ends with `existing.Handler = handler` on what
// used to be a binary-global map. Every service registers handlers that close
// over their own *Process — RegisterAuthService most consequentially — so with
// one shared map, the second Process to register left the FIRST dispatching
// into the SECOND's service. examples/4node-basic/mesh_e2e_test.go already
// builds N Processes in one binary, so this was reachable, not hypothetical.
func TestTypedOpHandlersDoNotLeakAcrossProcesses(t *testing.T) {
	p1 := mmokit.New(mmokit.Config{Name: "a", Mode: "all", CellsX: 1, CellsY: 1, Headless: true})
	p2 := mmokit.New(mmokit.Config{Name: "b", Mode: "all", CellsX: 1, CellsY: 1, Headless: true})

	// The same request type on both, as two Processes each registering the
	// same service would do — with handlers that answer distinguishably.
	mmokit.RegisterOp(p1, mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, _ *alphaOnlyOpReq) (*alphaOnlyOpRes, error) {
			return &alphaOnlyOpRes{Y: 1}, nil
		})
	mmokit.RegisterOp(p2, mmokit.RouteGatewayLocal,
		func(_ *mmokit.OpContext, _ *alphaOnlyOpReq) (*alphaOnlyOpRes, error) {
			return &alphaOnlyOpRes{Y: 2}, nil
		})

	reqID := mmokit.TypeIDOf(reflect.TypeFor[alphaOnlyOpReq]())
	for _, tc := range []struct {
		name string
		p    *mmokit.Process
		want int32
	}{{"p1", p1, 1}, {"p2", p2, 2}} {
		entry, ok := tc.p.Wire().LookupTypedOp(reqID)
		if !ok {
			t.Fatalf("%s: op not registered", tc.name)
		}
		handler, ok := entry.Handler.(func(*mmokit.OpContext, *alphaOnlyOpReq) (*alphaOnlyOpRes, error))
		if !ok {
			t.Fatalf("%s: handler has type %T", tc.name, entry.Handler)
		}
		res, err := handler(&mmokit.OpContext{}, &alphaOnlyOpReq{})
		if err != nil {
			t.Fatalf("%s: handler: %v", tc.name, err)
		}
		if res.Y != tc.want {
			t.Errorf("%s dispatched into the other process's handler: got Y=%d, want %d",
				tc.name, res.Y, tc.want)
		}
	}
}
