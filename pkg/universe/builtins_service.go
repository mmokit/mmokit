package universe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/ops"
)

type serviceListArgs struct{}

type serviceListResult struct {
	Output string
}

type serviceInfoArgs struct {
	Kind string `cmd:"help=service kind name"`
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

	return nil
}
