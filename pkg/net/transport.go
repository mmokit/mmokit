package net

// Transport is the interface for all network transports (WebSocket, UDP, etc.).
// All messages are complete protobuf frames ([]byte).
type Transport interface {
	// SendReliable sends a message that must be delivered (e.g. login, death, spawn).
	SendReliable(data []byte)
	// SendUnreliable sends a message that can be dropped (e.g. world updates).
	SendUnreliable(data []byte)
	// DrainInput returns all queued inbound messages and clears the queue.
	DrainInput() [][]byte
	// Close shuts down the transport.
	Close()
}
