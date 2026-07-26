package net

// Transport is the interface for all client network transports (WebSocket,
// UDP, etc.). Messages are complete application frames ([]byte); the
// client-facing protocol is not protobuf.
type Transport interface {
	// SendReliable submits a message through the transport's strongest
	// available delivery path. The result reports the guarantee actually
	// provided; a queued result is transport acceptance, not a remote ACK.
	SendReliable(data []byte) SendResult
	// SendUnreliable submits a message through the transport's hot update path.
	// Ordered transports may still report a stronger delivery class.
	SendUnreliable(data []byte) SendResult
	// DrainInput returns all queued inbound messages (channel 0x00) and clears the queue.
	// Typed client-input frames ride this channel after Plan 1 Phase 5.
	DrainInput() [][]byte
	// DrainOpInput returns all queued operation messages (channel 0x01) and clears the queue.
	DrainOpInput() [][]byte
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
