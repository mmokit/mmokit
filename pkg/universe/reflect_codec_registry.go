package universe

import (
	"reflect"
	"sync"
)

// ReflectCodec is a custom encoder/decoder for a specific reflect.Type.
// Plug in when the default reflection path can't handle a type — the
// canonical example is mmokit.Entity, which contains a *Stage pointer the
// reflective codec would reject, but which we want to wire-encode as its
// uint32 NetID.
//
// Encode writes the field value to buf at offset 0; Size returns the byte
// width on the wire (fixed-size only for now); Decode reads from data at
// offset 0. The stage parameter on Decode threads stage context to codecs
// that need it (e.g. mmokit.Entity's NetID resolution via EntityByNetID).
type ReflectCodec struct {
	Size   func() int
	Encode func(buf []byte, v reflect.Value)
	Decode func(stage *Stage, data []byte, v reflect.Value)
}

var (
	codecsMu sync.RWMutex
	codecs   = map[reflect.Type]*ReflectCodec{}
)

// RegisterReflectCodec installs a custom codec for type t. Call from init().
// Subsequent ReflectMarshal/ReflectUnmarshalOnStage calls on structs with
// fields of type t delegate to the codec instead of the default reflection
// path.
func RegisterReflectCodec(t reflect.Type, codec *ReflectCodec) {
	codecsMu.Lock()
	codecs[t] = codec
	codecsMu.Unlock()
}

// LookupReflectCodec returns the registered codec for t, or nil if none.
func LookupReflectCodec(t reflect.Type) *ReflectCodec {
	codecsMu.RLock()
	defer codecsMu.RUnlock()
	return codecs[t]
}
