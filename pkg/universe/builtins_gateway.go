// Package universe — builtins_gateway.go
//
// gateway.list / gateway.info — inspect registered gateway processes via
// the coordinator's GatewayRegistry. Read-only; RouteLocal.

package universe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

type gatewayListArgs struct{}

type GatewayRow struct {
	ID           string
	State        string
	WSAddr       string
	GRPCAddr     string
	HeartbeatAge string
	Sessions     int
}

type gatewayListResult struct {
	Gateways []GatewayRow `cmd:"table"`
}

type gatewayInfoArgs struct {
	ID string `cmd:"help=gateway id,complete=gateways"`
}

type gatewayInfoResult struct {
	ID            string
	State         string
	WSAddr        string
	GRPCAddr      string
	Local         bool
	RegisteredAt  string
	LastHeartbeat string
	HeartbeatAge  string
	SessionCount  int
	// For the session list use `session.list` (filterable there) — keeping
	// gateway.info focused on gateway-unique state.
}

func registerGatewayBuiltins(reg *cmdsys.Registry, coord *Coordinator) error {
	c := coord

	if err := reg.Register(cmdsys.Command{
		Verb:        "gateway.list",
		Capability:  "gateway.list",
		Description: "list all registered gateways with state, addresses, and session counts",
		Route:       cmdsys.RouteLocal,
		Args:        gatewayListArgs{},
		Result:      gatewayListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			if c.gatewayRegistry == nil {
				return gatewayListResult{}, nil
			}
			gws := c.gatewayRegistry.LiveGateways()
			sort.Slice(gws, func(i, j int) bool { return gws[i].ID < gws[j].ID })
			now := time.Now()
			rows := make([]GatewayRow, 0, len(gws))
			for _, g := range gws {
				state := g.State.String()
				age := "---"
				if g.Local {
					state += "*"
				} else {
					age = now.Sub(g.LastHeartbeat).Truncate(time.Millisecond).String()
				}
				rows = append(rows, GatewayRow{
					ID:           g.ID,
					State:        state,
					WSAddr:       g.WSAddr,
					GRPCAddr:     g.GRPCAddr,
					HeartbeatAge: age,
					Sessions:     len(g.Sessions),
				})
			}
			return gatewayListResult{Gateways: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("gateway.list: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "gateway.info",
		Capability:  "gateway.info",
		Description: "show detail for one gateway including its session list",
		Route:       cmdsys.RouteLocal,
		Args:        gatewayInfoArgs{},
		Result:      gatewayInfoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(gatewayInfoArgs)
			if c.gatewayRegistry == nil {
				return nil, fmt.Errorf("no gateway registry (not a coordinator process)")
			}
			gw := c.gatewayRegistry.Get(args.ID)
			if gw == nil {
				return nil, fmt.Errorf("no gateway %q", args.ID)
			}
			now := time.Now()
			age := "---"
			if !gw.Local {
				age = now.Sub(gw.LastHeartbeat).Truncate(time.Millisecond).String()
			}
			state := gw.State.String()
			if gw.Local {
				state += "*"
			}
			return gatewayInfoResult{
				ID:            gw.ID,
				State:         state,
				WSAddr:        nonEmpty(gw.WSAddr, "(embedded)"),
				GRPCAddr:      gw.GRPCAddr,
				Local:         gw.Local,
				RegisteredAt:  gw.RegisteredAt.Format(time.RFC3339),
				LastHeartbeat: gw.LastHeartbeat.Format(time.RFC3339),
				HeartbeatAge:  age,
				SessionCount:  len(gw.Sessions),
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("gateway.info: %w", err)
	}

	return nil
}

// nonEmpty returns s if non-empty, else fallback.
func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
