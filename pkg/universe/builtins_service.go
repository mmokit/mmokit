package universe

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/ops"
)

// ── service list (service kinds, aggregated) ────────────────────────────────

type serviceListArgs struct{}

type serviceKindRow struct {
	Kind    string
	OpCount int
	Routing string
	Running bool // true when at least one live instance is registered cluster-wide
}

type serviceListResult struct {
	Services []serviceKindRow `cmd:"table"`
}

// ── service instances (cluster-wide instance roster) ───────────────────────

type serviceInstancesArgs struct{}

type serviceInstanceRow struct {
	Kind       string
	InstanceID string
	HostID     string
	OpCount    int
}

type serviceInstancesResult struct {
	Instances []serviceInstanceRow `cmd:"table"`
}

// ── service ops (typed ops, optionally filtered) ────────────────────────────

type serviceOpsArgs struct {
	Kind string `cmd:"optional,help=filter to one service kind (e.g. chat); empty = all"`
}

type serviceOpsRow struct {
	Op       string
	Kind     string
	Routing  string
	Request  string
	Response string
}

type serviceOpsResult struct {
	Ops []serviceOpsRow `cmd:"table"`
}

// ── service info ─────────────────────────────────────────────────────────────

type serviceInfoArgs struct {
	OpName string `cmd:"help=op name (e.g. authLogin)"`
}

type serviceInfoResult struct {
	Output string
}

// ── service call ─────────────────────────────────────────────────────────────

type serviceCallArgs struct {
	OpName string `cmd:"help=op name (e.g. authLogin)"`
	JSON   string `cmd:"rest,help=request body as JSON object"`
}

type serviceCallResult struct {
	Output string
}

// ── helpers ──────────────────────────────────────────────────────────────────

// opShortName derives a friendly handle for a typed-op request type.
// reflect.Type.String() returns "pkg.RequestName" — strip the package
// prefix and the conventional "Request" suffix, then lowercase the first
// letter so `service ops` matches what the SDK names the method.
//
// Examples:
//
//	auth.AuthLoginRequest           -> authLogin
//	marketplace.MarketBrowseRequest -> marketBrowse
//	mmokit.OperationError           -> operationError
//
// Hand-rolled rather than canonical because the typed-op registry stores
// only the reflect.Type — there's no separate "wire name" attached.
func opShortName(t reflect.Type) string {
	name := t.String()
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	name = strings.TrimSuffix(name, "Request")
	if name == "" {
		return name
	}
	r := []rune(name)
	r[0] = []rune(strings.ToLower(string(r[0])))[0]
	return string(r)
}

// serviceKindOf derives the service kind name from a typed-op request
// type by extracting the package prefix from reflect.Type.String().
// "auth.AuthLoginRequest" -> "auth", "chat.ChatSendRequest" -> "chat".
// The typed-op registry tracks routing kind (RouteKind) but not service
// kind, so the package convention (one Go package per service) is the
// natural grouping axis. Returns "" when the type is not package-prefixed
// (e.g. an unnamed type used in tests).
func serviceKindOf(t reflect.Type) string {
	name := t.String()
	dot := strings.LastIndex(name, ".")
	if dot <= 0 {
		return ""
	}
	return name[:dot]
}

// routingString turns a TypedOpInfo.Kind (uint8 mirror of mmokit.RouteKind)
// into a human-readable routing label. Falls back to e.KindName when the
// hooks already populated it (the fast path — mmokit's init copies
// RouteKind.String() into KindName at registration time).
func routingString(kind uint8, kindName string) string {
	if kindName != "" {
		return kindName
	}
	switch kind {
	case TypedOpHooks.RouteGatewayLocal:
		return "gateway-local"
	default:
		return "unknown"
	}
}

// findOpByShortName returns the registry row whose request type maps to
// the given short name. Returns the zero TypedOpInfo and false if no
// match. Snapshot of the registry — calls ListTypedOps once.
func findOpByShortName(name string) (TypedOpInfo, bool) {
	if TypedOpHooks.ListTypedOps == nil {
		return TypedOpInfo{}, false
	}
	for _, e := range TypedOpHooks.ListTypedOps() {
		if opShortName(e.RequestType) == name {
			return e, true
		}
	}
	return TypedOpInfo{}, false
}

// printStructFields renders an indented field listing for a struct type.
// Used by `service info` to surface the request and response shapes.
func printStructFields(sb *strings.Builder, t reflect.Type, indent string) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		fmt.Fprintf(sb, "%s(non-struct: %s)\n", indent, t.String())
		return
	}
	if t.NumField() == 0 {
		fmt.Fprintf(sb, "%s(no fields)\n", indent)
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fmt.Fprintf(sb, "%s%s: %s\n", indent, f.Name, f.Type.String())
	}
}

// ── registration ─────────────────────────────────────────────────────────────

func registerServiceBuiltins(reg *cmdsys.Registry, coord *Process) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.list",
		Capability:  "service.list",
		Description: "list registered service kinds with op counts and routing summary",
		Route:       cmdsys.RouteLocal,
		Args:        serviceListArgs{},
		Result:      serviceListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if TypedOpHooks.ListTypedOps == nil {
				return serviceListResult{}, nil
			}
			entries := TypedOpHooks.ListTypedOps()

			type kindAgg struct {
				name     string
				opCount  int
				routings map[string]struct{}
			}
			agg := map[string]*kindAgg{}
			for _, e := range entries {
				kind := serviceKindOf(e.RequestType)
				if kind == "" {
					kind = "(unknown)"
				}
				a, ok := agg[kind]
				if !ok {
					a = &kindAgg{name: kind, routings: map[string]struct{}{}}
					agg[kind] = a
				}
				a.opCount++
				a.routings[routingString(e.Kind, e.KindName)] = struct{}{}
			}
			rows := make([]serviceKindRow, 0, len(agg))
			for _, a := range agg {
				routing := "mixed"
				if len(a.routings) == 1 {
					for r := range a.routings {
						routing = r
					}
				}
				running := false
				if coord != nil {
					running = coord.HostForServiceKind(a.name) != ""
				}
				rows = append(rows, serviceKindRow{
					Kind:    a.name,
					OpCount: a.opCount,
					Routing: routing,
					Running: running,
				})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Kind < rows[j].Kind })
			return serviceListResult{Services: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.list: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "service.instances",
		Capability:  "service.list",
		Description: "list every service instance running anywhere in the cluster (coordinator-side roster)",
		Examples:    []string{"service instances"},
		// Reads coord.coordServices directly — no remote dispatch needed
		// because the coordinator holds the authoritative cluster-wide
		// view. Non-coordinator processes return an empty result.
		Route:  cmdsys.RouteLocal,
		Args:   serviceInstancesArgs{},
		Result: serviceInstancesResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if coord == nil || coord.coordServices == nil {
				// Not a coordinator process — surface an empty roster
				// rather than erroring so the command is harmless to
				// invoke from any pane.
				return serviceInstancesResult{}, nil
			}
			// Mirror service.list: typed-op-only kinds (the modern norm)
			// leave Kind.OpCodes nil because dispatch is by request typeID,
			// not integer code. Aggregate the typed-op registry by kind so
			// per-instance OpCount matches what `service list` reports.
			typedOpsByKind := map[string]int{}
			if TypedOpHooks.ListTypedOps != nil {
				for _, e := range TypedOpHooks.ListTypedOps() {
					typedOpsByKind[serviceKindOf(e.RequestType)]++
				}
			}
			rows := make([]serviceInstanceRow, 0)
			for _, k := range coord.coordServices.Kinds() {
				for _, inst := range coord.coordServices.InstancesOfKind(k) {
					rows = append(rows, serviceInstanceRow{
						Kind:       inst.Kind,
						InstanceID: inst.InstanceID,
						HostID:     inst.HostID,
						OpCount:    typedOpsByKind[inst.Kind] + len(inst.OpCodes),
					})
				}
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Kind != rows[j].Kind {
					return rows[i].Kind < rows[j].Kind
				}
				return rows[i].HostID < rows[j].HostID
			})
			return serviceInstancesResult{Instances: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.instances: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "service.ops",
		Capability:  "service.list",
		Description: "list typed ops (optionally filtered by service kind)",
		Examples:    []string{"service ops", "service ops chat"},
		Route:       cmdsys.RouteLocal,
		Args:        serviceOpsArgs{},
		Result:      serviceOpsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(serviceOpsArgs)
			if TypedOpHooks.ListTypedOps == nil {
				return serviceOpsResult{}, nil
			}
			entries := TypedOpHooks.ListTypedOps()
			kindFilter := strings.TrimSpace(args.Kind)
			rows := make([]serviceOpsRow, 0, len(entries))
			for _, e := range entries {
				svcKind := serviceKindOf(e.RequestType)
				if kindFilter != "" && svcKind != kindFilter {
					continue
				}
				rows = append(rows, serviceOpsRow{
					Op:       opShortName(e.RequestType),
					Kind:     svcKind,
					Routing:  routingString(e.Kind, e.KindName),
					Request:  e.RequestType.String(),
					Response: e.ResponseType.String(),
				})
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Kind != rows[j].Kind {
					return rows[i].Kind < rows[j].Kind
				}
				return rows[i].Op < rows[j].Op
			})
			return serviceOpsResult{Ops: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.ops: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "service.info",
		Capability:  "service.info",
		Description: "show request/response field schema for a typed op",
		Route:       cmdsys.RouteLocal,
		Args:        serviceInfoArgs{},
		Result:      serviceInfoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(serviceInfoArgs)
			name := strings.TrimSpace(args.OpName)
			if name == "" {
				return nil, fmt.Errorf("op name is required (try `service ops`)")
			}
			e, ok := findOpByShortName(name)
			if !ok {
				return nil, fmt.Errorf("unknown op %q (try `service ops`)", name)
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "  Op:       %s\n", opShortName(e.RequestType))
			fmt.Fprintf(&sb, "  Kind:     %s\n", e.KindName)
			fmt.Fprintf(&sb, "  Request:  %s\n", e.RequestType.String())
			printStructFields(&sb, e.RequestType, "    ")
			fmt.Fprintf(&sb, "  Response: %s\n", e.ResponseType.String())
			printStructFields(&sb, e.ResponseType, "    ")
			return serviceInfoResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.info: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "service.call",
		Capability:  "service.call",
		Description: "invoke a typed op with a JSON request body (RouteGatewayLocal only)",
		Examples: []string{
			`service call authValidateToken {"sessionToken":"..."}`,
		},
		Route:  cmdsys.RouteLocal,
		Args:   serviceCallArgs{},
		Result: serviceCallResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(serviceCallArgs)
			name := strings.TrimSpace(args.OpName)
			if name == "" {
				return nil, fmt.Errorf("op name is required (try `service ops`)")
			}
			e, ok := findOpByShortName(name)
			if !ok {
				return nil, fmt.Errorf("unknown op %q (try `service ops`)", name)
			}

			// RoutePlayerCell handlers need a real player session — the
			// console goroutine has none to thread through engine.RunOnLoop.
			// Surface a clear error rather than letting DispatchTypedOpInbound
			// return an opaque "no active cell" failure later.
			if e.Kind != TypedOpHooks.RouteGatewayLocal {
				return nil, fmt.Errorf("op %q is %s — invoke from a logged-in client; console-driven cell ops are not yet supported", name, e.KindName)
			}

			jsonBody := strings.TrimSpace(args.JSON)
			if jsonBody == "" {
				jsonBody = "{}"
			}

			reqPtr := reflect.New(e.RequestType)
			if err := json.Unmarshal([]byte(jsonBody), reqPtr.Interface()); err != nil {
				return nil, fmt.Errorf("parse request JSON: %w (input: %s)", err, jsonBody)
			}

			body, err := ReflectMarshal(reqPtr.Interface())
			if err != nil {
				return nil, fmt.Errorf("encode request: %w", err)
			}
			frame := EncodeTypedOpFrame(e.RequestID, 0, body)

			// Synthesize an OpContext. ConnID 0 is a "no real client"
			// sentinel; "service-cli" tags any DB rows the handler writes
			// so an operator can grep their own console activity later.
			opCtx := &ops.OpContext{ConnID: 0, Username: "service-cli"}
			respFrame := DispatchTypedOpInbound(frame[1:], opCtx, nil, coord.clientWireLimits())
			if respFrame == nil {
				return nil, fmt.Errorf("dispatcher returned nil frame (RoutePlayerCell async path?)")
			}

			resTypeID, _, resBody, err := DecodeTypedOpFrame(respFrame[1:])
			if err != nil {
				return nil, fmt.Errorf("decode response frame: %w", err)
			}

			var sb strings.Builder
			if resTypeID == TypedOpHooks.OperationErrorTypeID {
				// OperationError shape mirrored on the universe side via
				// reflection — Code uint32, Message string. Decode as a
				// dynamic struct so this file stays free of the mmokit
				// import.
				type opErrShape struct {
					Code    uint32
					Message string
				}
				var opErr opErrShape
				if err := ReflectUnmarshal(resBody, &opErr); err != nil {
					// The operator asked what went wrong; reporting a
					// half-decoded code and an empty message would answer with
					// a different lie than the one they hit.
					return nil, fmt.Errorf("decode OperationError body: %w", err)
				}
				fmt.Fprintf(&sb, "OperationError: code=%d %s\n", opErr.Code, opErr.Message)
				return serviceCallResult{Output: sb.String()}, nil
			}

			resPtr := reflect.New(e.ResponseType)
			if err := ReflectUnmarshal(resBody, resPtr.Interface()); err != nil {
				return nil, fmt.Errorf("decode %s response: %w", e.ResponseType.String(), err)
			}
			pretty, err := json.MarshalIndent(resPtr.Interface(), "  ", "  ")
			if err != nil {
				return nil, fmt.Errorf("render response JSON: %w", err)
			}
			fmt.Fprintf(&sb, "  %s:\n  %s\n", e.ResponseType.String(), string(pretty))
			return serviceCallResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.call: %w", err)
	}

	return nil
}
