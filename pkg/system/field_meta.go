package system

import (
	"fmt"
	"reflect"
	"strings"
)

// fieldMeta holds parsed metadata from a single `net:"..."` struct tag.
type fieldMeta struct {
	encoding string            // e.g. "qnorm", "u16", "qvel", "string"
	options  map[string]string // e.g. {"scale": "2000"}
	initial  bool              // true if tagged `initial,...`
	wireSize int               // bytes on wire (-1 for variable like string)
}

// encodingWireSize maps encoding names to their fixed wire size.
// -1 means variable length.
var encodingWireSize = map[string]int{
	"f32":    4,
	"qvel":   2, // int16
	"qangle": 2, // uint16
	"qnorm":  1, // uint8
	"qsize":  2, // uint16
	"u8":     1,
	"u16":    2,
	"u32":    4,
	"i16":    2,
	"bool":   1,
	"string": -1,
}

// parseNetTag parses a `net:"..."` tag value into fieldMeta.
// Format: "encoding[,option=value]..." or "initial,encoding[,option=value]..."
// If encoding is omitted (e.g. `net:"initial"`), it is inferred from goKind.
func parseNetTag(tag string, goKind ...reflect.Kind) (fieldMeta, error) {
	parts := strings.Split(tag, ",")
	fm := fieldMeta{options: make(map[string]string)}

	idx := 0
	if len(parts) > 0 && parts[0] == "initial" {
		fm.initial = true
		idx = 1
	}

	// If no encoding part remains (or "auto"), infer from Go type.
	if idx >= len(parts) || parts[idx] == "" || parts[idx] == "auto" {
		if len(goKind) == 0 {
			return fm, fmt.Errorf("net tag missing encoding: %q", tag)
		}
		enc, ok := inferEncoding(goKind[0])
		if !ok {
			return fm, fmt.Errorf("net tag missing encoding and cannot infer from Go type %v: %q", goKind[0], tag)
		}
		fm.encoding = enc
		fm.wireSize = encodingWireSize[enc]
		return fm, nil
	}

	enc := parts[idx]
	size, ok := encodingWireSize[enc]
	if !ok {
		// Maybe it's a key=value option and encoding should be inferred.
		if len(goKind) > 0 && strings.Contains(enc, "=") {
			inferred, inferOk := inferEncoding(goKind[0])
			if inferOk {
				fm.encoding = inferred
				fm.wireSize = encodingWireSize[inferred]
				// Parse this and remaining parts as options.
				for i := idx; i < len(parts); i++ {
					kv := strings.SplitN(parts[i], "=", 2)
					if len(kv) == 2 {
						fm.options[kv[0]] = kv[1]
					}
				}
				return fm, nil
			}
		}
		return fm, fmt.Errorf("unknown net encoding %q in tag %q", enc, tag)
	}
	fm.encoding = enc
	fm.wireSize = size

	// Parse remaining key=value options.
	for i := idx + 1; i < len(parts); i++ {
		kv := strings.SplitN(parts[i], "=", 2)
		if len(kv) == 2 {
			fm.options[kv[0]] = kv[1]
		}
	}

	return fm, nil
}

// inferEncoding returns the default wire encoding for a Go reflect.Kind.
// Returns false for types with multiple valid encodings (e.g. float32).
func inferEncoding(k reflect.Kind) (string, bool) {
	switch k {
	case reflect.String:
		return "string", true
	case reflect.Uint8:
		return "u8", true
	case reflect.Uint16:
		return "u16", true
	case reflect.Uint32:
		return "u32", true
	case reflect.Int16:
		return "i16", true
	case reflect.Bool:
		return "bool", true
	default:
		return "", false
	}
}
