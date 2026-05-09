package cmdsys

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
)

// coerceArgs converts raw into the concrete args type expected by cmd.
//
// raw may be:
//   - string: parsed via Parser then reflect-set onto the command's Args zero value
//   - []byte: JSON-unmarshaled into the command's Args type
//   - struct value/pointer assignable to the command's Args type: used directly
func (d *Dispatcher) coerceArgs(cmd Command, raw any) (any, error) {
	if cmd.Args == nil {
		return nil, nil
	}

	argsType := reflect.TypeOf(cmd.Args)
	if argsType.Kind() == reflect.Pointer {
		argsType = argsType.Elem()
	}
	argsPtr := reflect.New(argsType)

	switch v := raw.(type) {
	case string:
		schema, err := SchemaOf(cmd.Args)
		if err != nil {
			return nil, fmt.Errorf("args schema: %w", err)
		}
		var p Parser
		m, err := p.Bind(v, schema)
		if err != nil {
			return nil, err
		}
		if err := ApplyMap(argsPtr.Interface(), m); err != nil {
			return nil, err
		}
	case []byte:
		if err := json.Unmarshal(v, argsPtr.Interface()); err != nil {
			return nil, fmt.Errorf("parse_json: %w", err)
		}
	default:
		// Direct struct assignment: check type compatibility.
		rv := reflect.ValueOf(raw)
		if rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		if rv.Type() == argsType {
			argsPtr.Elem().Set(rv)
		} else {
			return nil, fmt.Errorf("cmdsys: cannot coerce %T to %s", raw, argsType)
		}
	}

	return argsPtr.Elem().Interface(), nil
}

// NewArgs returns a fresh pointer to a zero-valued args struct of the type
// registered with the command. Callers (e.g. HTTP/console adapters that
// receive args as JSON) decode the request body into the returned pointer
// before calling Dispatcher.Invoke. Returns nil + nil error when the
// command takes no args (cmd.Args == nil).
func NewArgs(c Command) (any, error) {
	if c.Args == nil {
		return nil, nil
	}
	t := reflect.TypeOf(c.Args)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return reflect.New(t).Interface(), nil
}

// newTraceID returns a 16-character hex trace ID from 8 random bytes.
func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
