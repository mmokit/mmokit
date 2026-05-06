package mmokit

import (
	"reflect"
	"testing"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
)

func TestServerEventsRegisterAndSchema(t *testing.T) {
	e := NewServerEvents()
	RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
	RegisterServerEvent[enginepb.ServerConfigMsg](e, enginepb.ServerEventCode_SE_SERVER_CONFIG, WithEventName("serverHello"))

	schema := e.Schema()
	want := []ServerEventSchema{
		{Code: uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), Name: "playerSpawned", ProtoName: "enginepb.SpawnedMsg"},
		{Code: uint32(enginepb.ServerEventCode_SE_SERVER_CONFIG), Name: "serverHello", ProtoName: "enginepb.ServerConfigMsg"},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Errorf("Schema() = %+v, want %+v", schema, want)
	}
}

func TestServerEventsDuplicateRegistrationLastWins(t *testing.T) {
	// Contract: later RegisterServerEvent calls silently replace earlier
	// ones for the same code. This lets games override engine-default
	// registrations installed by NewProtocol (e.g. swap enginepb.SpawnedMsg
	// for a richer game-specific payload at SE_PLAYER_SPAWNED).
	e := NewServerEvents()
	RegisterServerEvent[enginepb.SpawnedMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED)
	RegisterServerEvent[enginepb.ServerConfigMsg](e, enginepb.ServerEventCode_SE_PLAYER_SPAWNED, WithEventName("overridden"))

	schema := e.Schema()
	want := []ServerEventSchema{
		{Code: uint32(enginepb.ServerEventCode_SE_PLAYER_SPAWNED), Name: "overridden", ProtoName: "enginepb.ServerConfigMsg"},
	}
	if !reflect.DeepEqual(schema, want) {
		t.Errorf("Schema() = %+v, want %+v (last registration must win)", schema, want)
	}
}

