package mmokit

import (
	"reflect"
	"testing"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

func TestServerEventsRegisterAndSchema(t *testing.T) {
	e := NewServerEvents()
	RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
	RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PONG, WithEventName("pingResponse"))

	schema := e.Schema()
	want := []ServerEventSchema{
		{Code: uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), Name: "playerSpawned", ProtoName: "enginepb.SpawnedMsg"},
		{Code: uint32(enginepb.ServerEventCode_SE_PONG), Name: "pingResponse", ProtoName: "enginepb.PongMsg"},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Errorf("Schema() = %+v, want %+v", schema, want)
	}
}

func TestServerEventsDuplicateRegistrationPanics(t *testing.T) {
	e := NewServerEvents()
	RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	RegisterServerEvent[enginepb.PongMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
}

func TestServerEventsBuildUnregisteredPanics(t *testing.T) {
	e := NewServerEvents()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Build with unregistered code")
		}
	}()
	e.Build(uint32(enginepb.ServerEventCode_SE_PONG), &enginepb.PongMsg{})
}

func TestServerEventsBuildWrongTypePanics(t *testing.T) {
	e := NewServerEvents()
	RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on type mismatch")
		}
	}()
	e.Build(uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), &enginepb.PongMsg{})
}
