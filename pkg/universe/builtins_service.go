package universe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/ops"
)

type serviceListArgs struct{}

type serviceListResult struct {
	Output string
}

type serviceInfoArgs struct {
	Kind string `cmd:"help=service kind name,complete=service-kinds"`
}

type serviceInfoResult struct {
	Output string
}

type serviceKindsArgs struct{}

type serviceKindsResult struct {
	Output string
}

type serviceOpsArgs struct{}

type serviceOpsResult struct {
	Output string
}

// opSchemaMap returns a code→schema lookup built from the router's typed
// registrations. Empty when router is nil or no handlers were registered
// via the typed mmokit.RegisterOp helper.
func opSchemaMap(router *ops.Router) map[uint32]ops.OperationSchema {
	out := map[uint32]ops.OperationSchema{}
	if router == nil {
		return out
	}
	for _, s := range router.Schema() {
		out[s.Code] = s
	}
	return out
}

// opNameOrFallback returns the handler name registered for a code, or a
// "(code)" placeholder when the handler was registered via the bare
// ops.Router.Register path (no typed name capture).
func opNameOrFallback(schemas map[uint32]ops.OperationSchema, code uint32) string {
	if s, ok := schemas[code]; ok && s.Name != "" {
		return s.Name
	}
	return "(unnamed)"
}

func registerServiceBuiltins(reg *cmdsys.Registry, coord *Process) error {
	c := coord

	// service.list — cluster-wide instance roster from CoordRegistry
	// (coordinator-only) or empty on processes without it.
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.list",
		Capability:  "service.list",
		Description: "list every running service instance across the cluster",
		Route:       cmdsys.RouteLocal,
		Args:        serviceListArgs{},
		Result:      serviceListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.coordServices == nil {
				return serviceListResult{Output: "  (this process has no coordinator role; ask the coordinator)\n"}, nil
			}
			snap := c.coordServices.Snapshot()
			if len(snap) == 0 {
				return serviceListResult{Output: "  (no service instances registered)\n"}, nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-10s %s\n", "KIND", "INSTANCE", "HOST", "JOINED", "OPS")
			fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-10s %s\n", "----", "--------", "----", "------", "---")
			for _, inst := range snap {
				age := time.Since(inst.JoinedAt).Truncate(time.Second).String()
				fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-10s %d\n",
					inst.Kind, inst.InstanceID, inst.HostID, age, len(inst.OpCodes))
			}
			fmt.Fprintf(&sb, "\n  (use 'service info <kind>' for op detail)\n")
			return serviceListResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return err
	}

	// service.info <kind> — per-kind detail
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.info",
		Capability:  "service.info",
		Description: "show detail for a service kind: instance count, op codes, registration",
		Route:       cmdsys.RouteLocal,
		Args:        serviceInfoArgs{},
		Result:      serviceInfoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args, ok := raw.(serviceInfoArgs)
			if !ok {
				return nil, fmt.Errorf("service.info: invalid args type %T", raw)
			}
			kindName := strings.TrimSpace(args.Kind)
			if kindName == "" {
				return nil, fmt.Errorf("service.info: kind is required")
			}
			var sb strings.Builder
			// Local registry — what this binary knows about
			k, found := c.services.Get(kindName)
			fmt.Fprintf(&sb, "Kind: %s\n", kindName)
			if found {
				fmt.Fprintf(&sb, "  Registered:    yes (this binary)\n")
				fmt.Fprintf(&sb, "  Description:   %s\n", k.Description)
				fmt.Fprintf(&sb, "  RequiresDB:    %v\n", k.RequiresDB)
				if k.MetricsPrefix != "" {
					fmt.Fprintf(&sb, "  MetricsPrefix: %s\n", k.MetricsPrefix)
				}
				// Resolve op codes to handler names + proto types via the
				// local OpRouter's schema. Names come from typed
				// mmokit.RegisterOp calls; bare ops.Router.Register
				// handlers show up as (unnamed).
				schemaByCode := opSchemaMap(c.cfg.OpRouter)
				fmt.Fprintf(&sb, "  Ops:\n")
				codes := append([]uint32(nil), k.OpCodes...)
				sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
				for _, code := range codes {
					if s, ok := schemaByCode[code]; ok && s.Name != "" {
						fmt.Fprintf(&sb, "    %5d  %-24s req=%s  resp=%s\n",
							code, s.Name, s.RequestProto, s.ResponseProto)
					} else {
						fmt.Fprintf(&sb, "    %5d  (unnamed)\n", code)
					}
				}
			} else {
				fmt.Fprintf(&sb, "  Registered:    no (not in this binary's registry)\n")
			}
			// Cluster — what's actually running
			if c.coordServices == nil {
				fmt.Fprintf(&sb, "  Live instances: (no coordinator role on this process)\n")
				return serviceInfoResult{Output: sb.String()}, nil
			}
			insts := c.coordServices.InstancesOfKind(kindName)
			fmt.Fprintf(&sb, "  Live instances: %d\n", len(insts))
			for _, inst := range insts {
				age := time.Since(inst.JoinedAt).Truncate(time.Second).String()
				fmt.Fprintf(&sb, "    - %s host=%s joined=%s\n",
					inst.InstanceID, inst.HostID, age)
			}
			return serviceInfoResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return err
	}

	// service.kinds — what's compiled into this binary's registry
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.kinds",
		Capability:  "service.kinds",
		Description: "list service kinds compiled into this binary",
		Route:       cmdsys.RouteLocal,
		Args:        serviceKindsArgs{},
		Result:      serviceKindsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			kinds := c.services.All()
			if len(kinds) == 0 {
				return serviceKindsResult{Output: "  (no service kinds registered in this binary)\n"}, nil
			}
			schemaByCode := opSchemaMap(c.cfg.OpRouter)
			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-12s %-10s %-4s  %s\n", "NAME", "DB", "OPS", "DESCRIPTION")
			fmt.Fprintf(&sb, "  %-12s %-10s %-4s  %s\n", "----", "--", "---", "-----------")
			for _, k := range kinds {
				dbCol := "-"
				if k.RequiresDB {
					dbCol = "required"
				}
				fmt.Fprintf(&sb, "  %-12s %-10s %-4d  %s\n",
					k.Name, dbCol, len(k.OpCodes), k.Description)
				codes := append([]uint32(nil), k.OpCodes...)
				sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
				for _, code := range codes {
					name := opNameOrFallback(schemaByCode, code)
					fmt.Fprintf(&sb, "      %5d  %s\n", code, name)
				}
			}
			return serviceKindsResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return err
	}

	// service.ops — current routing table (op-code → kind → instances)
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.ops",
		Capability:  "service.ops",
		Description: "show op-code → kind routing table from PeerList state",
		Route:       cmdsys.RouteLocal,
		Args:        serviceOpsArgs{},
		Result:      serviceOpsResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			opMap := c.serviceRouting.AllOps()
			if len(opMap) == 0 {
				return serviceOpsResult{Output: "  (no service ops registered; client ops route to cells by default)\n"}, nil
			}
			codes := make([]uint32, 0, len(opMap))
			for code := range opMap {
				codes = append(codes, code)
			}
			sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })

			schemaByCode := opSchemaMap(c.cfg.OpRouter)
			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-6s %-24s %-16s %s\n", "CODE", "NAME", "KIND", "INSTANCES")
			fmt.Fprintf(&sb, "  %-6s %-24s %-16s %s\n", "----", "----", "----", "---------")
			for _, code := range codes {
				kind := opMap[code]
				insts := c.serviceRouting.InstancesOfKind(kind)
				idStrs := make([]string, 0, len(insts))
				for _, in := range insts {
					idStrs = append(idStrs, in.InstanceID)
				}
				fmt.Fprintf(&sb, "  %-6d %-24s %-16s %s\n",
					code, opNameOrFallback(schemaByCode, code), kind, strings.Join(idStrs, ","))
			}
			return serviceOpsResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return err
	}

	// service.call — invoke a handler from the console for testing.
	// Synthesizes a connID/Username so the handler runs end-to-end
	// without a real client connection.
	//
	// Args use key=value pairs (the cmdsys tokenizer strips JSON quotes
	// otherwise). Values are coerced to bool/number/string by simple
	// content sniffing, which covers the common debug case. For nested
	// or complex protos, write a tiny test client instead.
	type serviceCallArgs struct {
		Kind string `cmd:"help=service kind name (e.g. echo),complete=service-kinds"`
		Op   string `cmd:"help=op handler name or numeric code (e.g. echoPing or 300),complete=service-ops"`
		Args string `cmd:"rest,optional,help=key=value pairs (e.g. msg=hello key=foo),complete=service-call-args"`
	}
	type serviceCallResult struct {
		Output string
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.call",
		Capability:  "service.call",
		Description: "invoke a service handler with a JSON request body (console testing)",
		Route:       cmdsys.RouteLocal,
		Args:        serviceCallArgs{},
		Result:      serviceCallResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args, ok := raw.(serviceCallArgs)
			if !ok {
				return nil, fmt.Errorf("service.call: invalid args type %T", raw)
			}
			out, err := invokeServiceCall(c, args.Kind, args.Op, args.Args)
			if err != nil {
				return serviceCallResult{Output: fmt.Sprintf("error: %v\n", err)}, nil
			}
			return serviceCallResult{Output: out}, nil
		},
	}); err != nil {
		return err
	}

	return nil
}

// invokeServiceCall implements 'service call'. Looks up the op by name
// or numeric code, builds the request proto from key=value pairs,
// dispatches via OpRouter.Invoke with a synthetic OpContext, then renders
// the response proto as pretty-printed JSON.
func invokeServiceCall(c *Process, kindName, opSpec, kvArgs string) (string, error) {
	kindName = strings.TrimSpace(kindName)
	opSpec = strings.TrimSpace(opSpec)
	if kindName == "" {
		return "", fmt.Errorf("kind is required")
	}
	if opSpec == "" {
		return "", fmt.Errorf("op is required (handler name or numeric code)")
	}
	kind, found := c.services.Get(kindName)
	if !found {
		return "", fmt.Errorf("kind %q not registered in this binary", kindName)
	}
	if c.cfg.OpRouter == nil {
		return "", fmt.Errorf("OpRouter is nil — should not happen in service-hosting processes")
	}

	// Resolve opSpec → (code, schema).
	schemas := c.cfg.OpRouter.Schema()
	schemaByCode := opSchemaMap(c.cfg.OpRouter)
	var matched *ops.OperationSchema
	if code, err := strconv.ParseUint(opSpec, 10, 32); err == nil {
		s, ok := schemaByCode[uint32(code)]
		if ok {
			matched = &s
		}
	}
	if matched == nil {
		for _, s := range schemas {
			if s.Name == opSpec {
				cp := s
				matched = &cp
				break
			}
		}
	}
	if matched == nil {
		return "", fmt.Errorf("op %q not found in this binary's OpRouter (try `service info %s`)", opSpec, kindName)
	}
	belongs := false
	for _, code := range kind.OpCodes {
		if code == matched.Code {
			belongs = true
			break
		}
	}
	if !belongs {
		return "", fmt.Errorf("op %q (code %d) is not a handler of kind %q", matched.Name, matched.Code, kindName)
	}

	// Build JSON object from key=value pairs.
	jsonIn, err := kvArgsToJSON(kvArgs)
	if err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	reqMsg, err := newProtoMessage(matched.RequestProto)
	if err != nil {
		return "", fmt.Errorf("request type %q: %w", matched.RequestProto, err)
	}
	if err := protojson.Unmarshal([]byte(jsonIn), reqMsg); err != nil {
		return "", fmt.Errorf("apply args to %s: %w (json=%s)", matched.RequestProto, err, jsonIn)
	}
	reqBytes, err := proto.Marshal(reqMsg)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Synthesize an OpContext. ConnID 0 is a "no real client" sentinel.
	// Username "console" tags any DB rows the handler may write so an
	// operator can grep their own console activity later.
	opCtx := &ops.OpContext{ConnID: 0, Username: "console"}
	respBytes, err := c.cfg.OpRouter.Invoke(matched.Code, opCtx, reqBytes)
	if err != nil {
		return "", fmt.Errorf("handler returned error: %w", err)
	}

	respMsg, err := newProtoMessage(matched.ResponseProto)
	if err != nil {
		return "", fmt.Errorf("response type %q: %w", matched.ResponseProto, err)
	}
	if err := proto.Unmarshal(respBytes, respMsg); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}
	jsonOut, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(respMsg)
	if err != nil {
		return "", fmt.Errorf("marshal response JSON: %w", err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (code %d)\n", matched.Name, matched.Code)
	fmt.Fprintf(&sb, "  request:  %s\n", jsonIn)
	fmt.Fprintf(&sb, "  response: %s\n", string(jsonOut))
	return sb.String(), nil
}

// kvArgsToJSON parses a whitespace-separated list of key=value pairs into
// a flat JSON object. Values are coerced by content:
//   - "true" / "false" → JSON bool
//   - parseable as int/float → JSON number
//   - anything else → JSON string
//
// Empty input returns "{}". Quoting is supported via the cmdsys tokenizer
// upstream; this function operates on already-tokenized + re-joined args.
func kvArgsToJSON(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}", nil
	}
	parts := strings.Fields(s)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, p := range parts {
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			return "", fmt.Errorf("expected key=value, got %q", p)
		}
		key, val := p[:eq], p[eq+1:]
		if i > 0 {
			sb.WriteByte(',')
		}
		// Key as JSON string.
		keyJSON, err := jsonStringEncode(key)
		if err != nil {
			return "", err
		}
		sb.WriteString(keyJSON)
		sb.WriteByte(':')
		// Value: bool / number / string.
		switch {
		case val == "true" || val == "false":
			sb.WriteString(val)
		case isNumericLiteral(val):
			sb.WriteString(val)
		default:
			vJSON, err := jsonStringEncode(val)
			if err != nil {
				return "", err
			}
			sb.WriteString(vJSON)
		}
	}
	sb.WriteByte('}')
	return sb.String(), nil
}

func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

// jsonStringEncode wraps s in JSON-quoted string form, escaping the
// chars that JSON requires escaped. Avoids a full json.Marshal for one
// string (and its allocation overhead in a console hot path).
func jsonStringEncode(s string) (string, error) {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String(), nil
}

// newProtoMessage looks up the proto message type by its full name in
// the global type registry (auto-populated by every imported .pb.go) and
// returns a fresh empty instance. Returns an error if the type isn't
// registered — which means the binary doesn't import the package that
// defines it.
func newProtoMessage(fullName string) (proto.Message, error) {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(fullName))
	if err != nil {
		return nil, fmt.Errorf("type %q not in global registry: %w", fullName, err)
	}
	return mt.New().Interface(), nil
}
