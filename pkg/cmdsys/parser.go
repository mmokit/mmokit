package cmdsys

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Parser converts raw text input into a map[string]any keyed by Go field
// names, validated against a Schema.
type Parser struct{}

// Bind parses raw into a map keyed by Go field names as defined in schema.
// Supports:
//   - positional values in field declaration order
//   - --name=value and --name value flag forms
//   - double-quoted strings with \" and \\ escapes
//
// Defaults from cmd:"default=..." are applied when the caller omits a field.
// Missing required fields and unknown named arguments are errors.
func (p *Parser) Bind(raw string, schema Schema) (map[string]any, error) {
	tokens, err := tokenize(raw)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	named := map[string]string{}  // field name → raw value string
	positional := []string{}

	// boolField reports whether the named field (by Name or lowercase) is
	// declared as a bool — bare --foo means --foo=true on bool fields.
	boolField := func(key string) bool {
		for _, f := range schema.Fields {
			if f.Name == key || strings.EqualFold(f.Name, key) {
				return f.Kind == "bool"
			}
		}
		return false
	}
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if strings.HasPrefix(tok, "--") {
			key, val, cut := strings.Cut(tok[2:], "=")
			if !cut {
				switch {
				case boolField(key):
					// Bare --flag on a bool field is implicitly true.
					val = "true"
				case i+1 >= len(tokens):
					return nil, fmt.Errorf("parse: flag --%s has no value", key)
				default:
					i++
					val = tokens[i]
				}
			}
			named[key] = val
		} else {
			positional = append(positional, tok)
		}
	}

	// build field name lookup by index for positional
	result := make(map[string]any, len(schema.Fields))
	posIdx := 0

	for _, f := range schema.Fields {
		fieldNameLower := strings.ToLower(f.Name)

		var rawVal string
		var supplied bool

		if v, ok := named[f.Name]; ok {
			rawVal = v
			supplied = true
			delete(named, f.Name)
		} else if v, ok := named[fieldNameLower]; ok {
			rawVal = v
			supplied = true
			delete(named, fieldNameLower)
		} else if !f.NamedOnly && posIdx < len(positional) {
			if f.Rest {
				// Consume all remaining positional tokens joined by spaces.
				rawVal = strings.Join(positional[posIdx:], " ")
				posIdx = len(positional)
			} else {
				rawVal = positional[posIdx]
				posIdx++
			}
			supplied = true
		}

		if !supplied {
			if f.Default != "" {
				rawVal = f.Default
				supplied = true
			}
		}

		if !supplied {
			if f.Required {
				return nil, fmt.Errorf("parse: missing required field %q", f.Name)
			}
			continue
		}

		val, err := convertField(rawVal, f)
		if err != nil {
			return nil, fmt.Errorf("parse_%s: %w", f.Name, err)
		}
		result[f.Name] = val
	}

	// any leftover named args are unknown
	for k := range named {
		return nil, fmt.Errorf("parse: unknown flag --%s", k)
	}

	return result, nil
}

func convertField(raw string, f FieldSchema) (any, error) {
	if len(f.Enum) > 0 {
		for _, v := range f.Enum {
			if raw == v {
				return raw, nil
			}
		}
		return nil, fmt.Errorf("value %q is not a valid enum value for field %s (allowed: %s)",
			raw, f.Name, strings.Join(f.Enum, ", "))
	}
	switch f.Kind {
	case "string":
		return raw, nil
	case "int":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		return int(n), nil
	case "int32":
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return int32(n), nil
	case "int64":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		return n, nil
	case "uint32":
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return nil, err
		}
		return uint32(n), nil
	case "uint64":
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, err
		}
		return n, nil
	case "float32":
		f64, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return nil, err
		}
		return float32(f64), nil
	case "float64":
		f64, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, err
		}
		return f64, nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, err
		}
		return b, nil
	default:
		// Slice and nested-struct fields cannot be bound from positional or
		// named text args in C1. Mark the field cmd:"named-only" and use
		// --name=value if a future phase adds multi-value parsing.
		// TODO(C3): add multi-value parsing for slices and nested structs.
		return nil, fmt.Errorf("cmdsys: field %q kind %q cannot be bound from positional args (mark cmd:\"named-only\" and use --name=value)", f.Name, f.Kind)
	}
}

// ApplyMap sets fields on a struct pointer from the map[string]any produced
// by Bind. Keys are Go field names.
func ApplyMap(dst any, m map[string]any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("cmdsys.ApplyMap: dst must be a pointer to a struct")
	}
	rv = rv.Elem()
	rt := rv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		val, ok := m[f.Name]
		if !ok {
			continue
		}
		fv := rv.Field(i)
		sv := reflect.ValueOf(val)
		if !sv.Type().AssignableTo(fv.Type()) {
			if sv.Type().ConvertibleTo(fv.Type()) {
				fv.Set(sv.Convert(fv.Type()))
				continue
			}
			return fmt.Errorf("cmdsys.ApplyMap: cannot assign %T to %s.%s (%s)", val, rt.Name(), f.Name, fv.Type())
		}
		fv.Set(sv)
	}
	return nil
}

// tokenize splits raw into tokens. Whitespace separates tokens; double-quoted
// strings are treated as single tokens with \" and \\ escape sequences.
func tokenize(raw string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false

	i := 0
	runes := []rune(raw)
	for i < len(runes) {
		ch := runes[i]
		if inQuote {
			if ch == '\\' && i+1 < len(runes) {
				next := runes[i+1]
				switch next {
				case '"':
					cur.WriteRune('"')
				case '\\':
					cur.WriteRune('\\')
				default:
					cur.WriteRune('\\')
					cur.WriteRune(next)
				}
				i += 2
				continue
			}
			if ch == '"' {
				inQuote = false
				i++
				continue
			}
			cur.WriteRune(ch)
		} else {
			if ch == '"' {
				inQuote = true
				i++
				continue
			}
			if ch == ' ' || ch == '\t' {
				if cur.Len() > 0 {
					tokens = append(tokens, cur.String())
					cur.Reset()
				}
				i++
				continue
			}
			cur.WriteRune(ch)
		}
		i++
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}
