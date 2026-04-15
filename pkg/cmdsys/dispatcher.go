package cmdsys

import (
	"context"
	"encoding/json"
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

	// Parse / coerce args.
	argsVal, err := d.coerceArgs(cmd, raw)
	if err != nil {
		emitDone(false, "parse_error", err.Error(), nil)
		return Result{}, err
	}

	// Resolve route.
	targets, err := d.resolver.Resolve(cmd.Route, verb)
	if err != nil {
		emitDone(false, "not_yet_wired", err.Error(), nil)
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
