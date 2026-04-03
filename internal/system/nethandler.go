package system

// lockerInfo tracks the most-progressed entity locking a given target.
type lockerInfo struct {
	netID    uint32
	progress float32
}
