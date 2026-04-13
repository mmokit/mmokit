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
	if c.hostRegistry == nil {
		console.Print("  (HostRegistry not active — coordinator mode required)\n")
		return
	}
	hosts := c.hostRegistry.LiveHosts()
	if len(hosts) == 0 {
		console.Print("  (no hosts registered)\n")
		return
	}
	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].ID < hosts[j].ID
	})

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
