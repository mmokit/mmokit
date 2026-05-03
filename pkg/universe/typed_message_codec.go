package universe

import "encoding/binary"

// Wire format for a typed cross-cell message:
//
//	[u16 typeNameLen][typeNameLen bytes type name][reflect-marshaled struct bytes]
//
// The type name is the Go reflect.Type.Name() of the message struct. Names
// must match between sending and receiving processes — typically the same
// build, but cross-version is fine as long as the type isn't renamed.

// EncodeTypedMessage builds the wire frame: type name length + name + body.
// body is produced by ReflectMarshal on the message pointer.
func EncodeTypedMessage(typeName string, msgPtr any) []byte {
	body := ReflectMarshal(msgPtr)
	nameBytes := []byte(typeName)
	out := make([]byte, 2+len(nameBytes)+len(body))
	binary.LittleEndian.PutUint16(out[0:2], uint16(len(nameBytes)))
	copy(out[2:], nameBytes)
	copy(out[2+len(nameBytes):], body)
	return out
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

// DecodeTypedMessage unmarshals payload bytes into ptr (pointer to struct).
// Wraps ReflectUnmarshal for symmetry with EncodeTypedMessage.
func DecodeTypedMessage(payload []byte, ptr any) {
	ReflectUnmarshal(payload, ptr)
}
