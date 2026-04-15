package cmdsys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

// pendingReq tracks an in-flight remote dispatch awaiting a response.
type pendingReq struct {
	created time.Time
	cancel  context.CancelFunc
	Done    chan struct{}
	result  RemoteResponse
	err     error
}

// Dispatcher ties together the registry, resolver, transport, audit, and
// RBAC grant store to implement Invoke.
type Dispatcher struct {
	registry  *Registry
	resolver  RouteResolver
	transport Transport
	audit     AuditSink
	grants    GrantStore
	log       *logger.Logger

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
		cfg.Transport = InProcTransport{}
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
		pending:   make(map[uint64]*pendingReq),
		closeCh:   make(chan struct{}),
	}
	go d.janitor()
	return d
}

// Close stops the janitor and cancels all pending requests.
func (d *Dispatcher) Close() error {
	d.closeOnce.Do(func() {
		close(d.closeCh)
		d.mu.Lock()
		for id, p := range d.pending {
			p.cancel()
			p.err = fmt.Errorf("dispatcher closed")
			close(p.Done)
			delete(d.pending, id)
		}
		d.mu.Unlock()
	})
	return d.transport.Close()
}

// janitor sweeps expired or context-cancelled pending requests every 60s and
// also responds to Close() immediately.
func (d *Dispatcher) janitor() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.closeCh:
			return
		case <-ticker.C:
			d.mu.Lock()
			for id, p := range d.pending {
				select {
				case <-p.Done:
					delete(d.pending, id)
				default:
				}
			}
			d.mu.Unlock()
		}
	}
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

	// Merge stored grants into caller.
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

	emitDone := func(ok bool, errCode string, targets []string) {
		d.audit.Emit(AuditRecord{
			Time:       time.Now(),
			TraceID:    traceID,
			Phase:      "done",
			Verb:       verb,
			CallerID:   caller.ID,
			Source:     caller.Source,
			OK:         ok,
			Error:      errCode,
			Targets:    targets,
			DurationMS: time.Since(start).Milliseconds(),
		})
	}

	// Lookup verb.
	cmd, ok := d.registry.Lookup(verb)
	if !ok {
		emitDone(false, "unknown_verb", nil)
		return Result{}, ErrUnknownVerb
	}

	// RBAC check — must happen BEFORE parse so unauthorized callers never have
	// their args parsed (parse errors can leak schema information).
	if !Check(caller, cmd.Capability) {
		emitDone(false, "rbac_denied", nil)
		return Result{}, ErrRBACDenied
	}

	// Parse / coerce args.
	argsVal, err := d.coerceArgs(cmd, raw)
	if err != nil {
		emitDone(false, "parse_"+err.Error(), nil)
		return Result{}, err
	}

	// Resolve route.
	targets, err := d.resolver.Resolve(cmd.Route, verb)
	if err != nil {
		emitDone(false, "route_error", nil)
		return Result{}, err
	}

	env := &Env{
		Caller:  caller,
		TraceID: traceID,
		Local:   &LocalContext{},
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
			tr.OK = false
			tr.Error = ErrNotYetWired.Error()
			if firstErr == nil {
				firstErr = ErrNotYetWired
			}
		}
		perTarget = append(perTarget, tr)
	}

	ok2 := firstErr == nil
	emitDone(ok2, func() string {
		if !ok2 {
			return firstErr.Error()
		}
		return ""
	}(), targetIDs)

	result := Result{
		Verb:       verb,
		Caller:     caller,
		TraceID:    traceID,
		PerTarget:  perTarget,
		DurationMS: time.Since(start).Milliseconds(),
	}
	return result, firstErr
}

// coerceArgs converts raw into the concrete args type expected by cmd.
func (d *Dispatcher) coerceArgs(cmd Command, raw any) (any, error) {
	if cmd.Args == nil {
		return nil, nil
	}

	argsType := reflect.TypeOf(cmd.Args)
	if argsType.Kind() == reflect.Ptr {
		argsType = argsType.Elem()
	}
	argsPtr := reflect.New(argsType)

	switch v := raw.(type) {
	case string:
		schema, err := SchemaOf(cmd.Args)
		if err != nil {
			return nil, fmt.Errorf("args schema: %w", err)
		}
		var p Parser
		m, err := p.Bind(v, schema)
		if err != nil {
			return nil, err
		}
		if err := ApplyMap(argsPtr.Interface(), m); err != nil {
			return nil, err
		}
	case []byte:
		if err := json.Unmarshal(v, argsPtr.Interface()); err != nil {
			return nil, fmt.Errorf("parse_json: %w", err)
		}
	default:
		// Direct struct assignment: check type compatibility.
		rv := reflect.ValueOf(raw)
		if rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		if rv.Type() == argsType {
			argsPtr.Elem().Set(rv)
		} else {
			return nil, fmt.Errorf("cmdsys: cannot coerce %T to %s", raw, argsType)
		}
	}

	return argsPtr.Elem().Interface(), nil
}

// newTraceID returns a 16-character hex trace ID from 8 random bytes.
func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// allocPending registers a new in-flight remote request. Returns its ID.
func (d *Dispatcher) allocPending(cancel context.CancelFunc) uint64 {
	d.mu.Lock()
	id := d.nextID
	d.nextID++
	d.pending[id] = &pendingReq{
		created: time.Now(),
		cancel:  cancel,
		Done:    make(chan struct{}),
	}
	d.mu.Unlock()
	return id
}

// resolvePending marks a pending request done and removes it from the map.
func (d *Dispatcher) resolvePending(id uint64, resp RemoteResponse, err error) {
	d.mu.Lock()
	p, ok := d.pending[id]
	if ok {
		p.result = resp
		p.err = err
		p.cancel()
		close(p.Done)
		delete(d.pending, id)
	}
	d.mu.Unlock()
}
