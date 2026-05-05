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
// Call this at registration time to catch problems early.
func ValidateComponentType(t reflect.Type) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("reflect_marshal: expected struct, got %s", t.Kind()))
	}
	validateStruct(t, "")
}

func validateStruct(t reflect.Type, path string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fpath := path + "." + f.Name
		if f.Type == ecsEntityType {
			continue // skipped during marshal
		}
		validateType(f.Type, fpath)
	}
}

func validateType(t reflect.Type, path string) {
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
		validateType(t.Elem(), path+"[]")
	case reflect.Struct:
		validateStruct(t, path)
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
	case reflect.Struct:
		return unmarshalStructOnStage(stage, data, off, v)
	default:
		return off
	}
}
