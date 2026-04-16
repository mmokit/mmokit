// Package universe — builtins_session.go
//
// session.list / session.info — inspect live client sessions via the
// coordinator's sessionRoutes table. Read-only; RouteLocal because sessions
// live in the coord's process regardless of where the game lives.

package universe

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/engine"
)

type sessionListArgs struct{}

type SessionRow struct {
	Gateway  string
	ConnID   uint32
	Username string
	Host     string
	Cell     string
	Epoch    uint64
}

type sessionListResult struct {
	Sessions []SessionRow `cmd:"table"`
}

type sessionInfoArgs struct {
	Key string `cmd:"help=session key in the form <gatewayID>:<connID>"`
}

type sessionInfoResult struct {
	Gateway   string
	ConnID    uint32
	Username  string
	Host      string
	Cell      string
	Epoch     uint64
	Online    bool
	NodeIndex string
}

func registerSessionBuiltins(reg *cmdsys.Registry, _ *engine.Console, coord *Coordinator) error {
	c := coord

	if err := reg.Register(cmdsys.Command{
		Verb:        "session.list",
		Capability:  "session.list",
		Description: "list all active client sessions with their current host + cell",
		Route:       cmdsys.RouteLocal,
		Args:        sessionListArgs{},
		Result:      sessionListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			var rows []SessionRow
			c.sessionRoutes.ForEach(func(r *SessionRoute) bool {
				rows = append(rows, SessionRow{
					Gateway:  r.Key.GatewayID,
					ConnID:   r.Key.ConnID,
					Username: r.Username,
					Host:     r.HostID,
					Cell:     r.CellID,
					Epoch:    r.Epoch,
				})
				return true
			})
			sort.Slice(rows, func(i, j int) bool {
				if rows[i].Gateway != rows[j].Gateway {
					return rows[i].Gateway < rows[j].Gateway
				}
				return rows[i].ConnID < rows[j].ConnID
			})
			return sessionListResult{Sessions: rows}, nil
		},
	}); err != nil {
		return fmt.Errorf("session.list: %w", err)
	}

	if err := reg.Register(cmdsys.Command{
		Verb:        "session.info",
		Capability:  "session.info",
		Description: "show detail for one session by <gatewayID>:<connID>",
		Route:       cmdsys.RouteLocal,
		Args:        sessionInfoArgs{},
		Result:      sessionInfoResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(sessionInfoArgs)
			gwID, connID, err := parseSessionKey(args.Key)
			if err != nil {
				return nil, err
			}
			route, ok := c.sessionRoutes.Get(SessionKey{GatewayID: gwID, ConnID: connID})
			if !ok {
				return nil, fmt.Errorf("no session for %s", args.Key)
			}
			online := false
			c.mu.RLock()
			if loc := c.players[route.Username]; loc != nil {
				online = loc.Active
			}
			c.mu.RUnlock()
			return sessionInfoResult{
				Gateway:  route.Key.GatewayID,
				ConnID:   route.Key.ConnID,
				Username: route.Username,
				Host:     route.HostID,
				Cell:     route.CellID,
				Epoch:    route.Epoch,
				Online:   online,
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("session.info: %w", err)
	}

	return nil
}

// parseSessionKey splits "gatewayID:connID" into its parts.
func parseSessionKey(s string) (string, uint32, error) {
	idx := strings.LastIndexByte(s, ':')
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, fmt.Errorf("session key must be <gatewayID>:<connID>")
	}
	gwID := s[:idx]
	connID, err := strconv.ParseUint(s[idx+1:], 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("connID: %w", err)
	}
	return gwID, uint32(connID), nil
}
