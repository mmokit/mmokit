package universe

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/engine"
)

// registerHostCommands registers the "host" command group on the
// console. Called in coordinator and all-in-one modes so operators can
// inspect the HostRegistry and simulate node crashes for S4 validation.
func (c *Coordinator) registerHostCommands(console *engine.Console) {
	hostGroup := engine.NewCommandGroup("host", "mesh", "manage and inspect registered remote hosts")

	hostGroup.Add(engine.Command{
		Name:        "list",
		Category:    "mesh",
		Description: "list all registered remote hosts (HostRegistry snapshot)",
		Fn: func(args []string) {
			c.printHostList(console)
		},
	})

	hostGroup.Add(engine.Command{
		Name:        "kill",
		Category:    "mesh",
		Usage:       "kill <host-id>",
		Description: "force-cancel a host's control stream to simulate a crash",
		Fn: func(args []string) {
			if len(args) < 1 {
				console.Print("  usage: host kill <host-id>\n")
				return
			}
			c.killHost(console, args[0])
		},
	})

	console.RegisterGroup(hostGroup)
}

func (c *Coordinator) printHostList(console *engine.Console) {
	// Coordinator / multi-process mode: enumerate from HostRegistry.
	if c.hostRegistry != nil {
		hosts := c.hostRegistry.LiveHosts()
		if len(hosts) == 0 {
			console.Print("  (no hosts registered)\n")
			return
		}
		sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("  %-16s %-12s %-10s %-22s %-6s\n",
			"HOST", "STATE", "HB-AGE", "GRPC-ADDR", "CELLS"))
		sb.WriteString(fmt.Sprintf("  %-16s %-12s %-10s %-22s %-6s\n",
			"----", "-----", "------", "---------", "-----"))
		now := time.Now()
		for _, h := range hosts {
			age := now.Sub(h.LastHeartbeat).Truncate(time.Millisecond)
			sb.WriteString(fmt.Sprintf("  %-16s %-12s %-10s %-22s %d\n",
				h.ID, h.State.String(), age.String(), h.GrpcAddr, len(h.OwnedCells)))
		}
		console.Print(sb.String())
		return
	}

	// All-in-one mode: enumerate local Hosts directly. There's no remote
	// registry, no heartbeats, no state machine — every local host is
	// implicitly Live with zero hb-age. cellToHostMap gives owned cell counts.
	c.mu.RLock()
	hostIDs := make([]string, 0, len(c.Hosts))
	for id := range c.Hosts {
		hostIDs = append(hostIDs, id)
	}
	cellsPerHost := make(map[string]int, len(c.Hosts))
	for _, hostID := range c.cellToHostMap {
		cellsPerHost[hostID]++
	}
	c.mu.RUnlock()
	sort.Strings(hostIDs)

	if len(hostIDs) == 0 {
		console.Print("  (no local hosts)\n")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("  %-16s %-12s %-22s %-6s\n",
		"HOST", "STATE", "GRPC-ADDR", "CELLS"))
	sb.WriteString(fmt.Sprintf("  %-16s %-12s %-22s %-6s\n",
		"----", "-----", "---------", "-----"))
	for _, id := range hostIDs {
		h := c.Hosts[id]
		grpcAddr := "(local)"
		if h.Network != nil {
			grpcAddr = h.Network.Addr()
		}
		// In single-host all-in-one with no cellToHostMap entries, attribute
		// every cell to the sole local host so the count isn't misleading.
		count := cellsPerHost[id]
		if count == 0 && len(c.Hosts) == 1 {
			count = len(c.Cells)
		}
		sb.WriteString(fmt.Sprintf("  %-16s %-12s %-22s %d\n",
			id, "Local", grpcAddr, count))
	}
	console.Print(sb.String())
}

func (c *Coordinator) killHost(console *engine.Console, hostID string) {
	if c.controlServer == nil {
		console.Print("  (meshControlServer not active — coordinator mode required)\n")
		return
	}
	if !c.controlServer.cancelStream(hostID) {
		console.Printf("  no stream for host %q\n", hostID)
		return
	}
	console.Printf("  host %s stream force-closed; crash-detection path will reassign its cells within ~3s\n", hostID)
}
