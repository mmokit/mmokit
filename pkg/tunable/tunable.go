// Package tunable is the zero-dependency contract for runtime-tunable system
// values. It compiles for both native and GOOS=wasip1 so the host and the wasm
// guest parse the `tune:` struct tag identically. A tunable is an exported
// scalar struct field tagged `tune:"default=...,min=...,max=...,step=..."`.
package tunable

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind enumerates the supported tunable scalar kinds. Every kind round-trips
// losslessly through a float64 (the wasm value-transport unit).
type Kind uint8

const (
	KindInt Kind = iota
	KindUint
	KindFloat
	KindBool
)

func (k Kind) String() string {
	switch k {
	case KindInt:
		return "int"
	case KindUint:
		return "uint"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	default:
		return "?"
	}
}

// Def describes one tunable: its identity, kind, optional bounds (empty string
// = unset), and current stringified value.
type Def struct {
	Name    string
	Kind    Kind
	Default string
	Min     string
	Max     string
	Step    string
	Value   string
}

// Tag is the parsed contents of a `tune:"..."` struct tag.
type Tag struct {
	Default string
	Min     string
	Max     string
	Step    string
}

// ParseTag parses a tune-tag body ("default=1,min=0,max=2"). The bool is false
// for an empty body (the field is not a tunable).
func ParseTag(body string) (Tag, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Tag{}, false
	}
	var t Tag
	for _, part := range strings.Split(body, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		switch key {
		case "default":
			t.Default = val
		case "min":
			t.Min = val
		case "max":
			t.Max = val
		case "step":
			t.Step = val
		}
	}
	return t, true
}

// ToF64 parses a stringified value of the given kind into a float64 for the
// wasm value block. bool → 0/1.
func ToF64(kind Kind, value string) (float64, error) {
	switch kind {
	case KindInt:
		n, err := strconv.ParseInt(value, 10, 64)
		return float64(n), err
	case KindUint:
		n, err := strconv.ParseUint(value, 10, 64)
		return float64(n), err
	case KindFloat:
		return strconv.ParseFloat(value, 64)
	case KindBool:
		b, err := strconv.ParseBool(value)
		if b {
			return 1, err
		}
		return 0, err
	default:
		return 0, fmt.Errorf("tunable: unknown kind %v", kind)
	}
}

// FromF64 renders a float64 (as carried in the wasm block) back to the kind's
// canonical string form.
func FromF64(kind Kind, v float64) string {
	switch kind {
	case KindInt:
		return strconv.FormatInt(int64(v), 10)
	case KindUint:
		return strconv.FormatUint(uint64(v), 10)
	case KindBool:
		if v != 0 {
			return "true"
		}
		return "false"
	default: // KindFloat
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
}

// Validate checks that value parses for the Def's kind and falls within
// [Min,Max] when those bounds are set.
func (d Def) Validate(value string) error {
	v, err := ToF64(d.Kind, value)
	if err != nil {
		return fmt.Errorf("invalid %s value %q: %w", d.Kind, value, err)
	}
	if d.Min != "" {
		lo, err := ToF64(d.Kind, d.Min)
		if err == nil && v < lo {
			return fmt.Errorf("%s=%s below min %s", d.Name, value, d.Min)
		}
	}
	if d.Max != "" {
		hi, err := ToF64(d.Kind, d.Max)
		if err == nil && v > hi {
			return fmt.Errorf("%s=%s above max %s", d.Name, value, d.Max)
		}
	}
	return nil
}

// Source is the host-side contract for anything exposing tunables — a native
// struct (via Reflect) or the wasm adapter.
type Source interface {
	Tunables() []Def
	Set(name, value string) error
}
