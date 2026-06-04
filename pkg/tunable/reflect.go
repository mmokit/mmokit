package tunable

import (
	"fmt"
	"reflect"
)

// field binds a Def to the reflect index of the live struct field.
type field struct {
	def   Def
	index int
}

type reflectSource struct {
	v      reflect.Value // the addressable struct value
	fields []field
}

// kindOf maps a reflect.Kind to a tunable.Kind, reporting false for
// unsupported kinds (so they're skipped rather than panicking).
func kindOf(k reflect.Kind) (Kind, bool) {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return KindInt, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return KindUint, true
	case reflect.Float32, reflect.Float64:
		return KindFloat, true
	case reflect.Bool:
		return KindBool, true
	default:
		return 0, false
	}
}

// scan walks ptr's struct fields collecting tagged, exported, scalar tunables.
func scan(ptr any) (reflect.Value, []field) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		panic(fmt.Sprintf("tunable: Reflect requires a struct pointer, got %T", ptr))
	}
	elem := rv.Elem()
	t := elem.Type()
	var fields []field
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		body, ok := sf.Tag.Lookup("tune")
		if !ok {
			continue
		}
		tag, present := ParseTag(body)
		if !present {
			continue
		}
		kind, ok := kindOf(sf.Type.Kind())
		if !ok {
			continue
		}
		fields = append(fields, field{
			def: Def{
				Name: sf.Name, Kind: kind,
				Default: tag.Default, Min: tag.Min, Max: tag.Max, Step: tag.Step,
			},
			index: i,
		})
	}
	return elem, fields
}

// HasTunables reports whether ptr (a struct pointer) has any tune-tagged fields.
func HasTunables(ptr any) bool {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return false
	}
	_, fields := scan(ptr)
	return len(fields) > 0
}

// Reflect wraps a struct pointer as a Source over its tune-tagged fields.
func Reflect(ptr any) Source {
	v, fields := scan(ptr)
	return &reflectSource{v: v, fields: fields}
}

func (r *reflectSource) Tunables() []Def {
	out := make([]Def, len(r.fields))
	for i, f := range r.fields {
		d := f.def
		d.Value = r.read(f)
		out[i] = d
	}
	return out
}

func (r *reflectSource) Set(name, value string) error {
	for _, f := range r.fields {
		if f.def.Name != name {
			continue
		}
		if err := f.def.Validate(value); err != nil {
			return err
		}
		return r.write(f, value)
	}
	return fmt.Errorf("tunable: unknown field %q", name)
}

// ApplyDefaults writes each field's tag default (skipping fields without one).
func (r *reflectSource) ApplyDefaults() {
	for _, f := range r.fields {
		if f.def.Default == "" {
			continue
		}
		_ = r.write(f, f.def.Default)
	}
}

func (r *reflectSource) read(f field) string {
	fv := r.v.Field(f.index)
	switch f.def.Kind {
	case KindInt:
		return FromF64(KindInt, float64(fv.Int()))
	case KindUint:
		return FromF64(KindUint, float64(fv.Uint()))
	case KindBool:
		return FromF64(KindBool, boolF(fv.Bool()))
	default:
		return FromF64(KindFloat, fv.Float())
	}
}

func (r *reflectSource) write(f field, value string) error {
	v, err := ToF64(f.def.Kind, value)
	if err != nil {
		return err
	}
	fv := r.v.Field(f.index)
	switch f.def.Kind {
	case KindInt:
		fv.SetInt(int64(v))
	case KindUint:
		fv.SetUint(uint64(v))
	case KindBool:
		fv.SetBool(v != 0)
	default:
		fv.SetFloat(v)
	}
	return nil
}

func boolF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
