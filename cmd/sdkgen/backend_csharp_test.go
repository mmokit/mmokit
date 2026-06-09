package main

import (
	"strings"
	"testing"
)

func sampleEntitySchema() ProtocolSchema {
	return ProtocolSchema{
		Game: "demo",
		Entities: []EntitySchema{
			{
				Kind: 0,
				Name: "Ship",
				Bindings: []BindingSchema{
					{Type: "Position", Fields: []BindingSchemaField{
						{Name: "x", Encoding: "f32", Size: 4},
						{Name: "y", Encoding: "f32", Size: 4},
					}},
					{Type: "Velocity", Fields: []BindingSchemaField{
						{Name: "vx", Encoding: "qvel", Size: 2, Scale: 0.01},
					}},
					{Type: "Name", Fields: []BindingSchemaField{
						{Name: "shipName", Encoding: "string", Initial: true},
					}},
				},
				VarTail: &VarTailSchema{
					Name:     "statusEffects",
					ItemSize: 5,
					ItemFields: []BindingSchemaField{
						{Name: "kind", Encoding: "u8", Size: 1},
						{Name: "stacks", Encoding: "f32", Size: 4},
					},
				},
			},
			{Kind: 1, Name: "Asteroid", Bindings: []BindingSchema{
				{Type: "Position", Fields: []BindingSchemaField{
					{Name: "x", Encoding: "f32", Size: 4},
				}},
			}},
		},
		BroadcastTypes: []BroadcastTypeSchema{
			{Name: "game.Damage", TypeID: 0xA1B2C3D4, Fields: []BroadcastFieldSchema{
				{Name: "amount", Encoding: "f32"},
				{Name: "victim", Encoding: "entity"},
			}},
		},
		ServerEventTypes: []ServerEventTypeSchema{
			{Name: "game.Pong", TypeID: 0x11223344, Fields: []BroadcastFieldSchema{
				{Name: "nonce", Encoding: "u32"},
			}},
		},
		ClientInputTypes: []ClientInputTypeSchema{
			{Name: "game.SetMoveTarget", TypeID: 0x55667788, Fields: []BroadcastFieldSchema{
				{Name: "x", Encoding: "f32"},
				{Name: "y", Encoding: "f32"},
				{Name: "tags", Encoding: "slice", Item: &BroadcastFieldSchema{Name: "tag", Encoding: "u32"}},
			}},
		},
		Operations: []OperationSchema{
			{
				RequestTypeName: "auth.AuthLoginRequest", RequestTypeID: 0xAA01,
				RequestFields:    []BroadcastFieldSchema{{Name: "Username", Encoding: "string"}, {Name: "Password", Encoding: "string"}},
				ResponseTypeName: "auth.AuthLoginResponse", ResponseTypeID: 0xAA02,
				ResponseFields:   []BroadcastFieldSchema{{Name: "SessionToken", Encoding: "string"}, {Name: "ExpiresAtMs", Encoding: "i64"}},
			},
		},
	}
}

func TestCsharpBackend_EntityType(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntityType(sampleEntitySchema())
	for _, want := range []string{
		"namespace Mmokit.Sdk",
		"public enum EntityType : byte",
		"Ship = 0,",
		"Asteroid = 1,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntityType missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Entities(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEntities(sampleEntitySchema())
	for _, want := range []string{
		"public abstract class EntityBase",
		"public uint NetID;",
		"public sealed class ShipEntity : EntityBase",
		"public float x;",         // f32 → float
		"public float vx;",        // qvel → float
		"public string shipName;", // string + initial field present
		"public List<ShipStatusEffectsItem> statusEffects = new();",
		"public sealed class ShipStatusEffectsItem",
		"public byte kind;",       // u8 → byte
		"public sealed class AsteroidEntity : EntityBase",
		"public sealed class DeltaWorldUpdate",
		"public List<EntityBase> Entered = new();",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEntities missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_OutputFiles_SkipsWhenNoEntities(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(ProtocolSchema{})
	if len(files) != 0 {
		t.Fatalf("expected no files for empty schema, got %d", len(files))
	}
	files = csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	for _, name := range []string{"EntityType.cs", "Entities.cs"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("expected %s in OutputFiles", name)
		}
	}
}

func TestCsharpBackend_CoreFiles(t *testing.T) {
	core := csharpBackend{coreDir: "x/core"}.CoreFiles()
	if len(core) != 6 {
		t.Fatalf("CoreFiles len = %d, want 6", len(core))
	}
	// Dst basenames are what the SDK compiles; Src is threaded from coreDir.
	if core[0].Dst != "DeltaDecoderCore.cs" || core[0].Src != "x/core/DeltaDecoderCore.cs" {
		t.Fatalf("CoreFiles[0] = %+v", core[0])
	}
	// ReflectCodec.cs must be among the copied runtime files.
	var hasReflect bool
	for _, c := range core {
		if c.Dst == "ReflectCodec.cs" {
			hasReflect = true
		}
	}
	if !hasReflect {
		t.Fatalf("CoreFiles missing ReflectCodec.cs: %+v", core)
	}
}

func TestCsharpBackend_Events(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genEvents(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class Damage",
		"public const uint TypeID = 0xa1b2c3d4u;",
		"public float amount;",
		"public uint victim;", // entity → uint
		"public static Damage Decode(byte[] buf)",
		"m.amount = r.ReadF32();",
		"m.victim = r.ReadEntity();",
		"public sealed class Pong",
		"public sealed class TypedDispatcher",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genEvents missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Inputs(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genInputs(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class SetMoveTarget",
		"public byte[] Encode()",
		"w.WriteF32(this.x);",
		"public List<uint> tags = new();",
		"w.WriteSliceLen(this.tags.Count); foreach (var _v in this.tags) w.WriteU32(_v);",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genInputs missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_Operations(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genOperations(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class AuthLoginRequest",
		"public byte[] Encode()", // request encodes
		"public static AuthLoginRequest Decode(byte[] buf)",
		"public sealed class AuthLoginResponse",
		"w.WriteString(this.Username);",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genOperations missing %q in:\n%s", want, out)
		}
	}
	// Response is decode-only (no Encode()).
	respIdx := strings.Index(out, "class AuthLoginResponse")
	if respIdx >= 0 && strings.Contains(out[respIdx:], "public byte[] Encode()") {
		t.Fatalf("AuthLoginResponse should be decode-only (no Encode)")
	}
}

func TestCsharpBackend_OutputFiles_IncludesReflectFiles(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	for _, name := range []string{"EntityType.cs", "Entities.cs", "Events.cs", "Inputs.cs", "Operations.cs"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("OutputFiles missing %s", name)
		}
	}
}

func TestCsharpBackend_DeltaDecoder(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genDeltaDecoder(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class DemoDeltaDecoder",
		"public DeltaWorldUpdate Decode(byte[] data)",
		"static ShipEntity DecodeShipEntitySnapshot(byte[] snap, byte[]? initial, ShipEntity? existing)",
		"e.x = DeltaDecoderCore.ReadFloat32(snap, o); o += 4;",
		"e.vx = (float)DeltaDecoderCore.UnVel(DeltaDecoderCore.ReadInt16(snap, o), 0.01); o += 2;",
		"e.statusEffects.Add(_it);",
		"int initialOff = 0;",
		"Encoding.UTF8.GetString(initial, initialOff + 1, _l)",
		"case 0: { var e = DecodeShipEntitySnapshot(snap, initial, existing as ShipEntity);",
		"static bool HasVarTailFor(byte type_)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genDeltaDecoder missing %q in:\n%s", want, out)
		}
	}
}

func TestCsharpBackend_OutputFiles_IncludesDeltaDecoder(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	if _, ok := files["DeltaDecoder.cs"]; !ok {
		t.Fatalf("OutputFiles missing DeltaDecoder.cs")
	}
}

func TestCsharpBackend_Client(t *testing.T) {
	out := csharpBackend{namespace: "Mmokit.Sdk"}.genClient(sampleEntitySchema())
	for _, want := range []string{
		"public sealed class DemoClient",
		"public void Connect(string host, int port, int handshakeTimeoutMs = 5000)",
		"_transport = UdpTransport.Connect(host, port, handshakeTimeoutMs);",
		"public DemoDeltaDecoder Decoder { get; } = new();",
		"public TypedDispatcher TypedEvents { get; } = new();",
		"public async Task<AuthLoginResponse> AuthLogin(AuthLoginRequest req)",
		"byte[] raw = await CallOp(AuthLoginRequest.TypeID, req.Encode());",
		"return AuthLoginResponse.Decode(raw);",
		"public void SendSetMoveTarget(SetMoveTarget msg, bool reliable = false)",
		"public Action OnPong(Action<Pong> handler) => TypedEvents.On(Pong.TypeID, b => handler(Pong.Decode(b)));",
		"frame[0] = 0x01;",
		"frame[0] = 0x00;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("genClient missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "OperationError.TypeID") {
		t.Fatalf("genClient should not reference OperationError when absent from schema")
	}
}

func TestCsharpBackend_OutputFiles_IncludesClient(t *testing.T) {
	files := csharpBackend{namespace: "Mmokit.Sdk"}.OutputFiles(sampleEntitySchema())
	if _, ok := files["Client.cs"]; !ok {
		t.Fatalf("OutputFiles missing Client.cs")
	}
}
