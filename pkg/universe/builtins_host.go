package universe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mmokit/mmokit/pkg/cmdsys"
)

type hostListArgs struct{}

type hostListResult struct {
	Output string
}

type hostKillArgs struct {
	HostID string `cmd:"help=host ID to kill,complete=hosts"`
}

type hostKillResult struct {
	HostID string
	OK     bool
	Note   string
}

type hostDrainArgs struct {
	HostID string `cmd:"help=host ID to drain,complete=hosts"`
}

type hostDrainResult struct {
	HostID string
	Cells  int
	Note   string
	OK     bool
}

func registerHostBuiltins(reg *cmdsys.Registry, coord *Process) error {
	c := coord

	if err := reg.Register(cmdsys.Command{
		Verb:        "host.list",
		Capability:  "host.list",
		Description: "list all registered remote hosts (HostRegistry snapshot)",
		Route:       cmdsys.RouteLocal,
		Args:        hostListArgs{},
		Result:      hostListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			var sb strings.Builder
			if c.hostRegistry != nil {
				hosts := c.hostRegistry.LiveHosts()
				if len(hosts) == 0 {
					return hostListResult{Output: "  (no hosts registered)\n"}, nil
				}
				sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
				fmt.Fprintf(&sb, "  %-16s %-12s %-10s %-22s %-6s %-7s\n", "HOST", "STATE", "HB-AGE", "GRPC-ADDR", "CELLS", "SVC-ONLY")
				fmt.Fprintf(&sb, "  %-16s %-12s %-10s %-22s %-6s %-7s\n", "----", "-----", "------", "---------", "-----", "--------")
				now := time.Now()
				for _, h := range hosts {
					state := h.State.String()
					age := "---"
					if h.Local {
						state += "*"
					} else {
						age = now.Sub(h.LastHeartbeat).Truncate(time.Millisecond).String()
					}
					svcOnly := "-"
					if h.ServiceOnly {
						svcOnly = "yes"
					}
					fmt.Fprintf(&sb, "  %-16s %-12s %-10s %-22s %-6d %-7s\n",
						h.ID, state, age, h.GrpcAddr, len(h.OwnedCells), svcOnly)
				}
				return hostListResult{Output: sb.String()}, nil
			}
			// All-in-one mode
			c.mu.RLock()
			hostIDs := make([]string, 0, len(c.Hosts))
			for id := range c.Hosts {
				hostIDs = append(hostIDs, id)
			}
			c.mu.RUnlock()
			cellsPerHost := make(map[string]int)
			c.Control.AllOwnedCells(func(_, hostID string) bool {
				cellsPerHost[hostID]++
				return true
			})
			sort.Strings(hostIDs)
			if len(hostIDs) == 0 {
				return hostListResult{Output: "  (no local hosts)\n"}, nil
			}
			fmt.Fprintf(&sb, "  %-16s %-12s %-22s %-6s\n", "HOST", "STATE", "GRPC-ADDR", "CELLS")
			fmt.Fprintf(&sb, "  %-16s %-12s %-22s %-6s\n", "----", "-----", "---------", "-----")
			for _, id := range hostIDs {
				h := c.Hosts[id]
				grpcAddr := h.NetworkAddr()
				count := cellsPerHost[id]
				if count == 0 && len(c.Hosts) == 1 {
					count = len(c.Cells)
				}
				fmt.Fprintf(&sb, "  %-16s %-12s %-22s %d\n", id, "Local", grpcAddr, count)
			}
			return hostListResult{Output: sb.String()}, nil
		},
	}); err != nil {
		return fmt.Errorf("host.list: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "host.kill",
		Capability:  "host.kill",
		Description: "force-cancel a host's control stream to simulate a crash",
		Route:       cmdsys.RouteLocal,
		Args:        hostKillArgs{},
		Result:      hostKillResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(hostKillArgs)
			if c.controlServer == nil {
				return hostKillResult{HostID: args.HostID, OK: false,
					Note: "meshControlServer not active — coordinator mode required"}, nil
			}
			if !c.controlServer.cancelStream(args.HostID) {
				return hostKillResult{HostID: args.HostID, OK: false,
					Note: fmt.Sprintf("no stream for host %q", args.HostID)}, nil
			}
			return hostKillResult{
				HostID: args.HostID,
				OK:     true,
				Note:   "stream force-closed; crash-detection path will reassign its cells within ~3s",
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("host.kill: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "host.drain",
		Capability:  "host.drain",
		Description: "migrate every cell currently owned by hostID to surviving hosts",
		Examples:    []string{"host drain host-2"},
		Route:       cmdsys.RouteLocal,
		Args:        hostDrainArgs{},
		Result:      hostDrainResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(hostDrainArgs)
			if args.HostID == "" {
				return nil, fmt.Errorf("host.drain: host ID required")
			}
			// Snapshot cell count before drainHost potentially clears the
			// ownership map so the result is meaningful.
			owned := 0
			if c.hostRegistry != nil {
				for _, h := range c.hostRegistry.LiveHosts() {
					if h.ID == args.HostID {
						owned = len(h.OwnedCells)
						break
					}
				}
			}
			ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := c.drainHost(ctx2, args.HostID); err != nil {
				return hostDrainResult{HostID: args.HostID, Cells: owned, OK: false, Note: err.Error()}, nil
			}
			return hostDrainResult{
				HostID: args.HostID,
				Cells:  owned,
				OK:     true,
				Note:   fmt.Sprintf("drained %d cells", owned),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("host.drain: %w", err)
	}

	return nil
}
