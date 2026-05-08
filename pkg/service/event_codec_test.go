package service_test

import (
	"reflect"
	"testing"

	"github.com/zenion/mmoserver/pkg/service"
)

type fooEvent struct{ N int }
type barEvent struct{ Name string }

func TestEventCodec_RegisterAndLookup(t *testing.T) {
	service.RegisterEventType[fooEvent]()
	service.RegisterEventType[barEvent]()
	got, ok := service.LookupEventType("github.com/zenion/mmoserver/pkg/service_test.fooEvent")
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

func TestEventCodec_FrameworkEventsRegistered(t *testing.T) {
	for _, name := range []string{
		"github.com/zenion/mmoserver/pkg/service.SessionEnterEvent",
		"github.com/zenion/mmoserver/pkg/service.SessionLeaveEvent",
		"github.com/zenion/mmoserver/pkg/service.AuthLoginSucceededEvent",
		"github.com/zenion/mmoserver/pkg/service.AuthLogoutEvent",
		"github.com/zenion/mmoserver/pkg/service.AuthRegisteredEvent",
		"github.com/zenion/mmoserver/pkg/service.PlayerSpawnedEvent",
		"github.com/zenion/mmoserver/pkg/service.PlayerDespawnedEvent",
	} {
		if _, ok := service.LookupEventType(name); !ok {
			t.Errorf("framework event %s not registered", name)
		}
	}
}
