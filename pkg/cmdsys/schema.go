package cmdsys

import (
	"fmt"
	"hash/fnv"
	"reflect"
	"strings"
)

// FieldSchema describes a single exported field in a command Args or Result struct.
type FieldSchema struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // "string", "int32", "int64", "float32", "float64", "bool", "[]<elem>", "{...}"
	Required  bool     `json:"required"`
	NamedOnly bool     `json:"named_only"`
	Default   string   `json:"default"`
	Enum      []string `json:"enum"`
	Help      string   `json:"help,omitempty"`
	// Rest reports whether this field consumes all remaining positional
	// tokens joined by a single space. Only valid on string fields; must
	// be the last positional field in the struct.
	Rest bool `json:"rest,omitempty"`
	// Complete names a dynamic completion source key registered with the
	// console (e.g. "players", "hosts", "cells"). Tab completion looks up
	// the current values via Console.completions[Complete]. Empty means
	// no dynamic completion for this field.
	Complete string `json:"complete,omitempty"`
	// Secret marks the field as a sensitive value that should not be
	// echoed or stored in shell history. The console adapter prompts
	// for these fields via TTY (no echo) instead of accepting them
	// from CLI tokens; the HTTP/admin-UI path passes them in the JSON
	// request body and surfaces a password-type form field via
	// ArgsModal.svelte. Mark with cmd:"secret".
	Secret bool `json:"secret,omitempty"`
}

// Schema is the reflected description of an Args or Result struct.
type Schema struct {
	StructName string        `json:"struct"`
	Fields     []FieldSchema `json:"fields"`
}

// SchemaOf returns the Schema for the concrete type of v, using the Args
// depth limit (1 level). Kept unchanged to preserve Args semantics — the
// parser relies on the flat shape.
// v must be a struct or a pointer to a struct.
// Only exported fields are included. Nested structs are supported one level
// deep; structs nested inside slices or deeper than two levels return an error.
func SchemaOf(v any) (Schema, error) {
	return schemaOf(v, 1)
}

// SchemaOfResult returns the Schema for the concrete type of v, allowing
// arbitrary struct nesting. Result types are always JSON-marshaled, so
// depth isn't a correctness concern. Used by the registry when hashing
// Result type layouts.
func SchemaOfResult(v any) (Schema, error) {
	return schemaOf(v, maxStructDepthUnlimited)
}

// maxStructDepthUnlimited is passed to schemaFields/typeKind to disable the
// nesting depth limit for Result types.
const maxStructDepthUnlimited = -1

func schemaOf(v any, maxDepth int) (Schema, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return Schema{}, fmt.Errorf("cmdsys.SchemaOf: nil value")
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return Schema{}, fmt.Errorf("cmdsys.SchemaOf: expected struct, got %s", t.Kind())
	}
	fields, err := schemaFields(t, 0, maxDepth)
	if err != nil {
		return Schema{}, err
	}
	return Schema{StructName: t.Name(), Fields: fields}, nil
}

func schemaFields(t reflect.Type, depth int, maxDepth int) ([]FieldSchema, error) {
	var fields []FieldSchema
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("cmd")
		fs := FieldSchema{Name: f.Name}
		// Fields are required by default. cmd:"optional" opts out.
		fs.Required = !containsFlag(tag, "optional")
		fs.NamedOnly = containsFlag(tag, "named-only")
		fs.Rest = containsFlag(tag, "rest")
		fs.Secret = containsFlag(tag, "secret")
		fs.Default = extractTagValue(tag, "default")
		fs.Enum = extractEnum(tag)
		fs.Help = extractTagValue(tag, "help")
		fs.Complete = extractTagValue(tag, "complete")

		kind, err := typeKind(f.Type, depth, maxDepth)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.Name, err)
		}
		fs.Kind = kind
		fields = append(fields, fs)
	}
	return fields, nil
}

func typeKind(t reflect.Type, depth int, maxDepth int) (string, error) {
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Int:
		return "int", nil
	case reflect.Int32:
		return "int32", nil
	case reflect.Int64:
		return "int64", nil
	case reflect.Uint32:
		return "uint32", nil
	case reflect.Uint64:
		return "uint64", nil
	case reflect.Float32:
		return "float32", nil
	case reflect.Float64:
		return "float64", nil
	case reflect.Bool:
		return "bool", nil
	case reflect.Slice:
		elem, err := typeKind(t.Elem(), depth, maxDepth)
		if err != nil {
			return "", err
		}
		return "[]" + elem, nil
	case reflect.Struct:
		if maxDepth != maxStructDepthUnlimited && depth >= maxDepth {
			return "", fmt.Errorf("cmdsys: struct nesting deeper than %d level is not supported", maxDepth)
		}
		sub, err := schemaFields(t, depth+1, maxDepth)
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
// Uses the Args depth limit (1 level).
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

// schemaHashOfResult computes the schema hash for a Result type, allowing
// arbitrary nested struct depth.
func schemaHashOfResult(v any) (uint64, error) {
	s, err := SchemaOfResult(v)
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
