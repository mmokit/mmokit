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

// ── service list ─────────────────────────────────────────────────────────────

type serviceListArgs struct{}

type serviceListRow struct {
	Op       string
	Kind     string
	Request  string
	Response string
}

type serviceListResult struct {
	Ops []serviceListRow `cmd:"table"`
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
// letter so `service list` matches what the SDK names the method.
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
	_ = coord // reserved for future RoutePlayerCell threading

	if err := reg.Register(cmdsys.Command{
		Verb:        "service.list",
		Capability:  "service.list",
		Description: "list every registered typed op (request/response types and route kind)",
		Route:       cmdsys.RouteLocal,
		Args:        serviceListArgs{},
		Result:      serviceListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if TypedOpHooks.ListTypedOps == nil {
				return serviceListResult{}, nil
			}
			entries := TypedOpHooks.ListTypedOps()
			rows := make([]serviceListRow, 0, len(entries))
			for _, e := range entries {
				rows = append(rows, serviceListRow{
					Op:       opShortName(e.RequestType),
					Kind:     e.KindName,
					Request:  e.RequestType.String(),
					Response: e.ResponseType.String(),
				})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Op < rows[j].Op })
			return serviceListResult{Ops: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.list: %w", err)
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
				return nil, fmt.Errorf("op name is required (try `service list`)")
			}
			e, ok := findOpByShortName(name)
			if !ok {
				return nil, fmt.Errorf("unknown op %q (try `service list`)", name)
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
				return nil, fmt.Errorf("op name is required (try `service list`)")
			}
			e, ok := findOpByShortName(name)
			if !ok {
				return nil, fmt.Errorf("unknown op %q (try `service list`)", name)
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

			body := ReflectMarshal(reqPtr.Interface())
			frame := EncodeTypedOpFrame(e.RequestID, 0, body)

			// Synthesize an OpContext. ConnID 0 is a "no real client"
			// sentinel; "service-cli" tags any DB rows the handler writes
			// so an operator can grep their own console activity later.
			opCtx := &ops.OpContext{ConnID: 0, Username: "service-cli"}
			respFrame := DispatchTypedOpInbound(frame[1:], opCtx, nil)
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
				ReflectUnmarshal(resBody, &opErr)
				fmt.Fprintf(&sb, "OperationError: code=%d %s\n", opErr.Code, opErr.Message)
				return serviceCallResult{Output: sb.String()}, nil
			}

			resPtr := reflect.New(e.ResponseType)
			ReflectUnmarshal(resBody, resPtr.Interface())
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
