// Package universe — builtins_cluster.go
//
// cluster.overview — one-shot executive summary of the mesh. Reads coord-local
// state only (hostRegistry, gatewayRegistry, sessionRoutes, c.players,
// CellOwner). RouteCoordinator so a future non-coord caller surfaces a
// proper routing error rather than silently reading empty state.

package universe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/zenion/mmoserver/pkg/ops"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type clusterOverviewArgs struct{}

type clusterOverviewResult struct {
	Output string
}

// wireCompletionSources hooks dynamic completion providers into the Console
// so tab-completion on host/cell/gateway/session args surfaces live values
// from the coord's registries. Called once from startConsole.
func (c *Process) wireCompletionSources() {
	if c.console == nil {
		return
	}
	c.console.SetCompletionSource("hosts", func() []string {
		if c.hostRegistry != nil {
			hosts := c.hostRegistry.LiveHosts()
			ids := make([]string, 0, len(hosts))
			for _, h := range hosts {
				ids = append(ids, h.ID)
			}
			return ids
		}
		c.mu.RLock()
		defer c.mu.RUnlock()
		ids := make([]string, 0, len(c.Hosts))
		for id := range c.Hosts {
			ids = append(ids, id)
		}
		return ids
	})
	c.console.SetCompletionSource("cells", func() []string {
		c.mu.RLock()
		defer c.mu.RUnlock()
		ids := make([]string, 0, len(c.CellOwner))
		for cell := range c.CellOwner {
			ids = append(ids, cell.String())
		}
		return ids
	})
	c.console.SetCompletionSource("gateways", func() []string {
		if c.gatewayRegistry == nil {
			return nil
		}
		gws := c.gatewayRegistry.LiveGateways()
		ids := make([]string, 0, len(gws))
		for _, g := range gws {
			ids = append(ids, g.ID)
		}
		return ids
	})
	c.console.SetCompletionSource("players", func() []string {
		online := c.ActiveUsers()
		out := make([]string, 0, len(online))
		for u := range online {
			out = append(out, u)
		}
		return out
	})
	c.console.SetCompletionSource("sessions", func() []string {
		var ids []string
		c.sessionRoutes.ForEach(func(r *SessionRoute) bool {
			ids = append(ids, r.Key.String())
			return true
		})
		return ids
	})
	// Service framework completions. service-kinds: registered Kind.Name
	// values. service-ops: handler names captured by typed RegisterOp,
	// flattened across kinds (the completion API is context-free, so we
	// can't restrict to "ops of the kind in arg 1").
	c.console.SetCompletionSource("service-kinds", func() []string {
		return c.services.Names()
	})
	c.console.SetCompletionSource("service-ops", func() []string {
		if c.cfg.OpRouter == nil {
			return nil
		}
		schemas := c.cfg.OpRouter.Schema()
		out := make([]string, 0, len(schemas))
		for _, s := range schemas {
			if s.Name != "" {
				out = append(out, s.Name)
			}
		}
		return out
	})
	// Contextual source for the rest-field args of `service call`. Reads
	// the op name from the prior tokens, looks up its request proto type
	// via protoregistry, and returns "<jsonName>=" suggestions for each
	// field of the request message. Falls back to no suggestions if the
	// op isn't resolvable yet (e.g. user is still typing the op name).
	c.console.SetCompletionSourceCtx("service-call-args", func(tokens []string) []string {
		// tokens are pre-verb-resolution: ["service", "call", <kind>, <op>, ...rest...]
		// We need at least the op slot populated.
		if len(tokens) < 4 || c.cfg.OpRouter == nil {
			return nil
		}
		opSpec := tokens[3]
		var match *ops.OperationSchema
		for _, s := range c.cfg.OpRouter.Schema() {
			if s.Name == opSpec {
				cp := s
				match = &cp
				break
			}
			if code, err := strconv.ParseUint(opSpec, 10, 32); err == nil && uint32(code) == s.Code {
				cp := s
				match = &cp
				break
			}
		}
		if match == nil {
			return nil
		}
		mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(match.RequestProto))
		if err != nil {
			return nil
		}
		fields := mt.Descriptor().Fields()
		out := make([]string, 0, fields.Len())
		for i := 0; i < fields.Len(); i++ {
			out = append(out, string(fields.Get(i).JSONName())+"=")
		}
		return out
	})
}

func registerClusterBuiltins(reg *cmdsys.Registry, coord *Process) error {
	c := coord
	return reg.Register(cmdsys.Command{
		Verb:        "cluster.overview",
		Capability:  "cluster.overview",
		Description: "one-shot summary of hosts, gateways, cells, sessions, and load across the mesh",
		Route:       cmdsys.RouteCoordinator,
		Args:        clusterOverviewArgs{},
		Result:      clusterOverviewResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			return clusterOverviewResult{Output: buildOverview(c)}, nil
		},
	})
}

func buildOverview(c *Process) string {
	var sb strings.Builder
	now := time.Now()

	// Snapshots under read locks. Prefer hostRegistry when it has entries;
	// fall back to c.Hosts for single-process `all` / test configs that
	// don't go through the control plane.
	var hosts []*RemoteHost
	if c.hostRegistry != nil {
		hosts = c.hostRegistry.LiveHosts()
	}
	if len(hosts) == 0 {
		c.mu.RLock()
		for id, h := range c.Hosts {
			owned := make(map[string]bool, len(h.Cells))
			for _, hc := range h.Cells {
				owned[string(hc.MeshID)] = true
			}
			hosts = append(hosts, &RemoteHost{
				ID:            id,
				State:         RemoteHostLive,
				Local:         true,
				OwnedCells:    owned,
				RegisteredAt:  now,
				LastHeartbeat: now,
			})
		}
		c.mu.RUnlock()
	}
	var gateways []*RemoteGateway
	if c.gatewayRegistry != nil {
		gateways = c.gatewayRegistry.LiveGateways()
	}
	sessionCount := c.sessionRoutes.Len()

	c.mu.RLock()
	cellCount := len(c.CellOwner)
	localCellCount := len(c.Cells)
	playersActive := 0
	playersTotal := len(c.players)
	for _, loc := range c.players {
		if loc.Active {
			playersActive++
		}
	}
	coordEpoch := c.coordEpoch
	roleStr := c.roles.String()
	c.mu.RUnlock()

	// Header: roles + epoch + coarse counts.
	fmt.Fprintf(&sb, "  roles      : %s\n", roleStr)
	fmt.Fprintf(&sb, "  coord epoch: %d\n", coordEpoch)
	fmt.Fprintf(&sb, "  mesh       : %d host(s), %d gateway(s), %d cell(s), %d session(s)\n",
		len(hosts), len(gateways), cellCount, sessionCount)
	fmt.Fprintf(&sb, "  players    : %d online, %d total\n", playersActive, playersTotal-playersActive+playersActive)
	if localCellCount != cellCount {
		fmt.Fprintf(&sb, "  local cells: %d (of %d total)\n", localCellCount, cellCount)
	}

	// Hosts with per-host cell count + heartbeat age.
	if len(hosts) > 0 {
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
		fmt.Fprintf(&sb, "\n  hosts:\n")
		for _, h := range hosts {
			state := h.State.String()
			age := "---"
			if h.Local {
				state += "*"
			} else {
				age = now.Sub(h.LastHeartbeat).Truncate(time.Millisecond).String()
			}
			fmt.Fprintf(&sb, "    %-16s %-12s hb=%-10s cells=%d  grpc=%s\n",
				h.ID, state, age, len(h.OwnedCells), h.GrpcAddr)
		}
	}

	// Gateways.
	if len(gateways) > 0 {
		sort.Slice(gateways, func(i, j int) bool { return gateways[i].ID < gateways[j].ID })
		fmt.Fprintf(&sb, "\n  gateways:\n")
		for _, g := range gateways {
			state := g.State.String()
			age := "---"
			if g.Local {
				state += "*"
			} else {
				age = now.Sub(g.LastHeartbeat).Truncate(time.Millisecond).String()
			}
			wsAddr := g.WSAddr
			if wsAddr == "" {
				wsAddr = "(embedded)"
			}
			fmt.Fprintf(&sb, "    %-16s %-12s hb=%-10s sessions=%d  ws=%s\n",
				g.ID, state, age, len(g.Sessions), wsAddr)
		}
	}

	// Cell distribution: how many cells does each host own?
	distribution := make(map[string]int)
	c.Control.AllOwnedCells(func(_, hostID string) bool {
		distribution[hostID]++
		return true
	})
	// Also include local hosts even when cellToHostMap is empty (all-in-one mode).
	c.mu.RLock()
	for id := range c.Hosts {
		if _, ok := distribution[id]; !ok {
			distribution[id] = 0
		}
	}
	c.mu.RUnlock()

	if len(distribution) > 0 {
		keys := make([]string, 0, len(distribution))
		for k := range distribution {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&sb, "\n  cell distribution:\n")
		for _, k := range keys {
			fmt.Fprintf(&sb, "    %-16s %d\n", k, distribution[k])
		}
	}

	return sb.String()
}
