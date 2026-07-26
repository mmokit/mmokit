package net

// ConnSender is the narrow connection-write surface used by the
// engine hot path. It is satisfied by both the real *ConnManager
// (gateway processes) and by VirtualConnManager (T6+, node processes)
// which bridges to remote gateways via MeshData. Gateway-only
// operations (listen, accept, disconnect, byte counters, event channel)
// are NOT part of this interface — callers that need them hold the
// concrete *ConnManager separately.
type ConnSender interface {
	Send(connID uint32, data []byte) SendResult
	SendReliable(connID uint32, data []byte) SendResult
	InjectInput(connID uint32, data []byte)
	DrainInput(connID uint32) [][]byte
	DrainOpInput(connID uint32) [][]byte
}

// TrackedReplicationSender is an optional extension implemented by connection
// routers that can correlate an asynchronously-confirmed transport admission
// with a replication frame sequence. SendReplication has the same immediate
// ownership semantics as ConnSender.Send; a queued result is not itself the
// downstream receipt.
type TrackedReplicationSender interface {
	SendReplication(connID uint32, scope uint64, streamEpoch, seq uint32, data []byte) SendResult
}

// ReplicationReceipt reports the final downstream enqueue outcome for one
// tracked replication attempt. ConnID is the sender-local connection ID,
// Scope identifies the frame writer, while StreamEpoch and Seq identify the
// exact replication stream attempt supplied to SendReplication. Both are
// required because Seq may restart at one after an authority change while a
// receipt from the previous stream is still in flight.
type ReplicationReceipt struct {
	ConnID      uint32
	Scope       uint64
	StreamEpoch uint32
	Seq         uint32
	Result      SendResult
}

// ReplicationReceiptSource is an optional extension paired with
// TrackedReplicationSender. Receipts are drained by connection and writer
// scope because one ConnSender and local connection can be shared by source
// and destination cells during an authority handoff.
type ReplicationReceiptSource interface {
	DrainReplicationReceipts(connID uint32, scope uint64) []ReplicationReceipt
}

var _ ConnSender = (*ConnManager)(nil)
