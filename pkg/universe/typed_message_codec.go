package universe

import (
	"encoding/binary"
	"fmt"
)

// Wire format for a typed cross-cell message:
//
//	[u16 typeNameLen][typeNameLen bytes type name][reflect-marshaled struct bytes]
//
// The type name is the package-qualified reflect.Type.String() of the
// message struct (e.g. "combat.Damage") so two same-named types in
// different packages don't collide on the wire. Names must match between
// sending and receiving processes — typically the same build, but cross-
// version is fine as long as the type isn't renamed or moved.

// EncodeTypedMessage builds the wire frame: type name length + name + body.
// body is produced by ReflectMarshal on the message pointer.
//
// Returns an error rather than a truncated prefix when the body or the type
// name does not fit its u16 length field — SplitTypedMessage on the receiving
// cell would otherwise read the frame as a differently-shaped message.
func EncodeTypedMessage(typeName string, msgPtr any) ([]byte, error) {
	body, err := ReflectMarshal(msgPtr)
	if err != nil {
		return nil, err
	}
	nameBytes := []byte(typeName)
	if len(nameBytes) > maxWireStringLen {
		return nil, fmt.Errorf("reflect_marshal: type name of %d bytes exceeds the %d-byte u16 wire prefix",
			len(nameBytes), maxWireStringLen)
	}
	out := make([]byte, 2+len(nameBytes)+len(body))
	binary.LittleEndian.PutUint16(out[0:2], uint16(len(nameBytes)))
	copy(out[2:], nameBytes)
	copy(out[2+len(nameBytes):], body)
	return out, nil
}

// SplitTypedMessage decodes the wire frame's type name and returns the
// remaining payload bytes for ReflectUnmarshal. Returns ("", nil) on a
// malformed frame.
func SplitTypedMessage(data []byte) (typeName string, payload []byte) {
	if len(data) < 2 {
		return "", nil
	}
	n := int(binary.LittleEndian.Uint16(data[0:2]))
	if 2+n > len(data) {
		return "", nil
	}
	return string(data[2 : 2+n]), data[2+n:]
}

// DecodeTypedMessageOnStage unmarshals payload bytes into ptr (pointer to
// struct), threading stage to any registered field codecs that need it
// (notably mmokit.Entity, which resolves its local ECS handle via the
// stage's NetID index).
//
// Use on the dest cell of a cross-cell typed-message dispatch — Entity
// fields decoded without a stage have a nil .Stage() and can't be Send'd
// onward or used to lookup components.
func DecodeTypedMessageOnStage(stage *Stage, payload []byte, ptr any) {
	ReflectUnmarshalOnStage(stage, payload, ptr)
}
