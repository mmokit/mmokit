package engine

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Configurable provides read/write access to a configuration struct's fields.
// Used by the built-in "config" command group for generic get/set/list.
type Configurable interface {
	Fields() []string
	GetField(name string) (string, error)
	SetField(name, value string) error
}

// ReflectConfig wraps any struct pointer as a Configurable using reflection.
type ReflectConfig struct {
	ptr any
}

// NewReflectConfig returns a ReflectConfig wrapping the given struct pointer.
func NewReflectConfig(ptr any) *ReflectConfig {
	return &ReflectConfig{ptr: ptr}
}

// Fields returns all field names in declaration order.
func (c *ReflectConfig) Fields() []string {
	t := reflect.TypeOf(c.ptr).Elem()
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		names = append(names, t.Field(i).Name)
	}
	return names
}

// GetField performs a case-insensitive field lookup and returns the stringified value.
func (c *ReflectConfig) GetField(name string) (string, error) {
	v := reflect.ValueOf(c.ptr).Elem()
	f := v.FieldByNameFunc(func(n string) bool {
		return strings.EqualFold(n, name)
	})
	if !f.IsValid() {
		return "", fmt.Errorf("unknown config field: %s", name)
	}
	return fmt.Sprintf("%v", f.Interface()), nil
}

// SetField performs a case-insensitive field lookup and sets the field using
// type-aware parsing for int, float32, float64, string, and bool kinds.
func (c *ReflectConfig) SetField(name, value string) error {
	v := reflect.ValueOf(c.ptr).Elem()
	f := v.FieldByNameFunc(func(n string) bool {
		return strings.EqualFold(n, name)
	})
	if !f.IsValid() {
		return fmt.Errorf("unknown config field: %s", name)
	}
	if !f.CanSet() {
		return fmt.Errorf("cannot set field: %s", name)
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int: %s", value)
		}
		f.SetInt(n)
	case reflect.Uint, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid uint: %s", value)
		}
		f.SetUint(n)
	case reflect.Float32:
		n, err := strconv.ParseFloat(value, 32)
		if err != nil {
			return fmt.Errorf("invalid float: %s", value)
		}
		f.SetFloat(n)
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float: %s", value)
		}
		f.SetFloat(n)
	case reflect.String:
		f.SetString(value)
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool: %s", value)
		}
		f.SetBool(b)
	default:
		return fmt.Errorf("unsupported field type: %s (%s)", name, f.Kind())
	}
	return nil
}
