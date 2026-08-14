package service_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmokit/pkg/service"
)

type fooEvent struct{ N int }
type barEvent struct{ Name string }

func TestEventCodec_RegisterAndLookup(t *testing.T) {
	service.RegisterEventType[fooEvent]()
	service.RegisterEventType[barEvent]()
	got, ok := service.LookupEventType("github.com/zenion/mmokit/pkg/service_test.fooEvent")
	if !ok {
		t.Fatalf("LookupEventType(fooEvent) miss")
	}
	if got != reflect.TypeOf(fooEvent{}) {
		t.Fatalf("LookupEventType(fooEvent) = %v, want fooEvent", got)
	}
}

func TestEventCodec_RegisterIdempotent(t *testing.T) {
	// Second registration of the same T must not panic.
	service.RegisterEventType[fooEvent]()
	service.RegisterEventType[fooEvent]()
}

func TestEventCodec_LookupMiss(t *testing.T) {
	if _, ok := service.LookupEventType("does.not.Exist"); ok {
		t.Fatal("expected miss")
	}
}

type wireFooEvent struct{ N int }
type localBarEvent struct{ Name string }

func TestEventCodec_WireEligibility_DefaultOff(t *testing.T) {
	service.RegisterEventType[localBarEvent]()
	if service.IsWireEligible("github.com/zenion/mmokit/pkg/service_test.localBarEvent") {
		t.Fatal("RegisterEventType should NOT make a type wire-eligible")
	}
}

func TestEventCodec_RegisterWireEvent_OptsIn(t *testing.T) {
	service.RegisterWireEvent[wireFooEvent]()
	if !service.IsWireEligible("github.com/zenion/mmokit/pkg/service_test.wireFooEvent") {
		t.Fatal("RegisterWireEvent should opt in to wire eligibility")
	}
	// Also implicitly registered the type:
	if _, ok := service.LookupEventType("github.com/zenion/mmokit/pkg/service_test.wireFooEvent"); !ok {
		t.Fatal("RegisterWireEvent should also register the type in the codec")
	}
}

func TestEventCodec_RegisterWireEvent_Idempotent(t *testing.T) {
	service.RegisterWireEvent[wireFooEvent]()
	service.RegisterWireEvent[wireFooEvent]() // must not panic
}

func TestEventCodec_FrameworkEventsRegistered(t *testing.T) {
	for _, name := range []string{
		"github.com/zenion/mmokit/pkg/service.SessionEnterEvent",
		"github.com/zenion/mmokit/pkg/service.SessionLeaveEvent",
		"github.com/zenion/mmokit/pkg/service.AuthLoginSucceededEvent",
		"github.com/zenion/mmokit/pkg/service.AuthLogoutEvent",
		"github.com/zenion/mmokit/pkg/service.AuthRegisteredEvent",
		"github.com/zenion/mmokit/pkg/service.PlayerSpawnedEvent",
		"github.com/zenion/mmokit/pkg/service.PlayerDespawnedEvent",
	} {
		if _, ok := service.LookupEventType(name); !ok {
			t.Errorf("framework event %s not registered", name)
		}
	}
}
