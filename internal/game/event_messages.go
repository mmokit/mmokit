package game

// Typed server→client event messages — registered via mmokit.RegisterEvent[T]
// and emitted with mmokit.SendEvent. Replaces the legacy gamepb.* /
// enginepb.* protobuf-envelope path on the 0x00 channel.
//
// Wire format: pkguniverse.EncodeTypedEventFrame — fields encoded in source
// declaration order, little-endian, no padding (mirrors the codec defined in
// pkg/universe/reflect_marshal.go). String fields are length-prefixed (uint16
// LE length + UTF-8 bytes). The reflect codec rejects unsupported field types
// at registration time.
//
// Naming: proto field foo_bar_baz → Go field FooBarBaz. The TS codegen
// emits camelCase property names (fooBarBaz) automatically — see
// cmd/sdkgen/broadcasts.go.

// PlayerDied — sent to a player when their entity dies.
// Replaces gamepb.PlayerDiedMsg.
type PlayerDied struct {
	KillerID uint32 // network ID of who killed you, 0 if unknown
}
