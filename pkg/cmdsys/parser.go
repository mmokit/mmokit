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

	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if strings.HasPrefix(tok, "--") {
			key, val, cut := strings.Cut(tok[2:], "=")
			if !cut {
				// --name value form
				if i+1 >= len(tokens) {
					return nil, fmt.Errorf("parse: flag --%s has no value", key)
				}
				i++
				val = tokens[i]
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
			rawVal = positional[posIdx]
			posIdx++
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
		return raw, nil
	}
}

// ApplyMap sets fields on a struct pointer from the map[string]any produced
// by Bind. Keys are Go field names.
func ApplyMap(dst any, m map[string]any) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
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
