package universe

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	meshpb "github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// commandOrchestrator tracks in-flight cross-process command requests.
// When a response arrives from the remote side, OnResponse delivers it to
// the waiting caller via the per-request channel.
type commandOrchestrator struct {
	mu       sync.Mutex
	nextID   atomic.Uint64
	inflight map[uint64]*inflightCmd
}

type inflightCmd struct {
	id      uint64
	resp    chan *cmdsys.RemoteResponse
	cancel  context.CancelFunc // cancels the handler context on the remote side when called
	started time.Time
}

func newCommandOrchestrator() *commandOrchestrator {
	return &commandOrchestrator{
		inflight: make(map[uint64]*inflightCmd),
	}
}

// alloc registers a new in-flight request and returns its ID + response channel.
// The cancel func is stored so OnCancel can cancel the handler context on the
// remote if the caller's ctx is cancelled.
func (o *commandOrchestrator) alloc(cancel context.CancelFunc) (uint64, chan *cmdsys.RemoteResponse) {
	id := o.nextID.Add(1)
	ch := make(chan *cmdsys.RemoteResponse, 1)
	o.mu.Lock()
	o.inflight[id] = &inflightCmd{
		id:      id,
		resp:    ch,
		cancel:  cancel,
		started: time.Now(),
	}
	o.mu.Unlock()
	return id, ch
}

// OnResponse delivers a response to a waiting alloc'd request.
// Safe to call from any goroutine. No-op if the request ID is unknown (already
// timed out or cancelled).
func (o *commandOrchestrator) OnResponse(resp *meshpb.CommandResponse) {
	o.mu.Lock()
	inf, ok := o.inflight[resp.RequestId]
	if ok {
		delete(o.inflight, resp.RequestId)
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	inf.cancel() // unblock any context-cancel listener
	rr := &cmdsys.RemoteResponse{
		RequestID:     resp.RequestId,
		OK:            resp.Ok,
		ResultJSON:    resp.ResultJson,
		Error:         resp.Error,
		TargetID:      resp.TargetId,
		SchemaVersion: resp.SchemaVersion,
	}
	select {
	case inf.resp <- rr:
	default:
	}
}

// OnCancel cancels the in-flight request locally if still pending.
func (o *commandOrchestrator) OnCancel(requestID uint64) {
	o.mu.Lock()
	inf, ok := o.inflight[requestID]
	if ok {
		delete(o.inflight, requestID)
	}
	o.mu.Unlock()
	if !ok {
		return
	}
	inf.cancel()
	// Send a nil response to wake any blocked Send call.
	select {
	case inf.resp <- nil:
	default:
	}
}

// remove removes an in-flight request without signalling; called when the
// caller's context is cancelled before the response arrives so we can send
// CommandCancel to the remote side.
func (o *commandOrchestrator) remove(requestID uint64) bool {
	o.mu.Lock()
	_, ok := o.inflight[requestID]
	if ok {
		delete(o.inflight, requestID)
	}
	o.mu.Unlock()
	return ok
}

// meshControlTransport is the cmdsys.Transport implementation backed by the
// MeshControl bidi gRPC stream. Process-side: sends CommandRequest to a
// target host via sendCoordMessageToHost. Host-side: sends CommandResponse
// back via the client's send path.
type meshControlTransport struct {
	coord *Process
	orch  *commandOrchestrator
}

func newMeshControlTransport(coord *Process) *meshControlTransport {
	return &meshControlTransport{
		coord: coord,
		orch:  newCommandOrchestrator(),
	}
}

// Send dispatches a CommandRequest to target and returns a channel on which
// exactly one *RemoteResponse will arrive (then the channel is closed/drained).
// If the target is a colocated host (same process), it short-circuits to a
// direct InvokeLocal call.
func (t *meshControlTransport) Send(ctx context.Context, target cmdsys.Target, req *cmdsys.RemoteRequest) (<-chan *cmdsys.RemoteResponse, error) {
	// Colocated short-circuit: if the target host is in-process, call
	// InvokeLocal directly without gRPC serialization. Mirrors the
	// cell_transfer_executor.go local fast-path pattern.
	if t.isColocated(target.ID) {
		return t.sendLocal(ctx, target, req)
	}

	// Remote path: allocate a request ID, register in orchestrator, send
	// CommandRequest via coordinator's control stream to the target host.
	cancelCtx, cancel := context.WithCancel(ctx)
	id, respCh := t.orch.alloc(cancel)
	req.RequestID = id

	pbReq := commandRequestToProto(req)
	msg := &meshpb.CoordMessage{
		CoordEpoch: t.coord.coordEpoch,
		Msg: &meshpb.CoordMessage_CommandRequest{
			CommandRequest: pbReq,
		},
	}
	if err := t.coord.sendCoordMessageToHost(target.ID, msg); err != nil {
		t.orch.remove(id)
		cancel()
		return nil, fmt.Errorf("cmdsys transport: send to host %q: %w", target.ID, err)
	}

	// Monitor ctx cancellation: if the caller cancels, remove from inflight
	// and send a CommandCancel to the remote.
	go func() {
		<-cancelCtx.Done()
		// Already resolved by OnResponse — no-op if not found.
		if t.orch.remove(id) {
			reason := "caller_context_canceled"
			if ctx.Err() == context.DeadlineExceeded {
				reason = "deadline_exceeded"
			}
			cancelMsg := &meshpb.CoordMessage{
				CoordEpoch: t.coord.coordEpoch,
				Msg: &meshpb.CoordMessage_CommandCancel{
					CommandCancel: &meshpb.CommandCancel{
						RequestId: id,
						Reason:    reason,
					},
				},
			}
			// Best-effort; ignore send errors.
			_ = t.coord.sendCoordMessageToHost(target.ID, cancelMsg)
			// Signal waiting Invoke so it unblocks.
			select {
			case respCh <- nil:
			default:
			}
		}
		cancel()
	}()

	return respCh, nil
}

// sendLocal executes the command in-process by calling InvokeLocal on the
// coordinator's own dispatcher. The response is delivered synchronously
// on a buffered channel.
//
// Note: unlike executeCommandRequest (the remote receive path) which forces
// caller.Source = SourceMeshControl, this colocated path preserves the
// caller's original Source. Any future handler that branches on Source
// must account for this asymmetry.
func (t *meshControlTransport) sendLocal(ctx context.Context, _ cmdsys.Target, req *cmdsys.RemoteRequest) (<-chan *cmdsys.RemoteResponse, error) {
	ch := make(chan *cmdsys.RemoteResponse, 1)

	caller := callerFromProto(&meshpb.Caller{
		Id:     req.Caller.ID,
		Source: uint32(req.Caller.Source),
		Grants: grantsToProto(req.Caller.Grants),
	})

	go func() {
		resultJSON, schemaVer, err := t.coord.dispatcher.InvokeLocal(ctx, caller, req.Verb, req.ArgsJSON)
		resp := &cmdsys.RemoteResponse{
			RequestID:     req.RequestID,
			SchemaVersion: schemaVer,
		}
		if err != nil {
			resp.OK = false
			resp.Error = err.Error()
		} else {
			resp.OK = true
			resp.ResultJSON = resultJSON
		}
		ch <- resp
	}()

	return ch, nil
}

// isColocated returns true when hostID refers to a host that is in the same
// process as this coordinator (i.e., present in c.Hosts).
func (t *meshControlTransport) isColocated(hostID string) bool {
	t.coord.mu.RLock()
	_, ok := t.coord.Hosts[hostID]
	t.coord.mu.RUnlock()
	return ok
}

// sendCoordMessageToHost is a thin wrapper so the transport can call the
// control server's method. Returns an error if no control server is active
// (e.g., no-host / standalone tests).
func (c *Process) sendCoordMessageToHost(hostID string, msg *meshpb.CoordMessage) error {
	if c.controlServer == nil {
		return fmt.Errorf("cmdsys: no control server (coordinator not in coordinator role)")
	}
	return c.controlServer.sendCoordMessageToHost(hostID, msg)
}

// Close is a no-op; the coordinator owns the transport lifetime.
func (t *meshControlTransport) Close() error { return nil }

// ── proto conversion helpers ──────────────────────────────────────────────────

func commandRequestToProto(req *cmdsys.RemoteRequest) *meshpb.CommandRequest {
	return &meshpb.CommandRequest{
		RequestId:         req.RequestID,
		Verb:              req.Verb,
		ArgsJson:          req.ArgsJSON,
		Caller:            callerToProto(req.Caller),
		DeadlineUnixNanos: req.DeadlineUnixNanos,
		TraceId:           req.TraceID,
		SchemaVersion:     req.SchemaVersion,
	}
}

func callerToProto(c cmdsys.Caller) *meshpb.Caller {
	return &meshpb.Caller{
		Id:     c.ID,
		Source: uint32(c.Source),
		Grants: grantsToProto(c.Grants),
	}
}

func grantsToProto(gs []cmdsys.Grant) []*meshpb.Grant {
	if len(gs) == 0 {
		return nil
	}
	out := make([]*meshpb.Grant, len(gs))
	for i, g := range gs {
		out[i] = &meshpb.Grant{Pattern: g.Pattern, Allow: g.Allow}
	}
	return out
}

func callerFromProto(pb *meshpb.Caller) cmdsys.Caller {
	if pb == nil {
		return cmdsys.Caller{}
	}
	grants := make([]cmdsys.Grant, len(pb.Grants))
	for i, g := range pb.Grants {
		if g != nil {
			grants[i] = cmdsys.Grant{Pattern: g.Pattern, Allow: g.Allow}
		}
	}
	return cmdsys.Caller{
		ID:     pb.Id,
		Source: cmdsys.CallerSource(pb.Source),
		Grants: grants,
	}
}

// buildCommandResponse builds a meshpb.CommandResponse from execution results.
// targetID identifies the executing host.
func buildCommandResponse(requestID uint64, targetID string, resultJSON []byte, schemaVer uint64, execErr error) *meshpb.CommandResponse {
	resp := &meshpb.CommandResponse{
		RequestId:     requestID,
		TargetId:      targetID,
		SchemaVersion: schemaVer,
	}
	if execErr != nil {
		resp.Ok = false
		resp.Error = execErr.Error()
	} else {
		resp.Ok = true
		resp.ResultJson = resultJSON
	}
	return resp
}

// executeCommandRequest handles an inbound CommandRequest on the receiving host,
// validates the schema version, executes the handler, and returns the response proto.
func executeCommandRequest(ctx context.Context, d *cmdsys.Dispatcher, hostID string, req *meshpb.CommandRequest) *meshpb.CommandResponse {
	// Schema version check: if the caller's ArgsSchemaHash doesn't match the
	// local command's hash, reject before executing.
	if req.SchemaVersion != 0 {
		localHash := d.ArgsSchemaHash(req.Verb)
		if localHash != 0 && localHash != req.SchemaVersion {
			return &meshpb.CommandResponse{
				RequestId: req.RequestId,
				TargetId:  hostID,
				Ok:        false,
				Error:     "schema_version",
			}
		}
	}

	caller := callerFromProto(req.Caller)
	// Re-validate caller source: arriving via MeshControl means it's a
	// MeshControl caller regardless of what the sender claims.
	caller.Source = cmdsys.SourceMeshControl

	resultJSON, schemaVer, err := d.InvokeLocal(ctx, caller, req.Verb, req.ArgsJson)
	return buildCommandResponse(req.RequestId, hostID, resultJSON, schemaVer, err)
}
