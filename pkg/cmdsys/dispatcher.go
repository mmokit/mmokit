package cmdsys

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

// Dispatcher ties together the registry, resolver, transport, audit, and
// RBAC grant store to implement Invoke.
type Dispatcher struct {
	registry  *Registry
	resolver  RouteResolver
	transport Transport
	audit     AuditSink
	grants    GrantStore
	log       *logger.Logger
	process   LocalProcess

	mu      sync.Mutex
	pending map[uint64]*pendingReq
	nextID  uint64

	closeOnce sync.Once
	closeCh   chan struct{}
}

// DispatcherConfig holds all dependencies for a Dispatcher.
type DispatcherConfig struct {
	Registry  *Registry
	Resolver  RouteResolver
	Transport Transport
	Audit     AuditSink
	Grants    GrantStore
	Logger    *logger.Logger
	// Process is published into env.Local.Process for every Invoke /
	// InvokeLocal call. Nil for unit tests.
	Process LocalProcess
}

// NewDispatcher creates a Dispatcher and starts its janitor goroutine.
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.Registry == nil {
		cfg.Registry = NewRegistry()
	}
	if cfg.Resolver == nil {
		cfg.Resolver = NewLocalResolver()
	}
	if cfg.Transport == nil {
		cfg.Transport = stubTransport{}
	}
	if cfg.Audit == nil {
		cfg.Audit = NoopAuditSink{}
	}
	if cfg.Grants == nil {
		cfg.Grants = NewInMemoryGrantStore()
	}
	if cfg.Logger == nil {
		cfg.Logger = logger.New()
	}
	d := &Dispatcher{
		registry:  cfg.Registry,
		resolver:  cfg.Resolver,
		transport: cfg.Transport,
		audit:     cfg.Audit,
		grants:    cfg.Grants,
		log:       cfg.Logger,
		process:   cfg.Process,
		pending:   make(map[uint64]*pendingReq),
		closeCh:   make(chan struct{}),
	}
	go d.janitor()
	return d
}

// InvokeLocal executes verb directly on the local registry, bypassing the
// route resolver and transport. Used by MeshControl receive paths to execute
// an inbound CommandRequest that has already been routed to this process.
// Returns (resultJSON, resultSchemaVersion, error).
func (d *Dispatcher) InvokeLocal(ctx context.Context, caller Caller, verb string, argsJSON []byte) ([]byte, uint64, error) {
	cmd, ok := d.registry.Lookup(verb)
	if !ok {
		return nil, 0, ErrUnknownVerb
	}
	if !Check(caller, cmd.Capability) {
		return nil, 0, ErrRBACDenied
	}
	argsVal, err := d.coerceArgs(cmd, argsJSON)
	if err != nil {
		return nil, 0, err
	}
	traceID := newTraceID()
	env := &Env{
		Caller:  caller,
		TraceID: traceID,
		Local:   &LocalContext{Process: d.process},
		Logger:  d.log,
	}
	res, herr := cmd.Handler(ctx, env, argsVal)
	if herr != nil {
		return nil, cmd.ResultSchemaHash, herr
	}
	resultJSON, merr := json.Marshal(res)
	if merr != nil {
		return nil, cmd.ResultSchemaHash, merr
	}
	return resultJSON, cmd.ResultSchemaHash, nil
}

// InvokeAsPeer executes verb on behalf of an authenticated mesh peer.
//
// Authority here is the PEER RELATION, not caller.Grants. The command arrived
// on a stream this process authenticated, sent by a coordinator that already
// ran Check against its own operator store before it resolved targets and
// fanned out. Re-checking on this side never added security — it re-ran the
// same test against grants the coordinator had just serialized — and the wire
// no longer carries grants at all, so Check would now deny everything.
//
// caller carries ID and Source for AUDIT ONLY. peerID is the authenticated
// sender. Callers must pass a peerID they took from the transport, never from
// the message body.
//
// Kept deliberately separate from InvokeLocal rather than adding a "skip RBAC"
// flag: the two have different trust stories and a boolean would let a future
// caller opt out of authorization without saying why.
func (d *Dispatcher) InvokeAsPeer(ctx context.Context, caller Caller, peerID, verb string, argsJSON []byte) ([]byte, uint64, error) {
	start := time.Now()
	traceID := newTraceID()

	emitDone := func(ok bool, errCode string, detail string) {
		d.audit.Emit(AuditRecord{
			Time:       time.Now(),
			TraceID:    traceID,
			Phase:      "done",
			Verb:       verb,
			CallerID:   caller.ID,
			PeerID:     peerID,
			Source:     caller.Source,
			OK:         ok,
			Error:      errCode,
			Detail:     detail,
			DurationMS: time.Since(start).Milliseconds(),
		})
	}

	d.audit.Emit(AuditRecord{
		Time:     start,
		TraceID:  traceID,
		Phase:    "start",
		Verb:     verb,
		CallerID: caller.ID,
		PeerID:   peerID,
		Source:   caller.Source,
		ArgsJSON: argsJSON,
	})

	cmd, ok := d.registry.Lookup(verb)
	if !ok {
		emitDone(false, "unknown_verb", verb)
		return nil, 0, ErrUnknownVerb
	}
	argsVal, err := d.coerceArgs(cmd, argsJSON)
	if err != nil {
		emitDone(false, "parse_error", err.Error())
		return nil, 0, err
	}
	env := &Env{
		Caller:  caller,
		TraceID: traceID,
		Local:   &LocalContext{Process: d.process},
		Logger:  d.log,
	}
	res, herr := cmd.Handler(ctx, env, argsVal)
	if herr != nil {
		emitDone(false, "handler_error", herr.Error())
		return nil, cmd.ResultSchemaHash, herr
	}
	resultJSON, merr := json.Marshal(res)
	if merr != nil {
		emitDone(false, "handler_error", merr.Error())
		return nil, cmd.ResultSchemaHash, merr
	}
	emitDone(true, "", "")
	return resultJSON, cmd.ResultSchemaHash, nil
}

// ArgsSchemaHash returns the ArgsSchemaHash for a registered verb, or 0 if not found.
func (d *Dispatcher) ArgsSchemaHash(verb string) uint64 {
	cmd, ok := d.registry.Lookup(verb)
	if !ok {
		return 0
	}
	return cmd.ArgsSchemaHash
}

// InvokeInternal dispatches a nested command on behalf of an already-running
// handler. It reuses the outer caller's identity (grants + source) and derives
// a context that leaves a 200ms margin so the inner fan-out cannot starve the
// outer handler of its own aggregation + response budget.
//
// Each inner invocation still generates its own TraceID. Operators correlate
// nested calls with outer via the audit log's shared CallerID plus time window.
func (d *Dispatcher) InvokeInternal(ctx context.Context, parent *Env, verb string, args any) (Result, error) {
	if parent == nil {
		return Result{}, fmt.Errorf("cmdsys: InvokeInternal: parent env is nil")
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 200*time.Millisecond {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, remaining-200*time.Millisecond)
			defer cancel()
		}
	}
	return d.Invoke(ctx, parent.Caller, verb, args)
}

// Close stops the janitor and cancels all pending requests.
func (d *Dispatcher) Close() error {
	d.closeOnce.Do(func() {
		close(d.closeCh)
		d.closeAllPending()
	})
	return d.transport.Close()
}

// Invoke executes verb on behalf of caller with the given raw args.
//
// raw may be:
//   - string: parsed via Parser then reflect-set onto the command's Args zero value
//   - []byte: JSON-unmarshaled into the command's Args type
//   - struct value/pointer assignable to the command's Args type: used directly
//
// A context deadline is required; the call returns ErrNoDeadline otherwise.
//
// Ordering: deadline → trace ID → start audit → lookup verb → RBAC → parse args
// → route resolve → execute → done audit.
// Unauthorized callers never have their args parsed (prevents schema-leak via
// parse error messages).
func (d *Dispatcher) Invoke(ctx context.Context, caller Caller, verb string, raw any) (Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		return Result{}, ErrNoDeadline
	}

	traceID := newTraceID()
	start := time.Now()

	// caller is pass-by-value — mutating Grants here is local-copy safe.
	if d.grants != nil {
		stored := d.grants.Grants(caller.ID)
		if len(stored) > 0 {
			merged := make([]Grant, 0, len(caller.Grants)+len(stored))
			merged = append(merged, caller.Grants...)
			merged = append(merged, stored...)
			caller.Grants = merged
		}
	}

	// Capture raw args as JSON for audit BEFORE any parsing.
	argsJSON, _ := json.Marshal(raw)
	d.audit.Emit(AuditRecord{
		Time:     start,
		TraceID:  traceID,
		Phase:    "start",
		Verb:     verb,
		CallerID: caller.ID,
		Source:   caller.Source,
		ArgsJSON: argsJSON,
	})

	emitDone := func(ok bool, errCode string, detail string, targets []string) {
		d.audit.Emit(AuditRecord{
			Time:       time.Now(),
			TraceID:    traceID,
			Phase:      "done",
			Verb:       verb,
			CallerID:   caller.ID,
			Source:     caller.Source,
			OK:         ok,
			Error:      errCode,
			Detail:     detail,
			Targets:    targets,
			DurationMS: time.Since(start).Milliseconds(),
		})
	}

	// Lookup verb.
	cmd, cmdFound := d.registry.Lookup(verb)
	if !cmdFound {
		emitDone(false, "unknown_verb", "", nil)
		return Result{}, ErrUnknownVerb
	}

	// RBAC check — must happen BEFORE parse so unauthorized callers never have
	// their args parsed (parse errors can leak schema information).
	if !Check(caller, cmd.Capability) {
		emitDone(false, "rbac_denied", "", nil)
		return Result{}, ErrRBACDenied
	}

	// Help-token interception. If the raw args contain --help, -h, or ? as a
	// standalone token (any position), skip parsing and the handler entirely
	// and return a HelpResult. This is dispatcher-level so every frontend
	// (console, HTTP, future remote CLI) gets help for free.
	if rawStr, isStr := raw.(string); isStr {
		if tokens, terr := tokenize(rawStr); terr == nil {
			for _, tok := range tokens {
				if IsHelpToken(tok) {
					emitDone(true, "", "", []string{"local"})
					return Result{
						Verb:       verb,
						Caller:     caller,
						TraceID:    traceID,
						PerTarget:  []TargetResult{{TargetID: "local", OK: true, Result: HelpResult{Verb: verb, Text: RenderHelp(d.registry, verb)}}},
						DurationMS: time.Since(start).Milliseconds(),
					}, nil
				}
			}
		}
	}

	// Parse / coerce args.
	argsVal, err := d.coerceArgs(cmd, raw)
	if err != nil {
		emitDone(false, "parse_error", err.Error(), nil)
		return Result{}, err
	}

	// Resolve route. Context-sensitive routes (RoutePlayerOwner,
	// RouteEntityOwner, RouteSpecificHost, RouteSpecificCell) read
	// the target identifier from argsVal by field name via reflection.
	// RouteService takes the service kind from cmd.Service.
	targets, err := d.resolver.Resolve(cmd.Route, verb, argsVal, cmd.Service)
	if err != nil {
		emitDone(false, "not_yet_wired", err.Error(), nil)
		return Result{}, err
	}

	env := &Env{
		Caller:  caller,
		TraceID: traceID,
		Local:   &LocalContext{Process: d.process},
		Logger:  d.log,
	}

	var perTarget []TargetResult
	var firstErr error
	var targetIDs []string

	for _, target := range targets {
		var tr TargetResult
		tr.TargetID = target.ID
		targetIDs = append(targetIDs, target.ID)

		if target.Kind == RouteLocal {
			res, herr := cmd.Handler(ctx, env, argsVal)
			if herr != nil {
				tr.OK = false
				tr.Error = herr.Error()
				if firstErr == nil {
					firstErr = herr
				}
			} else {
				tr.OK = true
				tr.Result = res
			}
		} else {
			// Remote dispatch: marshal args to JSON, send via transport, wait for response.
			argsJSON, merr := json.Marshal(argsVal)
			if merr != nil {
				tr.OK = false
				tr.Error = "marshal_error: " + merr.Error()
				if firstErr == nil {
					firstErr = merr
				}
				perTarget = append(perTarget, tr)
				continue
			}
			deadline, _ := ctx.Deadline()
			req := &RemoteRequest{
				Verb:              verb,
				ArgsJSON:          argsJSON,
				Caller:            caller,
				DeadlineUnixNanos: deadline.UnixNano(),
				TraceID:           traceID,
				SchemaVersion:     cmd.ArgsSchemaHash,
			}
			respCh, serr := d.transport.Send(ctx, target, req)
			if serr != nil {
				tr.OK = false
				tr.Error = serr.Error()
				if firstErr == nil {
					firstErr = serr
				}
				perTarget = append(perTarget, tr)
				continue
			}
			// Wait for response or context cancellation.
			var resp *RemoteResponse
			select {
			case resp = <-respCh:
			case <-ctx.Done():
				tr.OK = false
				tr.Error = ctx.Err().Error()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				perTarget = append(perTarget, tr)
				continue
			}
			if resp == nil {
				tr.OK = false
				tr.Error = ErrNotYetWired.Error()
				if firstErr == nil {
					firstErr = ErrNotYetWired
				}
			} else if !resp.OK {
				tr.OK = false
				tr.Error = resp.Error
				if firstErr == nil {
					firstErr = fmt.Errorf("%s", resp.Error)
				}
			} else {
				tr.OK = true
				// Unmarshal result JSON into cmd.Result type if available.
				if cmd.Result != nil && len(resp.ResultJSON) > 0 {
					resultType := reflect.TypeOf(cmd.Result)
					if resultType.Kind() == reflect.Pointer {
						resultType = resultType.Elem()
					}
					resultPtr := reflect.New(resultType)
					if uerr := json.Unmarshal(resp.ResultJSON, resultPtr.Interface()); uerr == nil {
						tr.Result = resultPtr.Elem().Interface()
					}
				}
			}
		}
		perTarget = append(perTarget, tr)
	}

	allOK := firstErr == nil
	errStr := ""
	if firstErr != nil {
		errStr = firstErr.Error()
	}
	errCode := ""
	if !allOK {
		errCode = "handler_error"
	}
	emitDone(allOK, errCode, errStr, targetIDs)

	result := Result{
		Verb:       verb,
		Caller:     caller,
		TraceID:    traceID,
		PerTarget:  perTarget,
		DurationMS: time.Since(start).Milliseconds(),
	}
	return result, firstErr
}
