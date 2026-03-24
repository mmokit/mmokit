package engine

// System is the interface all game systems implement.
type System interface {
	Name() string
	Update(dt float32)
}
