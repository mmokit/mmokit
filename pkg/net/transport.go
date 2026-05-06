package net

// Transport is the interface for all network transports (WebSocket, UDP, etc.).
// All messages are complete protobuf frames ([]byte).
type Transport interface {
	// SendReliable sends a message that must be delivered (e.g. login, spawn, state changes).
	SendReliable(data []byte)
	// SendUnreliable sends a message that can be dropped (e.g. world updates).
	SendUnreliable(data []byte)
	// DrainInput returns all queued inbound messages (channel 0x00) and clears the queue.
	DrainInput() [][]byte
	// DrainOpInput returns all queued operation messages (channel 0x01) and clears the queue.
	DrainOpInput() [][]byte
	// DrainClientInput returns all queued typed client-input messages
	// (channel 0x02) and clears the queue. Frames are dispatched per-tick
	// by the gateway path to mmokit.HandleClient handlers.
	DrainClientInput() [][]byte
	// InjectInput appends a message to the channel 0x00 input queue as if it
	// had arrived from the client. Used by the inter-cell forwarding path to
	// replay input on the destination cell after a handoff commit.
	InjectInput(data []byte)
	// Close shuts down the transport.
	Close()
}

// ByteCounter is an optional interface that transports may implement to
// report cumulative bytes sent and received. Used by ConnManager to
// aggregate bandwidth metrics.
type ByteCounter interface {
	BytesSent() uint64
	BytesRecv() uint64
}
