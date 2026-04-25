package mmokit

import (
	"reflect"
	"strings"
)

// NewSystem builds a SystemDef for a custom game system from a pointer to a
// zero-value struct. The pointer is used only for type information — fresh
// zero-values are constructed per cell. Field state on the prototype is
// ignored. Pass nil-free struct fields and run setup work in Init().
//
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}))
//	mmo.AddSystem(mmokit.NewSystem(&BotSystem{}).Named("AILogic"))
//
// The system name defaults to the struct type name with a trailing "System"
// suffix stripped (e.g. *BotSystem → "Bot"). Override with .Named() when the
// default doesn't fit — typically when registering multiple instances of the
// same type.
//
// For systems with constructor arguments (closures, channels, prebuilt
// dependencies), write a typed factory function that returns a SystemDef
// directly, like the built-in NewSpatialSystemWith / NewInputSystem helpers.
func NewSystem(s System) SystemDef {
	t := reflect.TypeOf(s)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		panic("mmokit.NewSystem: argument must be a non-nil pointer to a struct")
	}
	elem := t.Elem()
	return SystemDef{
		Name: strings.TrimSuffix(elem.Name(), "System"),
		Factory: func() System {
			return reflect.New(elem).Interface().(System)
		},
	}
}
