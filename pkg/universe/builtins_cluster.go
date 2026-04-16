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
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type clusterOverviewArgs struct{}

type clusterOverviewResult struct {
	Output string
}

// wireCompletionSources hooks dynamic completion providers into the Console
// so tab-completion on host/cell/gateway/session args surfaces live values
// from the coord's registries. Called once from startConsole.
func (c *Coordinator) wireCompletionSources() {
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
	c.console.SetCompletionSource("sessions", func() []string {
		var ids []string
		c.sessionRoutes.ForEach(func(r *SessionRoute) bool {
			ids = append(ids, r.Key.String())
			return true
		})
		return ids
	})
}

func registerClusterBuiltins(reg *cmdsys.Registry, _ *engine.Console, coord *Coordinator) error {
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

func buildOverview(c *Coordinator) string {
	var sb strings.Builder
	now := time.Now()

	// Snapshots under read locks.
	var hosts []*RemoteHost
	if c.hostRegistry != nil {
		hosts = c.hostRegistry.LiveHosts()
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
	c.mu.RLock()
	distribution := make(map[string]int, len(c.CellOwner))
	for _, hostID := range c.cellToHostMap {
		distribution[hostID]++
	}
	// Also include local hosts even when cellToHostMap is empty (all-in-one mode).
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
