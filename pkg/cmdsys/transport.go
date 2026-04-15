package cmdsys

import "context"

// RemoteRequest is the wire representation of a command invocation sent to
// a remote target.
type RemoteRequest struct {
	RequestID         uint64
	Verb              string
	ArgsJSON          []byte
	Caller            Caller
	DeadlineUnixNanos int64
	TraceID           string
	SchemaVersion     uint64
}

// RemoteResponse is the wire representation of a command result from a
// remote target.
type RemoteResponse struct {
	RequestID     uint64
	OK            bool
	ResultJSON    []byte
	Error         string
	TargetID      string
	SchemaVersion uint64
}

// Transport sends commands to remote targets and delivers responses.
// Implementations must be goroutine-safe.
// Send returns a channel on which exactly one *RemoteResponse will be sent,
// then the channel is closed. This allows C3 to dispatch concurrently without
// blocking the caller.
type Transport interface {
	Send(ctx context.Context, target Target, req *RemoteRequest) (<-chan *RemoteResponse, error)
	Close() error
}

// InProcTransport is a stub Transport that always returns ErrNotYetWired.
// Replaced by a real implementation in C3.
type InProcTransport struct{}

func (InProcTransport) Send(_ context.Context, _ Target, _ *RemoteRequest) (<-chan *RemoteResponse, error) {
	return nil, ErrNotYetWired
}

func (InProcTransport) Close() error { return nil }
