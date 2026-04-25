package mmokit

// AddSystem registers a system by its zero-valued type T. Equivalent to the
// AddSystem(name, func() System { return &T{} }) pattern that examples
// otherwise write inline. Use:
//
//	mmokit.AddSystem[BotSystem](mmo, "Bots")
//
// instead of:
//
//	mmo.AddSystem("Bots", func() mmokit.System { return &BotSystem{} })
//
// PT is the constraint that *T satisfies the System interface — Go infers it
// from the explicit T parameter, so call sites only spell T.
//
// Systems that require constructor arguments (e.g. a setup closure or a
// non-zero channel) must still use the func-literal form; AddSystem[T] is
// only safe when *T's Init() is self-contained — all setup happens at
// Init() time, not at construction.
func AddSystem[T any, PT interface {
	*T
	System
}](mmo *Process, name string) {
	mmo.AddSystem(name, func() System {
		var t T
		return PT(&t)
	})
}
