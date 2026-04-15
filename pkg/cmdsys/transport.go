package cmdsys

import "context"

// RemoteRequest is the wire representation of a command invocation sent to
// a remote target.
type RemoteRequest struct {
	TraceID string
	Verb    string
	Caller  Caller
	Target  Target
	Args    []byte // JSON-encoded args
}

// RemoteResponse is the wire representation of a command result from a
// remote target.
type RemoteResponse struct {
	TraceID  string
	TargetID string
	OK       bool
	Result   []byte // JSON-encoded result
	Error    string
}

// Transport sends commands to remote targets and delivers responses.
// Implementations must be goroutine-safe.
type Transport interface {
	Send(ctx context.Context, req RemoteRequest) (RemoteResponse, error)
	Close() error
}

// InProcTransport is a stub Transport that always returns ErrNotYetWired.
// Replaced by a real implementation in C3.
type InProcTransport struct{}

func (InProcTransport) Send(_ context.Context, _ RemoteRequest) (RemoteResponse, error) {
	return RemoteResponse{}, ErrNotYetWired
}

func (InProcTransport) Close() error { return nil }
