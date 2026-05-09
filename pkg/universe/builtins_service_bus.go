// Package universe — builtins_service_bus.go
//
// service.bus.list — observability shim for the cluster-wide service
// event bus (Phase 3, Task 12). Snapshots the local Bus routing cache
// (typeName → []processID, the publisher-side view) and, on a
// coordinator-bearing process, the authoritative coord-side router.
//
// Operators use this to diagnose "why didn't my subscriber receive
// event X" — the local cache reveals whether THIS process knows about
// a subscriber for that type, and the coord snapshot reveals whether
// coord ever recorded the subscribe in the first place. Disagreement
// between the two surfaces a PeerList propagation bug; agreement
// shifts blame to the publish-side dispatch path.
//
// Read-only; RouteLocal — every pane returns its own local view, which
// is the diagnostic point.

package universe

import (
	"context"
	"fmt"
	"sort"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type serviceBusListArgs struct {
	Type string `cmd:"optional,name=type,help=filter rows to one fully-qualified event type name"`
}

// ServiceBusRoutingRow is one row in the rendered routing-table view.
// The CoordRouter column distinguishes "coord knows about this subscriber"
// (Local + Coord both populated) from "local cache stale" (Local
// populated, Coord empty) on a coordinator process.
type ServiceBusRoutingRow struct {
	TypeName    string
	LocalCache  string // comma-joined processIDs from the local Bus.RoutingCacheSnapshot
	CoordRouter string // comma-joined processIDs from the coord-side serviceEventRouter; "(non-coord)" on processes that don't host the router
}

type serviceBusListResult struct {
	Rows   []ServiceBusRoutingRow `cmd:"table"`
	Notice string
}

// registerServiceBusBuiltins wires `service.bus.list` into the
// dispatcher. Sister of registerServiceBuiltins; lives in its own file
// because it touches the bus + serviceEventRouter rather than the
// service kind / typed-op registry.
func registerServiceBusBuiltins(reg *cmdsys.Registry, coord *Process) error {
	if err := reg.Register(cmdsys.Command{
		Verb:        "service.bus.list",
		Capability:  "service.list",
		Description: "snapshot the local service-event routing cache + (on coord) the authoritative router",
		Examples: []string{
			"service bus list",
			"service bus list --type=github.com/zenion/mmoserver/pkg/service.SessionEnterEvent",
		},
		Route:  cmdsys.RouteLocal,
		Args:   serviceBusListArgs{},
		Result: serviceBusListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(serviceBusListArgs)
			if coord == nil {
				return serviceBusListResult{Notice: "no Process in scope"}, nil
			}

			var local map[string][]string
			if bus := coord.Bus(); bus != nil {
				local = bus.RoutingCacheSnapshot()
			}

			var coordSnap map[string][]string
			if coord.serviceEventRouter != nil {
				coordSnap = coord.serviceEventRouter.Snapshot()
			}

			// Union the type-name keys so a row with a coord entry but no
			// local cache entry (or vice versa) still surfaces.
			seen := map[string]struct{}{}
			for k := range local {
				seen[k] = struct{}{}
			}
			for k := range coordSnap {
				seen[k] = struct{}{}
			}
			types := make([]string, 0, len(seen))
			for k := range seen {
				if args.Type != "" && k != args.Type {
					continue
				}
				types = append(types, k)
			}
			sort.Strings(types)

			rows := make([]ServiceBusRoutingRow, 0, len(types))
			for _, t := range types {
				row := ServiceBusRoutingRow{
					TypeName:   t,
					LocalCache: joinSorted(local[t]),
				}
				if coordSnap != nil {
					row.CoordRouter = joinSorted(coordSnap[t])
				} else {
					row.CoordRouter = "(non-coord)"
				}
				rows = append(rows, row)
			}

			if len(rows) == 0 {
				if args.Type != "" {
					return serviceBusListResult{Notice: fmt.Sprintf("no routing entries for type %q", args.Type)}, nil
				}
				return serviceBusListResult{Notice: "no routing entries (no remote subscribers cached)"}, nil
			}
			return serviceBusListResult{Rows: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("service.bus.list: %w", err)
	}
	return nil
}

// joinSorted returns a comma-joined sorted copy of ids. "-" for empty
// so the table renders a stable placeholder rather than a blank cell.
func joinSorted(ids []string) string {
	if len(ids) == 0 {
		return "-"
	}
	cp := append([]string(nil), ids...)
	sort.Strings(cp)
	out := ""
	for i, id := range cp {
		if i > 0 {
			out += ","
		}
		out += id
	}
	return out
}
