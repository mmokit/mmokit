package universe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
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
			fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-12s %s\n", "KIND", "INSTANCE", "HOST", "JOINED", "OPCODES")
			fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-12s %s\n", "----", "--------", "----", "------", "-------")
			for _, inst := range snap {
				age := time.Since(inst.JoinedAt).Truncate(time.Second).String()
				fmt.Fprintf(&sb, "  %-16s %-32s %-12s %-12s %v\n",
					inst.Kind, inst.InstanceID, inst.HostID, age, inst.OpCodes)
			}
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
				fmt.Fprintf(&sb, "  OpCodes:       %v\n", k.OpCodes)
				if k.MetricsPrefix != "" {
					fmt.Fprintf(&sb, "  MetricsPrefix: %s\n", k.MetricsPrefix)
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
				fmt.Fprintf(&sb, "    - %s host=%s joined=%s codes=%v\n",
					inst.InstanceID, inst.HostID, age, inst.OpCodes)
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
			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-16s %-12s %-30s %s\n", "NAME", "REQUIRES-DB", "OPCODES", "DESCRIPTION")
			fmt.Fprintf(&sb, "  %-16s %-12s %-30s %s\n", "----", "-----------", "-------", "-----------")
			for _, k := range kinds {
				fmt.Fprintf(&sb, "  %-16s %-12v %-30v %s\n", k.Name, k.RequiresDB, k.OpCodes, k.Description)
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
			ops := c.serviceRouting.AllOps()
			if len(ops) == 0 {
				return serviceOpsResult{Output: "  (no service ops registered; client ops route to cells by default)\n"}, nil
			}
			codes := make([]uint32, 0, len(ops))
			for code := range ops {
				codes = append(codes, code)
			}
			sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })

			var sb strings.Builder
			fmt.Fprintf(&sb, "  %-8s %-16s %s\n", "CODE", "KIND", "INSTANCES")
			fmt.Fprintf(&sb, "  %-8s %-16s %s\n", "----", "----", "---------")
			for _, code := range codes {
				kind := ops[code]
				insts := c.serviceRouting.InstancesOfKind(kind)
				idStrs := make([]string, 0, len(insts))
				for _, in := range insts {
					idStrs = append(idStrs, in.InstanceID)
				}
				fmt.Fprintf(&sb, "  %-8d %-16s %s\n", code, kind, strings.Join(idStrs, ","))
			}
			return serviceOpsResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return err
	}

	return nil
}
