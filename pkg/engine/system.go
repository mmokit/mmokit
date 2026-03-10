package engine

// System is the interface all game systems implement.
type System interface {
	Update(dt float32)
}
