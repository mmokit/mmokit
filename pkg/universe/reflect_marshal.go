package universe

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"

	"github.com/mlange-42/ark/ecs"
)

var ecsEntityType = reflect.TypeFor[ecs.Entity]()

// ValidateComponentType panics if t contains any fields with unsupported types.
// Call this at ECS-component registration time to catch problems early.
// Slices are rejected — components must have a fixed memory shape for the
// ECS column store. For typed-event message types (which support slices),
// use ValidateMessageType.
func ValidateComponentType(t reflect.Type) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflect_marshal: expected struct, got %s", t.Kind()))
	}
	validateStruct(t, "", false)
}

// ValidateMessageType is the same as ValidateComponentType but accepts
// slice fields — the typed-event wire codec serializes them as
// [u16 len][elem0]...[elemN]. Use for types registered via
// mmokit.RegisterEvent[T] / RegisterClientInputType[T].
func ValidateMessageType(t reflect.Type) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflect_marshal: expected struct, got %s", t.Kind()))
	}
	validateStruct(t, "", true)
}

func validateStruct(t reflect.Type, path string, allowSlice bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fpath := path + "." + f.Name
		if f.Type == ecsEntityType {
			continue // skipped during marshal
		}
		validateType(f.Type, fpath, allowSlice)
	}
}

func validateType(t reflect.Type, path string, allowSlice bool) {
	// Custom-codec-registered types are accepted regardless of their default
	// reflective shape (e.g. mmokit.Entity contains a *Stage pointer the
	// default validator would reject).
	if LookupReflectCodec(t) != nil {
		return
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64,
		reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Bool, reflect.String:
		// supported
	case reflect.Array:
		validateType(t.Elem(), path+"[]", allowSlice)
	case reflect.Slice:
		if !allowSlice {
			panic(fmt.Sprintf("reflect_marshal: unsupported type %s at %s (kind=%s)", t, path, t.Kind()))
		}
		// []byte fast path: encoded as [u32 len][bytes] without per-element
		// iteration. Same validity rule as any byte field — always allowed.
		if t.Elem().Kind() == reflect.Uint8 {
			return
		}
		validateType(t.Elem(), path+"[]", allowSlice)
	case reflect.Struct:
		validateStruct(t, path, allowSlice)
	default:
		panic(fmt.Sprintf("reflect_marshal: unsupported type %s at %s (kind=%s)", t, path, t.Kind()))
	}
}

// ReflectMarshal serializes a struct pointer to binary.
// Unexported fields and ecs.Entity fields are skipped.
func ReflectMarshal(ptr any) []byte {
	v := reflect.ValueOf(ptr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	// Pre-calculate size to avoid repeated allocation.
	size := structSize(v)
	buf := make([]byte, size)
	off := marshalStruct(buf, 0, v)
	return buf[:off]
}

// ReflectUnmarshalOnStage decodes data into the struct ptr, threading stage
// to any registered field codecs that need it. Use when the struct contains
// types whose decode is stage-dependent (e.g. mmokit.Entity, which resolves
// its local ECS handle via the stage's NetID index).
func ReflectUnmarshalOnStage(stage *Stage, data []byte, ptr any) {
	v := reflect.ValueOf(ptr)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	unmarshalStructOnStage(stage, data, 0, v)
}

// ReflectUnmarshal deserializes binary data into a struct pointer.
// Unexported fields and ecs.Entity fields are left at zero value.
//
// Preserves the no-stage call signature for callers that don't need stage
// context (the existing transfer codec). Equivalent to
// ReflectUnmarshalOnStage(nil, data, ptr).
func ReflectUnmarshal(data []byte, ptr any) {
	ReflectUnmarshalOnStage(nil, data, ptr)
}

func structSize(v reflect.Value) int {
	total := 0
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			total += codec.Size()
			continue
		}
		total += valueSize(v.Field(i))
	}
	return total
}

func valueSize(v reflect.Value) int {
	switch v.Kind() {
	case reflect.Float32:
		return 4
	case reflect.Float64:
		return 8
	case reflect.Uint8, reflect.Int8, reflect.Bool:
		return 1
	case reflect.Uint16, reflect.Int16:
		return 2
	case reflect.Uint32, reflect.Int32:
		return 4
	case reflect.Uint64, reflect.Int64:
		return 8
	case reflect.String:
		return 2 + len(v.String())
	case reflect.Array:
		n := v.Len()
		if n == 0 {
			return 0
		}
		// All elements have the same type, but strings vary in length.
		total := 0
		for i := range n {
			total += valueSize(v.Index(i))
		}
		return total
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. Uses u32 (vs u16 for
		// generic slices) because typed binary payloads (e.g. WorldDelta
		// body) routinely exceed 65535 bytes for stress-test densities.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return 4 + v.Len()
		}
		// Wire layout: [u16 len][elem0]...[elemN-1]. Slices are length-prefixed
		// rather than fixed-stride because they can hold strings (themselves
		// length-prefixed) or nested structs whose own size varies.
		n := v.Len()
		total := 2
		for i := range n {
			total += valueSize(v.Index(i))
		}
		return total
	case reflect.Struct:
		return structSize(v)
	default:
		return 0
	}
}

func marshalStruct(buf []byte, off int, v reflect.Value) int {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			codec.Encode(buf[off:], v.Field(i))
			off += codec.Size()
			continue
		}
		off = marshalValue(buf, off, v.Field(i))
	}
	return off
}

func marshalValue(buf []byte, off int, v reflect.Value) int {
	switch v.Kind() {
	case reflect.Float32:
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(float32(v.Float())))
		return off + 4
	case reflect.Float64:
		binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(v.Float()))
		return off + 8
	case reflect.Uint8:
		buf[off] = uint8(v.Uint())
		return off + 1
	case reflect.Uint16:
		binary.LittleEndian.PutUint16(buf[off:], uint16(v.Uint()))
		return off + 2
	case reflect.Uint32:
		binary.LittleEndian.PutUint32(buf[off:], uint32(v.Uint()))
		return off + 4
	case reflect.Uint64:
		binary.LittleEndian.PutUint64(buf[off:], v.Uint())
		return off + 8
	case reflect.Int8:
		buf[off] = byte(int8(v.Int()))
		return off + 1
	case reflect.Int16:
		binary.LittleEndian.PutUint16(buf[off:], uint16(int16(v.Int())))
		return off + 2
	case reflect.Int32:
		binary.LittleEndian.PutUint32(buf[off:], uint32(int32(v.Int())))
		return off + 4
	case reflect.Int64:
		binary.LittleEndian.PutUint64(buf[off:], uint64(v.Int()))
		return off + 8
	case reflect.Bool:
		if v.Bool() {
			buf[off] = 1
		} else {
			buf[off] = 0
		}
		return off + 1
	case reflect.String:
		s := v.String()
		binary.LittleEndian.PutUint16(buf[off:], uint16(len(s)))
		off += 2
		copy(buf[off:], s)
		return off + len(s)
	case reflect.Array:
		for i := range v.Len() {
			off = marshalValue(buf, off, v.Index(i))
		}
		return off
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. See valueSize for rationale.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := v.Bytes()
			binary.LittleEndian.PutUint32(buf[off:], uint32(len(b)))
			off += 4
			copy(buf[off:], b)
			return off + len(b)
		}
		n := v.Len()
		binary.LittleEndian.PutUint16(buf[off:], uint16(n))
		off += 2
		for i := range n {
			off = marshalValue(buf, off, v.Index(i))
		}
		return off
	case reflect.Struct:
		return marshalStruct(buf, off, v)
	default:
		return off
	}
}

func unmarshalStructOnStage(stage *Stage, data []byte, off int, v reflect.Value) int {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || f.Type == ecsEntityType {
			continue
		}
		if codec := LookupReflectCodec(f.Type); codec != nil {
			codec.Decode(stage, data[off:], v.Field(i))
			off += codec.Size()
			continue
		}
		off = unmarshalValueOnStage(stage, data, off, v.Field(i))
	}
	return off
}

func unmarshalValueOnStage(stage *Stage, data []byte, off int, v reflect.Value) int {
	switch v.Kind() {
	case reflect.Float32:
		v.SetFloat(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))))
		return off + 4
	case reflect.Float64:
		v.SetFloat(math.Float64frombits(binary.LittleEndian.Uint64(data[off:])))
		return off + 8
	case reflect.Uint8:
		v.SetUint(uint64(data[off]))
		return off + 1
	case reflect.Uint16:
		v.SetUint(uint64(binary.LittleEndian.Uint16(data[off:])))
		return off + 2
	case reflect.Uint32:
		v.SetUint(uint64(binary.LittleEndian.Uint32(data[off:])))
		return off + 4
	case reflect.Uint64:
		v.SetUint(binary.LittleEndian.Uint64(data[off:]))
		return off + 8
	case reflect.Int8:
		v.SetInt(int64(int8(data[off])))
		return off + 1
	case reflect.Int16:
		v.SetInt(int64(int16(binary.LittleEndian.Uint16(data[off:]))))
		return off + 2
	case reflect.Int32:
		v.SetInt(int64(int32(binary.LittleEndian.Uint32(data[off:]))))
		return off + 4
	case reflect.Int64:
		v.SetInt(int64(binary.LittleEndian.Uint64(data[off:])))
		return off + 8
	case reflect.Bool:
		v.SetBool(data[off] != 0)
		return off + 1
	case reflect.String:
		slen := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		v.SetString(string(data[off : off+slen]))
		return off + slen
	case reflect.Array:
		for i := range v.Len() {
			off = unmarshalValueOnStage(stage, data, off, v.Index(i))
		}
		return off
	case reflect.Slice:
		// []byte fast path: [u32 len][N bytes]. See valueSize for rationale.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			n := int(binary.LittleEndian.Uint32(data[off:]))
			off += 4
			// Copy out so the decoded slice doesn't alias the caller's
			// data buffer (which the connection layer may reuse).
			out := make([]byte, n)
			copy(out, data[off:off+n])
			v.SetBytes(out)
			return off + n
		}
		n := int(binary.LittleEndian.Uint16(data[off:]))
		off += 2
		// Allocate a fresh slice of the right length so the caller's
		// (possibly-shared, possibly-nil) underlying array is not aliased.
		slice := reflect.MakeSlice(v.Type(), n, n)
		for i := range n {
			off = unmarshalValueOnStage(stage, data, off, slice.Index(i))
		}
		v.Set(slice)
		return off
	case reflect.Struct:
		return unmarshalStructOnStage(stage, data, off, v)
	default:
		return off
	}
}
