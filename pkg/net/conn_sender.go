package net

// ConnSender is the narrow connection-write surface used by the
// engine hot path. It is satisfied by both the real *ConnManager
// (gateway processes) and by VirtualConnManager (node processes)
// which bridges to remote gateways via MeshData. Gateway-only
// operations (listen, accept, disconnect, byte counters, event channel)
// are NOT part of this interface — callers that need them hold the
// concrete *ConnManager separately.
type ConnSender interface {
	Send(connID uint32, data []byte)
	SendReliable(connID uint32, data []byte)
	InjectInput(connID uint32, data []byte)
	DrainInput(connID uint32) [][]byte
	DrainOpInput(connID uint32) [][]byte
}

var _ ConnSender = (*ConnManager)(nil)
