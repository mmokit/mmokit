package universe

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// FieldInfo describes one inspectable field on a component.
type FieldInfo struct {
	Path     string // dotted path within the component, e.g. "Current" or "Nested.Max"
	Type     string // human type, e.g. "int32", "float32", "bool", "string", "[]string"
	Value    string // rendered value (scalars: literal; non-scalar: JSON)
	Editable bool   // true for coercible scalar leaves
}

// ListFields reflects over component (a pointer to a struct) and returns one
// FieldInfo per exported scalar leaf — recursing through nested structs and
// non-nil struct pointers — plus one read-only entry per slice/map/array field
// rendered as JSON. Unexported fields are skipped.
func ListFields(component any) []FieldInfo {
	v := reflect.ValueOf(component)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	var out []FieldInfo
	walkFields(v, "", &out)
	return out
}

func walkFields(v reflect.Value, prefix string, out *[]FieldInfo) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" { // unexported
			continue
		}
		fv := v.Field(i)
		path := sf.Name
		if prefix != "" {
			path = prefix + "." + sf.Name
		}
		// Deref non-nil struct pointers.
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				*out = append(*out, FieldInfo{Path: path, Type: fv.Type().String(), Value: "null", Editable: false})
				continue
			}
			fv = fv.Elem()
		}
		switch fv.Kind() {
		case reflect.Bool, reflect.String,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			*out = append(*out, FieldInfo{
				Path:     path,
				Type:     fv.Type().String(),
				Value:    renderScalar(fv),
				Editable: true,
			})
		case reflect.Struct:
			walkFields(fv, path, out)
		default: // slice, map, array, chan, func, interface, complex
			*out = append(*out, FieldInfo{
				Path:     path,
				Type:     fv.Type().String(),
				Value:    renderJSON(fv),
				Editable: false,
			})
		}
	}
}

func renderScalar(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32:
		return strconv.FormatFloat(v.Float(), 'g', -1, 32)
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.String:
		return v.String()
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

func renderJSON(v reflect.Value) string {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		return fmt.Sprintf("%v", v.Interface())
	}
	return string(b)
}

// SetFieldByPath walks the dotted path to a settable scalar leaf on component
// (a pointer to a struct), coerces strVal to the leaf's kind, sets it, and
// returns the rendered old and new values. Errors on unknown path, non-scalar /
// read-only leaf, or uncoercible value.
func SetFieldByPath(component any, path, strVal string) (oldStr, newStr string, err error) {
	v := reflect.ValueOf(component)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return "", "", fmt.Errorf("component must be a non-nil pointer")
	}
	v = v.Elem()
	segs := strings.Split(path, ".")
	for i, seg := range segs {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return "", "", fmt.Errorf("nil pointer at %q", strings.Join(segs[:i], "."))
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return "", "", fmt.Errorf("%q is not a struct", strings.Join(segs[:i], "."))
		}
		f := v.FieldByName(seg)
		if !f.IsValid() {
			return "", "", fmt.Errorf("unknown field %q", seg)
		}
		if !f.CanSet() {
			return "", "", fmt.Errorf("field %q is not settable (unexported?)", seg)
		}
		v = f
	}
	// v is the leaf.
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", "", fmt.Errorf("cannot set nil pointer leaf %q", path)
		}
		v = v.Elem()
	}
	oldStr = renderScalar(v)
	if err := coerceInto(v, strVal); err != nil {
		return "", "", err
	}
	return oldStr, renderScalar(v), nil
}

func coerceInto(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("cannot parse %q as bool", s)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(s, 10, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return fmt.Errorf("cannot parse %q as %s", s, v.Type())
		}
		v.SetFloat(f)
	case reflect.String:
		v.SetString(s)
	default:
		return fmt.Errorf("field of kind %s is not editable", v.Kind())
	}
	return nil
}
