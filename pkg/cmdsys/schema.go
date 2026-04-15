package cmdsys

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"strings"
)

// FieldSchema describes a single exported field in a command Args or Result struct.
type FieldSchema struct {
	Name     string
	Kind     string // "string", "int32", "int64", "float32", "float64", "bool", "[]<elem>", "{...}"
	Required bool   // true when cmd:"required" tag is present
	Default  string // raw string value from cmd:"default=..."
	Enum     []string
}

// Schema is the reflected description of an Args or Result struct.
type Schema struct {
	StructName string
	Fields     []FieldSchema
}

// SchemaOf returns the Schema for the concrete type of v.
// v must be a struct or a pointer to a struct.
// Only exported fields are included. Nested structs are supported one level
// deep; structs nested inside slices or deeper than two levels return an error.
func SchemaOf(v any) (Schema, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return Schema{}, fmt.Errorf("cmdsys.SchemaOf: nil value")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return Schema{}, fmt.Errorf("cmdsys.SchemaOf: expected struct, got %s", t.Kind())
	}
	fields, err := schemaFields(t, 0)
	if err != nil {
		return Schema{}, err
	}
	return Schema{StructName: t.Name(), Fields: fields}, nil
}

func schemaFields(t reflect.Type, depth int) ([]FieldSchema, error) {
	if depth > 1 {
		return nil, fmt.Errorf("cmdsys: struct nesting deeper than 1 level is not supported")
	}
	var fields []FieldSchema
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("cmd")
		fs := FieldSchema{Name: f.Name}
		fs.Required = containsFlag(tag, "required")
		fs.Default = extractTagValue(tag, "default")
		fs.Enum = extractEnum(tag)

		kind, err := typeKind(f.Type, depth)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fs.Kind = kind
		fields = append(fields, fs)
	}
	return fields, nil
}

func typeKind(t reflect.Type, depth int) (string, error) {
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Int32:
		return "int32", nil
	case reflect.Int64:
		return "int64", nil
	case reflect.Float32:
		return "float32", nil
	case reflect.Float64:
		return "float64", nil
	case reflect.Bool:
		return "bool", nil
	case reflect.Slice:
		elem, err := typeKind(t.Elem(), depth)
		if err != nil {
			return "", err
		}
		return "[]" + elem, nil
	case reflect.Struct:
		if depth >= 1 {
			return "", fmt.Errorf("cmdsys: struct nesting deeper than 1 level is not supported")
		}
		sub, err := schemaFields(t, depth+1)
		if err != nil {
			return "", err
		}
		return "{" + encodeFields(sub) + "}", nil
	default:
		return "", fmt.Errorf("cmdsys: unsupported field type %s", t)
	}
}

// schemaHashOf computes a stable FNV-64a hash over the field layout of a
// type. The struct name is intentionally excluded so that two types with
// identical field declarations produce the same hash — enabling the
// dispatcher to detect schema mismatches without requiring the same type name.
func schemaHashOf(v any) (uint64, error) {
	s, err := SchemaOf(v)
	if err != nil {
		return 0, err
	}
	canonical := encodeFields(s.Fields)
	h := fnv.New64a()
	_, _ = h.Write([]byte(canonical))
	return h.Sum64(), nil
}

// encodeFields produces the canonical string for hashing: fieldName:kind, per field.
func encodeFields(fields []FieldSchema) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Name)
		b.WriteByte(':')
		b.WriteString(f.Kind)
		b.WriteByte(',')
	}
	return b.String()
}

// containsFlag reports whether a cmd struct tag contains the given bare flag word.
func containsFlag(tag, flag string) bool {
	for _, part := range strings.Split(tag, ",") {
		if strings.TrimSpace(part) == flag {
			return true
		}
	}
	return false
}

// extractTagValue returns the value from a "key=value" segment in a cmd tag.
func extractTagValue(tag, key string) string {
	prefix := key + "="
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, prefix) {
			return part[len(prefix):]
		}
	}
	return ""
}

// extractEnum parses cmd:"enum=a|b|c" into a string slice.
func extractEnum(tag string) []string {
	raw := extractTagValue(tag, "enum")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "|")
}
